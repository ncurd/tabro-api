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

func (s *OpenAIGatewayService) forwardImagesGenerationsOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal bool) ([]byte, *OpenAIForwardResult, error) {
	return s.forwardImagesOAuth(ctx, c, account, body, writeOriginal, false)
}

func (s *OpenAIGatewayService) forwardImagesOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal, edit bool) ([]byte, *OpenAIForwardResult, error) {
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

	upstreamReq, err := s.buildUpstreamRequest(ctx, c, account, responsesBody, token, true, openAIImagesOAuthSessionSeed(body), false)
	if err != nil {
		return nil, nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")

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
		result, streamErr := s.handleImagesOAuthStream(resp, c, body, model, startTime, writeOriginal, eventPrefix)
		return nil, result, streamErr
	}

	respBody, readErr := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if readErr != nil {
		return nil, nil, readErr
	}
	if resp.StatusCode >= 400 {
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			return respBody, nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBody, ResponseHeaders: resp.Header}
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		if c != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
			c.Data(resp.StatusCode, contentType, respBody)
		}
		return respBody, nil, fmt.Errorf("openai images oauth upstream returned status %d", resp.StatusCode)
	}

	imagesBody, usage, imageCount, err := buildOpenAIImagesOAuthClientResponse(respBody, body)
	if err != nil {
		return respBody, nil, err
	}
	if writeOriginal && c != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		c.Data(http.StatusOK, "application/json", imagesBody)
	}

	result := &OpenAIForwardResult{
		RequestID:       strings.TrimSpace(resp.Header.Get("x-request-id")),
		Usage:           usage,
		Model:           model,
		UpstreamModel:   model,
		Stream:          false,
		ResponseHeaders: resp.Header.Clone(),
		Duration:        time.Since(startTime),
		ImageCount:      imageCount,
		ImageSize:       normalizeOpenAIImageSize(gjson.GetBytes(body, "size").String()),
	}
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

	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	req, _ = sjson.SetBytes(req, "model", openAIImagesResponsesMainModel)
	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	req, _ = sjson.SetRawBytes(req, "input", input)

	tool := []byte(`{"type":"image_generation","action":"generate","model":""}`)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)
	if n := gjson.GetBytes(imagesBody, "n").Int(); n > 0 {
		tool, _ = sjson.SetBytes(tool, "n", n)
	}
	if gjson.GetBytes(imagesBody, "stream").Bool() {
		if partialImages := gjson.GetBytes(imagesBody, "partial_images"); partialImages.Exists() && partialImages.Int() >= 0 {
			tool, _ = sjson.SetBytes(tool, "partial_images", partialImages.Int())
		}
	}
	for _, key := range []string{"size", "quality", "background", "output_format", "moderation", "style", "input_fidelity"} {
		if value := strings.TrimSpace(gjson.GetBytes(imagesBody, key).String()); value != "" {
			tool, _ = sjson.SetBytes(tool, key, value)
		}
	}
	if compression := gjson.GetBytes(imagesBody, "output_compression"); compression.Exists() {
		tool, _ = sjson.SetBytes(tool, "output_compression", compression.Int())
	}
	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	req, _ = sjson.SetRawBytes(req, "tools.-1", tool)
	return req, nil
}

func (s *OpenAIGatewayService) handleImagesOAuthStream(resp *http.Response, c *gin.Context, requestBody []byte, model string, startTime time.Time, writeOriginal bool, eventPrefix string) (*OpenAIForwardResult, error) {
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
	emitDone := func() {
		if clientDisconnected {
			return
		}
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
			return
		}
		flusher.Flush()
	}
	emitFailure := func(message string) {
		if streamErr != nil {
			return
		}
		message = sanitizeUpstreamErrorMessage(message)
		streamErr = errors.New(message)
		_ = emitPayload(map[string]any{"type": "error", "error": map[string]any{
			"type": "upstream_error", "message": message,
		}})
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
		if !item.Exists() || item.Get("type").String() != "image_generation_call" {
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
		for idx, image := range finalImages {
			payload := map[string]any{
				"type":          eventPrefix + ".completed",
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
			if idx == len(finalImages)-1 && (usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageOutputTokens > 0 || usage.CacheReadInputTokens > 0) {
				outputTokens := usage.ImageOutputTokens
				if outputTokens <= 0 {
					outputTokens = usage.OutputTokens
				}
				payload["usage"] = map[string]any{
					"input_tokens":  usage.InputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  usage.InputTokens + outputTokens,
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
				} else {
					emitDone()
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
			if parsedUsage, ok := extractOpenAIUsageFromJSONBytes([]byte(gjson.Get(data, "response").Raw)); ok {
				usage = parsedUsage
			}
			emitFailure(firstNonEmpty(gjson.Get(data, "response.error.message").String(), gjson.Get(data, "error.message").String(), gjson.Get(data, "message").String(), "openai images oauth stream ended with "+eventType))
		}
	}
	if err := scanner.Err(); err != nil && !sawTerminal {
		emitFailure("openai images oauth stream read error: " + err.Error())
	}
	if !sawTerminal {
		emitFailure("openai images oauth stream missing terminal event")
	}
	return buildOpenAIImagesOAuthStreamingResult(resp, requestBody, model, usage, imageCount, firstTokenMs, startTime), streamErr
}

func buildOpenAIImagesOAuthStreamingResult(resp *http.Response, requestBody []byte, model string, usage OpenAIUsage, imageCount int, firstTokenMs *int, startTime time.Time) *OpenAIForwardResult {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "gpt-image-2"
	}
	return &OpenAIForwardResult{
		RequestID:       strings.TrimSpace(resp.Header.Get("x-request-id")),
		Usage:           usage,
		Model:           model,
		UpstreamModel:   model,
		Stream:          true,
		ResponseHeaders: resp.Header.Clone(),
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
		ImageCount:      imageCount,
		ImageSize:       normalizeOpenAIImageSize(gjson.GetBytes(requestBody, "size").String()),
	}
}

func openAIImagesOAuthSessionSeed(body []byte) string {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	prompt := strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
	size := strings.TrimSpace(gjson.GetBytes(body, "size").String())
	return strings.TrimSpace("openai-images|" + model + "|" + size + "|" + prompt)
}

func buildOpenAIImagesOAuthClientResponse(responsesBody, requestBody []byte) ([]byte, OpenAIUsage, int, error) {
	images, createdAt, usage, ok := collectOpenAIImagesOAuthResults(responsesBody)
	if !ok || len(images) == 0 {
		return nil, usage, 0, errors.New("openai images oauth response did not contain image results")
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
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageOutputTokens > 0 || usage.CacheReadInputTokens > 0 {
		payload["usage"] = usage
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, usage, 0, err
	}
	return out, usage, len(data), nil
}

func collectOpenAIImagesOAuthResults(body []byte) ([]openAIImageOAuthResult, int64, OpenAIUsage, bool) {
	var (
		results   []openAIImageOAuthResult
		createdAt int64
		usage     OpenAIUsage
		seen      = map[string]struct{}{}
		terminal  bool
		failed    bool
	)
	appendResult := func(item gjson.Result) {
		if !item.Exists() || item.Get("type").String() != "image_generation_call" {
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
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
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
			response := gjson.Get(data, "response")
			if response.Exists() {
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
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
			failed = true
		}
	}
	return results, createdAt, usage, terminal && !failed && len(results) > 0
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
	default:
		return "image/png"
	}
}
