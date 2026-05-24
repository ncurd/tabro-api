package service

import (
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

const openAIImagesResponsesMainModel = "gpt-5.4-mini"

type openAIImageOAuthResult struct {
	Result        string
	URL           string
	RevisedPrompt string
	OutputFormat  string
}

func (s *OpenAIGatewayService) forwardImagesGenerationsOAuth(ctx context.Context, c *gin.Context, account *Account, body []byte, writeOriginal bool) ([]byte, *OpenAIForwardResult, error) {
	startTime := time.Now()
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		model = "gpt-image-2"
	}
	responsesBody, err := buildOpenAIImagesOAuthResponsesRequest(body, model)
	if err != nil {
		return nil, nil, err
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
	for _, key := range []string{"size", "quality", "background", "output_format", "moderation", "style"} {
		if value := strings.TrimSpace(gjson.GetBytes(imagesBody, key).String()); value != "" {
			tool, _ = sjson.SetBytes(tool, key, value)
		}
	}
	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	req, _ = sjson.SetRawBytes(req, "tools.-1", tool)
	return req, nil
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
		if !ok || data == "" || data == "[DONE]" {
			continue
		}
		eventType := strings.TrimSpace(gjson.Get(data, "type").String())
		switch eventType {
		case "response.output_item.done":
			appendResult(gjson.Get(data, "item"))
		case "response.done", "response.completed":
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
		}
	}
	return results, createdAt, usage, len(results) > 0
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
