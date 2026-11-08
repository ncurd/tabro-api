package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// OpenAIImageUpstreamError contains only the public error fields from an image
// upstream. It deliberately excludes the surrounding response and image data.
type OpenAIImageUpstreamError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

var (
	openAIImageErrorBearer   = regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]+`)
	openAIImageErrorAPIKey   = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	openAIImageErrorDataURL  = regexp.MustCompile(`(?i)data:image/[A-Za-z0-9.+-]+;base64,[A-Za-z0-9+/=_-]+`)
	openAIImageErrorQueryKey = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|token)=)[^&"\s]+`)
)

func sanitizeOpenAIImageErrorField(value string) string {
	value = sanitizeUpstreamErrorMessage(value)
	value = openAIImageErrorQueryKey.ReplaceAllString(value, `$1***`)
	value = openAIImageErrorBearer.ReplaceAllString(value, "Bearer ***")
	value = openAIImageErrorAPIKey.ReplaceAllString(value, "sk-***")
	return openAIImageErrorDataURL.ReplaceAllString(value, "[image data omitted]")
}

func (e *OpenAIImageUpstreamError) Error() string { return e.Message }

func parseOpenAIImageUpstreamError(payload gjson.Result) *OpenAIImageUpstreamError {
	for _, path := range []string{"response.error", "error", "item.error"} {
		if value := payload.Get(path); value.IsObject() {
			if upstreamErr := openAIImageUpstreamErrorFields(value, false); upstreamErr != nil {
				return upstreamErr
			}
		}
	}
	if payload.Get("type").String() == "error" {
		return openAIImageUpstreamErrorFields(payload, true)
	}
	response := payload
	if value := payload.Get("response"); value.IsObject() {
		response = value
	}
	for _, item := range response.Get("output").Array() {
		if item.Get("type").String() == "image_generation_call" {
			if upstreamErr := openAIImageUpstreamErrorFields(item.Get("error"), false); upstreamErr != nil {
				return upstreamErr
			}
		}
	}
	return nil
}

func openAIImageUpstreamErrorFields(value gjson.Result, event bool) *OpenAIImageUpstreamError {
	field := func(key string, limit int) string {
		result := value.Get(key)
		if result.Type != gjson.String {
			return ""
		}
		text := []rune(strings.TrimSpace(sanitizeOpenAIImageErrorField(result.String())))
		if len(text) > limit {
			text = text[:limit]
		}
		return string(text)
	}
	upstreamErr := &OpenAIImageUpstreamError{Message: field("message", 2048), Type: field("type", 256), Code: field("code", 256), Param: field("param", 256)}
	if event {
		upstreamErr.Type = ""
	}
	if upstreamErr.Message == "" && upstreamErr.Type == "" && upstreamErr.Code == "" && upstreamErr.Param == "" {
		return nil
	}
	if upstreamErr.Message == "" {
		upstreamErr.Message = "OpenAI image request failed"
	}
	if upstreamErr.Type == "" {
		upstreamErr.Type = "upstream_error"
	}
	return upstreamErr
}

func extractOpenAIImageUpstreamError(body []byte) *OpenAIImageUpstreamError {
	if gjson.ValidBytes(body) {
		return parseOpenAIImageUpstreamError(gjson.ParseBytes(body))
	}
	for _, line := range strings.Split(string(body), "\n") {
		if data, ok := extractOpenAIStreamPayloadLine(line); ok && gjson.Valid(data) {
			if upstreamErr := parseOpenAIImageUpstreamError(gjson.Parse(data)); upstreamErr != nil {
				return upstreamErr
			}
		}
	}
	return nil
}

func recordOpenAIImageUpstreamError(c *gin.Context, status int, err error) {
	detailFields := map[string]any{}
	if c != nil {
		if value, ok := c.Get(OpsOpenAIImagesRequestSummaryKey); ok {
			if summary, ok := value.(openAIImagesOAuthRequestSummary); ok {
				detailFields["request_summary"] = summary
			}
		}
	}
	message := sanitizeUpstreamErrorMessage(err.Error())
	var upstreamErr *OpenAIImageUpstreamError
	if errors.As(err, &upstreamErr) {
		detailFields["error"] = upstreamErr
		message = upstreamErr.Message
	}
	var detail []byte
	if len(detailFields) > 0 {
		detail, _ = json.Marshal(detailFields)
	}
	setOpsUpstreamError(c, status, message, string(detail))
}
