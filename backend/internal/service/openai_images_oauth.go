package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ChatGPT-authenticated Codex retired gpt-5.4-mini on 2026-08-31.
// Keep the supported orchestration model separate from the requested image model.
const openAIImagesResponsesMainModel = "gpt-5.6-luna"

type openAIImageOAuthResult struct {
	Result        string
	URL           string
	RevisedPrompt string
	OutputFormat  string
}

// Tool usage belongs to the image model; response.usage belongs to the
// orchestration model. Missing tool usage must remain unknown.
type openAIImagesOAuthUsageState struct {
	ImageUsage         *OpenAIUsage
	SVGUsage           []OpenAIImageSVGUsage
	SVGUsageIncomplete bool
	seenSVG            map[string]struct{}
	missingSVG         map[string]struct{}
}

func (state *openAIImagesOAuthUsageState) consumeResponse(response gjson.Result) {
	if value := response.Get("tool_usage.image_gen"); value.IsObject() {
		body := []byte(`{"usage":` + value.Raw + `}`)
		if usage, ok := extractOpenAIImageUsageFromJSONBytes(body); ok {
			state.ImageUsage = &usage
		}
	}
	for _, item := range response.Get("output").Array() {
		state.consumeSVG(item)
	}
}

func (state *openAIImagesOAuthUsageState) consumeSVG(item gjson.Result) {
	if item.Get("type").String() != "image_generation_call" {
		return
	}
	key := item.Get("id").String()
	if key == "" {
		key = item.Get("result").String() + "|" + item.Get("output_format").String()
	}
	value := item.Get("svg_usage")
	if !value.Exists() || value.Type == gjson.Null {
		if item.Get("output_format").String() == "svg" && !openAIImagesOAuthResponseFailed(item) {
			if _, seen := state.seenSVG[key]; !seen {
				if state.missingSVG == nil {
					state.missingSVG = make(map[string]struct{})
				}
				state.missingSVG[key] = struct{}{}
			}
		}
		return
	}
	delete(state.missingSVG, key)
	if state.seenSVG == nil {
		state.seenSVG = make(map[string]struct{})
	}
	if _, seen := state.seenSVG[key]; seen {
		return
	}
	state.seenSVG[key] = struct{}{}
	if usage, ok := parseOpenAIImageSVGUsage(value); ok {
		state.SVGUsage = append(state.SVGUsage, usage)
	} else {
		state.SVGUsageIncomplete = true
	}
}

func (state *openAIImagesOAuthUsageState) apply(result *OpenAIForwardResult, requestBody []byte) {
	if state.ImageUsage != nil && result.ImageCount > 0 && state.ImageUsage.ImageOutputTokens <= 0 {
		// Hosted-tool families with no recorded usage can report zero counts.
		// A completed image does not establish that its missing rendering was free.
		state.ImageUsage.ImageUsageComplete = false
	}
	result.ImageUsage = state.ImageUsage
	result.ImageOrchestrationModel = openAIImagesResponsesMainModel
	result.SVGUsage = state.SVGUsage
	result.SVGUsageIncomplete = state.SVGUsageIncomplete || len(state.missingSVG) > 0 || (result.ImageCount > 0 && gjson.GetBytes(requestBody, "output_format").String() == "svg" && len(state.SVGUsage) == 0)
}

func openAIImagesOAuthClientImageUsage(usage *OpenAIUsage) map[string]any {
	payload := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.OutputTokens,
	}
	if usage.ImageUsageComplete {
		payload["input_tokens_details"] = map[string]int{"text_tokens": usage.InputTokens - usage.ImageInputTokens, "image_tokens": usage.ImageInputTokens}
		payload["output_tokens_details"] = map[string]int{"text_tokens": usage.OutputTokens - usage.ImageOutputTokens, "image_tokens": usage.ImageOutputTokens}
		if usage.CacheReadInputTokens > 0 {
			payload["cached_tokens"] = usage.CacheReadInputTokens
			payload["cached_tokens_details"] = map[string]int{"cached_text_tokens": usage.CacheReadInputTokens - usage.CacheReadImageTokens, "cached_image_tokens": usage.CacheReadImageTokens}
		}
	}
	return payload
}

func mergeOpenAIImagesOAuthResult(total, result *OpenAIForwardResult) {
	total.ImageCount += result.ImageCount
	addOpenAIImagesOAuthUsage(&total.Usage, result.Usage)
	if total.ImageUsage == nil {
		if result.ImageUsage != nil {
			copy := *result.ImageUsage
			copy.ImageUsageComplete = false // An earlier image request had no tool usage.
			total.ImageUsage = &copy
		}
	} else if result.ImageUsage == nil {
		total.ImageUsage.ImageUsageComplete = false
	} else {
		addOpenAIImagesOAuthUsage(total.ImageUsage, *result.ImageUsage)
	}
	total.SVGUsage = append(total.SVGUsage, result.SVGUsage...)
	total.SVGUsageIncomplete = total.SVGUsageIncomplete || result.SVGUsageIncomplete
}

func (s *OpenAIGatewayService) forwardImagesGenerationsOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal bool) ([]byte, *OpenAIForwardResult, error) {
	return s.forwardImagesOAuth(ctx, c, account, body, writeOriginal, false)
}

// Responses has no image tool n option. Repeat single-image requests locally
// and keep one Images API response (or one final streaming terminator).
func (s *OpenAIGatewayService) forwardImagesOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal, edit bool) ([]byte, *OpenAIForwardResult, error) {
	count := 1
	if n := gjson.GetBytes(body, "n"); n.Exists() && n.Type != gjson.Null {
		if n.Type != gjson.Number || n.Float() != float64(n.Int()) || n.Int() < 1 || n.Int() > 10 {
			return nil, nil, &OpenAIImageRequestError{Message: "n must be an integer between 1 and 10"}
		}
		count = int(n.Int())
	}
	if count > 1 && bc.IsCentral(ctx) {
		return nil, nil, &OpenAIImageRequestError{Message: "OAuth image requests with central billing currently require n=1"}
	}
	stream := openAIImagesRequestWantsStream(body)
	if stream && !writeOriginal {
		return nil, nil, errors.New("streaming codex image bridge is not supported")
	}
	startTime := time.Now()
	var total *OpenAIForwardResult
	var data []json.RawMessage
	var createdAt int64
	for total == nil || total.ImageCount < count {
		imageIndex := 0
		var priorUsage OpenAIUsage
		var priorImageUsage *OpenAIUsage
		if total != nil {
			imageIndex = total.ImageCount
			priorUsage = total.Usage
			priorImageUsage = total.ImageUsage
		}
		imagesBody, result, err := s.forwardSingleImageOAuth(ctx, c, account, body, stream && writeOriginal, edit, imageIndex, count-imageIndex, priorUsage, priorImageUsage)
		if err != nil {
			if total != nil {
				if result != nil {
					mergeOpenAIImagesOAuthResult(total, result)
				}
				total.Duration = time.Since(startTime)
				// A previous request has already generated images. Retrying the entire
				// operation on another account would duplicate successful work.
				var publicErr *OpenAIImageUpstreamError
				if errors.As(err, &publicErr) {
					copy := *publicErr
					publicErr = &copy
				} else {
					var failoverErr *UpstreamFailoverError
					if errors.As(err, &failoverErr) {
						publicErr = extractOpenAIImageUpstreamError(failoverErr.ResponseBody)
					}
				}
				if stream && result == nil && c != nil {
					var errorFields any = map[string]any{"type": "upstream_error", "message": "Image request failed before all requested images were generated"}
					if publicErr != nil {
						errorFields = publicErr
					}
					payload, _ := json.Marshal(map[string]any{"type": "error", "error": errorFields})
					_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
					if flusher, ok := c.Writer.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				if publicErr != nil {
					return nil, total, fmt.Errorf("image request failed after %d completed images: %w", total.ImageCount, publicErr)
				}
				return nil, total, fmt.Errorf("image request failed after %d completed images: %v", total.ImageCount, err)
			}
			return imagesBody, result, err
		}
		if result == nil || result.ImageCount == 0 {
			return nil, total, errors.New("openai images oauth response did not contain image results")
		}
		if total == nil {
			copy := *result
			total = &copy
		} else {
			mergeOpenAIImagesOAuthResult(total, result)
		}
		if !stream {
			var payload struct {
				Created int64             `json:"created"`
				Data    []json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(imagesBody, &payload); err != nil {
				return nil, total, err
			}
			if createdAt == 0 {
				createdAt = payload.Created
			}
			data = append(data, payload.Data...)
		}
	}
	total.Duration = time.Since(startTime)
	if stream {
		if c != nil {
			_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return nil, total, nil
	}
	payload := map[string]any{"created": createdAt, "data": data}
	if total.ImageUsage != nil {
		payload["usage"] = openAIImagesOAuthClientImageUsage(total.ImageUsage)
	} else if !strings.HasPrefix(total.Model, "gpt-image-2.6") && hasOpenAIImagesOAuthUsage(total.Usage) {
		payload["usage"] = total.Usage // Compatibility for older upstreams that omit tool usage.
	}
	if len(total.SVGUsage) == 1 {
		payload["svg_usage"] = total.SVGUsage[0]
	}
	imagesBody, err := json.Marshal(payload)
	if err != nil {
		return nil, total, err
	}
	if writeOriginal && c != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), total.ResponseHeaders, s.responseHeaderFilter)
		c.Data(http.StatusOK, "application/json", imagesBody)
	}
	return imagesBody, total, nil
}

func addOpenAIImagesOAuthUsage(total *OpenAIUsage, usage OpenAIUsage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
	total.ImageOutputTokens += usage.ImageOutputTokens
	total.ImageInputTokens += usage.ImageInputTokens
	total.CacheReadImageTokens += usage.CacheReadImageTokens
	total.ImageUsageComplete = total.ImageUsageComplete && usage.ImageUsageComplete
}

func hasOpenAIImagesOAuthUsage(usage OpenAIUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageOutputTokens > 0 || usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0
}

func (s *OpenAIGatewayService) forwardSingleImageOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal, edit bool, imageIndex, maxImages int, priorUsage OpenAIUsage, priorImageUsage *OpenAIUsage) ([]byte, *OpenAIForwardResult, error) {
	if err := ValidateBillingImageRequest(ctx, body); err != nil {
		return nil, nil, err
	}
	startTime := time.Now()
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		model = "gpt-image-2"
	}
	responsesBody, err := buildOpenAIImagesOAuthResponsesRequest(body, model)
	if err != nil {
		return nil, nil, err
	}
	eventPrefix := "image_generation"
	if edit {
		responsesBody, err = addOpenAIImageEditInputs(responsesBody, body)
		if err != nil {
			return nil, nil, err
		}
		eventPrefix = "image_edit"
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, nil, err
	}

	upstreamReq, err := s.buildUpstreamImagesOAuthRequest(ctx, c, account, responsesBody, token, openAIImagesOAuthSessionSeed(body))
	if err != nil {
		return nil, nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	upstreamStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		body := []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)
		return body, nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body}
	}
	if resp == nil {
		body := []byte(`{"error":{"type":"upstream_error","message":"Upstream response is nil"}}`)
		return body, nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body}
	}
	defer func() { _ = resp.Body.Close() }()

	if openAIImagesRequestWantsStream(body) && resp.StatusCode < 400 {
		result, streamErr := s.handleImagesOAuthStream(resp, c, body, model, startTime, writeOriginal, eventPrefix, imageIndex, maxImages, priorUsage, priorImageUsage)
		return nil, result, streamErr
	}

	respBody, readErr := ReadUpstreamImagesResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if readErr != nil {
		return nil, nil, readErr
	}
	if resp.StatusCode >= 400 {
		var upstreamErr error = fmt.Errorf("openai images oauth upstream returned status %d", resp.StatusCode)
		if parsed := extractOpenAIImageUpstreamError(respBody); parsed != nil {
			upstreamErr = parsed
		}
		recordOpenAIImageUpstreamError(c, resp.StatusCode, upstreamErr)
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			return respBody, nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, ResponseHeaders: resp.Header}
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		if c != nil && !c.Writer.Written() {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
			c.Data(resp.StatusCode, contentType, respBody)
		}
		return respBody, nil, upstreamErr
	}

	imagesBody, usage, imageCount, usageState, err := buildOpenAIImagesOAuthClientResponseWithUsage(respBody, body)
	if err != nil {
		recordOpenAIImageUpstreamError(c, resp.StatusCode, err)
		return respBody, nil, err
	}
	if imageCount > maxImages {
		images := gjson.GetBytes(imagesBody, "data").Array()[:maxImages]
		limited := make([]json.RawMessage, 0, len(images))
		for _, image := range images {
			limited = append(limited, json.RawMessage(image.Raw))
		}
		imagesBody, err = sjson.SetBytes(imagesBody, "data", limited)
		if err != nil {
			return nil, nil, err
		}
		imageCount = maxImages
	}

	result := &OpenAIForwardResult{
		RequestID:        strings.TrimSpace(resp.Header.Get("x-request-id")),
		Usage:            usage,
		Model:            model,
		UpstreamModel:    model,
		Stream:           false,
		ResponseHeaders:  resp.Header.Clone(),
		Duration:         time.Since(startTime),
		ImageCount:       imageCount,
		ImageSize:        normalizeOpenAIImageSize(gjson.GetBytes(body, "size").String()),
		ImageBillingTier: billingImageTier(body),
	}
	usageState.apply(result, body)
	return imagesBody, result, nil
}

func buildOpenAIImagesOAuthResponsesRequest(imagesBody []byte, imageModel string) ([]byte, error) {
	if !gjson.ValidBytes(imagesBody) {
		return nil, errors.New("invalid JSON request body")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(imagesBody, "prompt").String())
	if prompt == "" {
		return nil, errors.New("image generation prompt is empty")
	}
	imageModel = strings.TrimSpace(imageModel)
	if imageModel == "" {
		imageModel = "gpt-image-2"
	}

	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":false,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	req, _ = sjson.SetBytes(req, "model", openAIImagesResponsesMainModel)
	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	req, _ = sjson.SetRawBytes(req, "input", input)

	tool := []byte(`{"type":"image_generation","action":"generate","model":""}`)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)
	if gjson.GetBytes(imagesBody, "stream").Bool() {
		if partialImages := gjson.GetBytes(imagesBody, "partial_images"); partialImages.Exists() && partialImages.Int() >= 0 {
			tool, _ = sjson.SetBytes(tool, "partial_images", partialImages.Int())
		}
	}
	for _, key := range []string{"size", "quality", "background", "output_format", "moderation", "complexity", "input_fidelity"} {
		if key == "input_fidelity" && (imageModel == "gpt-image-2" || strings.HasPrefix(imageModel, "gpt-image-2-")) {
			continue
		}
		if value := strings.TrimSpace(gjson.GetBytes(imagesBody, key).String()); value != "" {
			tool, _ = sjson.SetBytes(tool, key, value)
		}
	}
	if imageModel == "gpt-image-2" || strings.HasPrefix(imageModel, "gpt-image-2-") {
		// Omitted tool complexity can inherit the orchestration model's medium
		// reasoning effort, which Codex rejects for GPT Image 2. Keep rendering
		// quality independent and explicitly avoid that inherited value.
		tool, _ = sjson.SetBytes(tool, "complexity", "high")
	}
	for _, key := range []string{"output_compression", "frame_count", "frame_duration_ms", "svg"} {
		if value := gjson.GetBytes(imagesBody, key); value.Exists() && value.Type != gjson.Null {
			tool, _ = sjson.SetRawBytes(tool, key, []byte(value.Raw))
		}
	}
	identifier := gjson.GetBytes(imagesBody, "safety_identifier").String()
	if strings.TrimSpace(identifier) == "" {
		identifier = gjson.GetBytes(imagesBody, "user").String()
	}
	if strings.TrimSpace(identifier) != "" {
		req, _ = sjson.SetBytes(req, "safety_identifier", identifier)
	}
	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	req, _ = sjson.SetRawBytes(req, "tools.-1", tool)
	return req, nil
}

func (s *OpenAIGatewayService) handleImagesOAuthStream(resp *http.Response, c *gin.Context, requestBody []byte, model string, startTime time.Time, writeOriginal bool, eventPrefix string, imageIndex, maxImages int, priorUsage OpenAIUsage, priorImageUsage *OpenAIUsage) (*OpenAIForwardResult, error) {
	if c == nil {
		return nil, errors.New("gin context is nil")
	}
	if !writeOriginal {
		return nil, errors.New("streaming codex image bridge is not supported")
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := OpenAIUsage{}
	usageState := openAIImagesOAuthUsageState{}
	createdAt := time.Now().Unix()
	var finalImages []openAIImageOAuthResult
	seenFinalImages := map[string]struct{}{}
	imageCount := 0
	sawTerminal := false
	var streamErr error
	completedEmitted := false
	clientDisconnected := false
	var firstTokenMs *int
	if _, err := fmt.Fprint(c.Writer, ":\n\n"); err != nil {
		clientDisconnected = true
	} else {
		flusher.Flush()
	}

	emitPayload := func(payload map[string]any) error {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if clientDisconnected {
			return nil
		}
		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", encoded); err != nil {
			clientDisconnected = true
			return nil
		}
		flusher.Flush()
		return nil
	}
	emitError := func(err error) {
		if streamErr != nil {
			return
		}
		streamErr = err
		recordOpenAIImageUpstreamError(c, resp.StatusCode, err)
		var upstreamErr *OpenAIImageUpstreamError
		if errors.As(err, &upstreamErr) {
			_ = emitPayload(map[string]any{"type": "error", "error": upstreamErr})
			return
		}
		_ = emitPayload(map[string]any{"type": "error", "error": map[string]any{"type": "upstream_error", "message": sanitizeUpstreamErrorMessage(err.Error())}})
	}
	emitFailure := func(message string) { emitError(errors.New(sanitizeUpstreamErrorMessage(message))) }
	emitUpstreamFailure := func(payload gjson.Result, fallback string) {
		if upstreamErr := parseOpenAIImageUpstreamError(payload); upstreamErr != nil {
			emitError(upstreamErr)
			return
		}
		emitFailure(fallback)
	}
	appendFinalImage := func(image openAIImageOAuthResult) {
		if image.Result == "" && image.URL == "" {
			return
		}
		key := image.OutputFormat + "|" + image.Result + "|" + image.URL
		if _, ok := seenFinalImages[key]; ok {
			return
		}
		seenFinalImages[key] = struct{}{}
		finalImages = append(finalImages, image)
	}
	appendFinalItem := func(item gjson.Result) {
		usageState.consumeSVG(item)
		if !item.Exists() || item.Get("type").String() != "image_generation_call" {
			return
		}
		if openAIImagesOAuthResponseFailed(item) {
			emitUpstreamFailure(item, "openai images oauth image generation did not complete")
			return
		}
		appendFinalImage(openAIImageOAuthResult{
			Result:        strings.TrimSpace(item.Get("result").String()),
			URL:           strings.TrimSpace(item.Get("url").String()),
			RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
			OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
		})
	}
	emitCompletedImages := func() error {
		if completedEmitted {
			return nil
		}
		if len(finalImages) == 0 {
			return nil
		}
		images := finalImages
		if len(images) > maxImages {
			images = images[:maxImages]
		}
		for idx, image := range images {
			payload := map[string]any{
				"type":          eventPrefix + ".completed",
				"image_index":   imageIndex + idx,
				"created_at":    createdAt,
				"size":          firstNonEmpty(gjson.GetBytes(requestBody, "size").String(), "1024x1024"),
				"quality":       firstNonEmpty(gjson.GetBytes(requestBody, "quality").String(), "auto"),
				"background":    firstNonEmpty(gjson.GetBytes(requestBody, "background").String(), "auto"),
				"output_format": firstNonEmpty(image.OutputFormat, gjson.GetBytes(requestBody, "output_format").String(), "png"),
			}
			if image.Result != "" {
				payload["b64_json"] = image.Result
			}
			if image.URL != "" {
				payload["url"] = image.URL
			}
			if image.RevisedPrompt != "" {
				payload["revised_prompt"] = image.RevisedPrompt
			}
			if idx == len(images)-1 {
				var eventImageUsage *OpenAIUsage
				if usageState.ImageUsage != nil {
					copy := *usageState.ImageUsage
					eventImageUsage = &copy
					if priorImageUsage != nil {
						addOpenAIImagesOAuthUsage(eventImageUsage, *priorImageUsage)
					} else if imageIndex > 0 {
						eventImageUsage.ImageUsageComplete = false
					}
				} else if priorImageUsage != nil {
					copy := *priorImageUsage
					copy.ImageUsageComplete = false
					eventImageUsage = &copy
				}
				if eventImageUsage != nil {
					payload["usage"] = openAIImagesOAuthClientImageUsage(eventImageUsage)
				} else if !strings.HasPrefix(model, "gpt-image-2.6") {
					eventUsage := usage
					addOpenAIImagesOAuthUsage(&eventUsage, priorUsage)
					if hasOpenAIImagesOAuthUsage(eventUsage) {
						outputTokens := eventUsage.ImageOutputTokens
						if outputTokens <= 0 {
							outputTokens = eventUsage.OutputTokens
						}
						payload["usage"] = map[string]any{"input_tokens": eventUsage.InputTokens, "output_tokens": outputTokens, "total_tokens": eventUsage.InputTokens + outputTokens}
					}
				}
				if len(usageState.SVGUsage) == 1 {
					payload["svg_usage"] = usageState.SVGUsage[0]
				}
			}
			if err := emitPayload(payload); err != nil {
				return err
			}
			imageCount++
		}
		completedEmitted = true
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer putSSEScannerBuf64K(scanBuf)

	for scanner.Scan() {
		data, ok := extractOpenAIStreamPayloadLine(scanner.Text())
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if streamErr == nil {
				_ = emitCompletedImages()
				if imageCount == 0 {
					emitFailure("openai images oauth response did not contain image results")
				}
			}
			sawTerminal = true
			continue
		}
		eventType := strings.TrimSpace(gjson.Get(data, "type").String())
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		switch eventType {
		case "response.image_generation_call.partial_image":
			payload := map[string]any{
				"type":                eventPrefix + ".partial_image",
				"image_index":         imageIndex,
				"b64_json":            firstNonEmpty(gjson.Get(data, "partial_image_b64").String(), gjson.Get(data, "b64_json").String()),
				"created_at":          time.Now().Unix(),
				"partial_image_index": gjson.Get(data, "partial_image_index").Int(),
				"size":                firstNonEmpty(gjson.Get(data, "size").String(), gjson.GetBytes(requestBody, "size").String(), "1024x1024"),
				"quality":             firstNonEmpty(gjson.Get(data, "quality").String(), gjson.GetBytes(requestBody, "quality").String(), "auto"),
				"background":          firstNonEmpty(gjson.Get(data, "background").String(), gjson.GetBytes(requestBody, "background").String(), "auto"),
				"output_format":       firstNonEmpty(gjson.Get(data, "output_format").String(), gjson.GetBytes(requestBody, "output_format").String(), "png"),
			}
			if payload["b64_json"] != "" {
				if err := emitPayload(payload); err != nil {
					return nil, err
				}
			}
		case "response.output_item.done":
			appendFinalItem(gjson.Get(data, "item"))
		case "response.completed", "response.done":
			sawTerminal = true
			response := gjson.Get(data, "response")
			if response.Exists() {
				usageState.consumeResponse(response)
				if openAIImagesOAuthResponseFailed(response) {
					emitUpstreamFailure(response, "openai images oauth response did not complete")
					continue
				}
				if v := response.Get("created_at").Int(); v > 0 {
					createdAt = v
				}
				if parsedUsage, ok := extractOpenAIUsageFromJSONBytes([]byte(response.Raw)); ok {
					usage = parsedUsage
				}
				for _, item := range response.Get("output").Array() {
					appendFinalItem(item)
				}
			}
			if streamErr == nil {
				if err := emitCompletedImages(); err != nil {
					return nil, err
				}
				if imageCount == 0 {
					emitFailure("openai images oauth response did not contain image results")
				}
			}
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
			sawTerminal = true
			usageState.consumeResponse(gjson.Get(data, "response"))
			if parsedUsage, ok := extractOpenAIUsageFromJSONBytes([]byte(gjson.Get(data, "response").Raw)); ok {
				usage = parsedUsage
			}
			emitUpstreamFailure(gjson.Parse(data), "openai images oauth stream ended with "+eventType)
		}
	}
	if err := scanner.Err(); err != nil && !sawTerminal {
		emitFailure("openai images oauth stream read error: " + err.Error())
	}
	if !sawTerminal {
		emitFailure("openai images oauth stream missing terminal event")
	}
	result := buildOpenAIImagesOAuthStreamingResult(resp, requestBody, model, usage, imageCount, firstTokenMs, startTime)
	usageState.apply(result, requestBody)
	return result, streamErr
}

func buildOpenAIImagesOAuthStreamingResult(resp *http.Response, requestBody []byte, model string, usage OpenAIUsage, imageCount int, firstTokenMs *int, startTime time.Time) *OpenAIForwardResult {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "gpt-image-2"
	}
	return &OpenAIForwardResult{
		RequestID:        strings.TrimSpace(resp.Header.Get("x-request-id")),
		Usage:            usage,
		Model:            model,
		UpstreamModel:    model,
		Stream:           true,
		ResponseHeaders:  resp.Header.Clone(),
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ImageCount:       imageCount,
		ImageSize:        normalizeOpenAIImageSize(gjson.GetBytes(requestBody, "size").String()),
		ImageBillingTier: billingImageTier(requestBody),
	}
}

func openAIImagesOAuthSessionSeed(body []byte) string {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	prompt := strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
	size := strings.TrimSpace(gjson.GetBytes(body, "size").String())
	return strings.TrimSpace("openai-images|" + model + "|" + size + "|" + prompt)
}

func buildOpenAIImagesOAuthClientResponse(responsesBody, requestBody []byte) ([]byte, OpenAIUsage, int, error) {
	body, usage, count, _, err := buildOpenAIImagesOAuthClientResponseWithUsage(responsesBody, requestBody)
	return body, usage, count, err
}

func buildOpenAIImagesOAuthClientResponseWithUsage(responsesBody, requestBody []byte) ([]byte, OpenAIUsage, int, openAIImagesOAuthUsageState, error) {
	images, createdAt, usage, usageState, ok := collectOpenAIImagesOAuthResults(responsesBody)
	if !ok || len(images) == 0 {
		if upstreamErr := extractOpenAIImageUpstreamError(responsesBody); upstreamErr != nil {
			return nil, usage, 0, usageState, upstreamErr
		}
		return nil, usage, 0, usageState, errors.New("openai images oauth response did not contain image results")
	}
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	responseFormat := strings.ToLower(strings.TrimSpace(gjson.GetBytes(requestBody, "response_format").String()))
	data := make([]map[string]any, 0, len(images))
	for _, image := range images {
		item := map[string]any{}
		if image.RevisedPrompt != "" {
			item["revised_prompt"] = image.RevisedPrompt
		}
		if image.URL != "" {
			item["url"] = image.URL
		}
		if image.Result != "" {
			if responseFormat == "url" {
				item["url"] = "data:" + openAIImageOAuthMIMEType(image.OutputFormat) + ";base64," + image.Result
			} else {
				item["b64_json"] = image.Result
			}
		}
		data = append(data, item)
	}
	payload := map[string]any{
		"created": createdAt,
		"data":    data,
	}
	if usageState.ImageUsage != nil {
		payload["usage"] = openAIImagesOAuthClientImageUsage(usageState.ImageUsage)
	} else if !strings.HasPrefix(gjson.GetBytes(requestBody, "model").String(), "gpt-image-2.6") && hasOpenAIImagesOAuthUsage(usage) {
		payload["usage"] = usage
	}
	if len(usageState.SVGUsage) == 1 {
		payload["svg_usage"] = usageState.SVGUsage[0]
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, usage, 0, usageState, err
	}
	return out, usage, len(data), usageState, nil
}

// A terminal event name alone must not turn an incomplete or failed response
// (or a failed image tool item) into a successful Images API response.
func openAIImagesOAuthResponseFailed(response gjson.Result) bool {
	status := strings.TrimSpace(response.Get("status").String())
	if status != "" && status != "completed" {
		return true
	}
	err := response.Get("error")
	return err.Exists() && err.Type != gjson.Null
}

func collectOpenAIImagesOAuthResults(body []byte) ([]openAIImageOAuthResult, int64, OpenAIUsage, openAIImagesOAuthUsageState, bool) {
	var (
		results    []openAIImageOAuthResult
		createdAt  int64
		usage      OpenAIUsage
		usageState openAIImagesOAuthUsageState
		seen       = map[string]struct{}{}
		terminal   bool
		failed     bool
	)
	appendResult := func(item gjson.Result) {
		usageState.consumeSVG(item)
		if !item.Exists() || item.Get("type").String() != "image_generation_call" {
			return
		}
		if openAIImagesOAuthResponseFailed(item) {
			failed = true
			return
		}
		result := openAIImageOAuthResult{
			Result:        strings.TrimSpace(item.Get("result").String()),
			URL:           strings.TrimSpace(item.Get("url").String()),
			RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
			OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
		}
		if result.Result == "" && result.URL == "" {
			return
		}
		key := result.OutputFormat + "|" + result.Result + "|" + result.URL
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		results = append(results, result)
	}
	consumeResponse := func(response gjson.Result) {
		usageState.consumeResponse(response)
		if openAIImagesOAuthResponseFailed(response) {
			failed = true
		}
		if v := response.Get("created_at").Int(); v > 0 {
			createdAt = v
		}
		if parsedUsage, ok := extractOpenAIUsageFromJSONBytes([]byte(response.Raw)); ok {
			usage = parsedUsage
		}
		for _, item := range response.Get("output").Array() {
			appendResult(item)
		}
	}
	if gjson.ValidBytes(body) {
		response := gjson.ParseBytes(body)
		if response.IsObject() && (response.Get("object").String() == "response" || response.Get("output").IsArray()) {
			consumeResponse(response)
			return results, createdAt, usage, usageState, !failed && len(results) > 0
		}
	}
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := extractOpenAIStreamPayloadLine(line)
		if !ok || data == "" {
			continue
		}
		if strings.TrimSpace(data) == "[DONE]" {
			terminal = true
			continue
		}
		eventType := strings.TrimSpace(gjson.Get(data, "type").String())
		switch eventType {
		case "response.output_item.done":
			appendResult(gjson.Get(data, "item"))
		case "response.done", "response.completed":
			terminal = true
			if response := gjson.Get(data, "response"); response.Exists() {
				consumeResponse(response)
			}
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
			usageState.consumeResponse(gjson.Get(data, "response"))
			failed = true
		}
	}
	return results, createdAt, usage, usageState, terminal && !failed && len(results) > 0
}

func openAIImageOAuthMIMEType(outputFormat string) string {
	format := strings.ToLower(strings.TrimSpace(outputFormat))
	if format == "" {
		return "image/png"
	}
	if strings.Contains(format, "/") {
		return format
	}
	switch format {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}
