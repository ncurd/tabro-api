package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func billingGeminiModel(path string) (string, bool) {
	index := strings.Index(path, "/models/")
	if index < 0 {
		return "", false
	}
	tail := path[index+len("/models/"):]
	for _, action := range []string{":generateContent", ":streamGenerateContent"} {
		if strings.HasSuffix(tail, action) {
			model := strings.TrimSuffix(tail, action)
			return model, model != ""
		}
	}
	return "", false
}
func (s *GatewayBillingCoordinator) BoundGeminiRequest(path string, body []byte) ([]byte, error) {
	model, ok := billingGeminiModel(path)
	if !ok {
		return body, nil
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, bc.ErrConflict
	}
	caps, known := s.pricing.GetTextModelCapabilities(model)
	if !known || caps.MaxInputTokens <= 0 || caps.MaxOutputTokens <= 0 {
		return nil, fmt.Errorf("Gemini model requires catalogued input and output limits")
	}
	value := gjson.GetBytes(body, "generationConfig.maxOutputTokens")
	if value.Exists() {
		var n int64
		if json.Unmarshal([]byte(value.Raw), &n) != nil || n <= 0 || n > caps.MaxOutputTokens {
			return nil, fmt.Errorf("invalid Gemini output limit")
		}
		return body, nil
	}
	return sjson.SetBytes(body, "generationConfig.maxOutputTokens", caps.MaxOutputTokens)
}
func geminiBillingQuoteRequest(path, operationID string, body []byte, pricing *PricingService) (bc.QuoteRequest, bool, error) {
	q := bc.QuoteRequest{OperationID: operationID, ServiceTier: "default", RequestPayloadHash: HashUsageRequestPayload(append([]byte(path+"\x00"), body...))}
	model, ok := billingGeminiModel(path)
	if !ok {
		return q, false, nil
	}
	if !gjson.ValidBytes(body) {
		return q, true, bc.ErrConflict
	}
	caps, known := pricing.GetTextModelCapabilities(model)
	if !known || caps.MaxInputTokens <= 0 || caps.MaxOutputTokens <= 0 {
		return q, true, fmt.Errorf("Gemini model capability is unavailable")
	}
	count := int64(1)
	if v := gjson.GetBytes(body, "generationConfig.candidateCount"); v.Exists() {
		if json.Unmarshal([]byte(v.Raw), &count) != nil || count < 1 || count > 8 {
			return q, true, fmt.Errorf("invalid Gemini candidate count")
		}
	}
	// Only client-executed functions are covered by token metering. Inspect
	// each key instead of a GJSON projection: an empty projection still exists,
	// and hosted tools have both camelCase and snake_case spellings.
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() {
		if !tools.IsArray() {
			return q, true, fmt.Errorf("invalid Gemini tools")
		}
		for _, tool := range tools.Array() {
			if !tool.IsObject() {
				return q, true, fmt.Errorf("invalid Gemini tool")
			}
			unsupported := false
			tool.ForEach(func(key, value gjson.Result) bool {
				unsupported = key.String() != "functionDeclarations" && key.String() != "function_declarations"
				return !unsupported
			})
			if unsupported {
				return q, true, fmt.Errorf("hosted Gemini tools require separately metered product support")
			}
		}
	}
	input := bc.Decimal(strconv.FormatInt(caps.MaxInputTokens, 10))
	output := bc.Decimal(strconv.FormatInt(caps.MaxOutputTokens*count, 10))
	q.ProductKey = "ai:" + model
	q.MaximumUsage = map[string]bc.Decimal{"input_tokens": input, "cache_read_tokens": input, "cache_write_tokens": input, "cache_write_5m_tokens": input, "cache_write_1h_tokens": input, "output_tokens": output, "image_output_tokens": output, "request_count": "1", "image_count": "0"}
	for _, v := range gjson.GetBytes(body, "generationConfig.responseModalities").Array() {
		if strings.EqualFold(v.String(), "IMAGE") {
			q.MaximumUsage["image_count"] = output
		}
	}
	return q, true, nil
}
