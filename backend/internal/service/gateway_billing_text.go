package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

func textBillingQuoteRequest(path, operationID string, body []byte, prices ...*PricingService) (bc.QuoteRequest, error) {
	q := bc.QuoteRequest{OperationID: operationID, ServiceTier: "default", RequestPayloadHash: HashUsageRequestPayload(append([]byte(path+"\x00"), body...))}
	path = billingCanonicalTextPath(path)
	switch path {
	case "/v1/messages", "/v1/responses", "/responses", "/v1/chat/completions", "/chat/completions", "/ws/responses":
	default:
		return q, fmt.Errorf("central billing does not yet support this endpoint")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return q, errors.New("invalid model request")
	}
	var model string
	if json.Unmarshal(payload["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return q, errors.New("model is required")
	}
	// Non-text inputs and server-hosted tools need their own bounded meters.
	var raw any
	_ = json.Unmarshal(body, &raw)
	var textOnly func(any) bool
	textOnly = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if typ, ok := x["type"].(string); ok {
				if typ == "mcp" || typ == "file_search" || strings.HasPrefix(typ, "web_search") || strings.HasPrefix(typ, "code_execution") || strings.HasPrefix(typ, "tool_search") {
					return false
				}
				switch typ {
				case "input_audio", "audio", "file", "input_file", "video", "web_search", "web_search_preview", "computer_use_preview", "image_generation", "code_interpreter":
					return false
				}
			}
			for _, child := range x {
				if !textOnly(child) {
					return false
				}
			}
		case []any:
			for _, child := range x {
				if !textOnly(child) {
					return false
				}
			}
		}
		return true
	}
	if !textOnly(raw) || (len(payload["previous_response_id"]) > 0 && path != "/ws/responses") {
		return q, errors.New("central billing requires a fully bounded text request")
	}
	var pricing *PricingService
	if len(prices) > 0 {
		pricing = prices[0]
	}
	_, maxOutput, err := boundTextRequest(path, body, pricing)
	if err != nil {
		return q, err
	}
	if background, ok := payload["background"]; ok && string(background) != "false" {
		return q, errors.New("central billing requires synchronous execution")
	}
	if v, ok := payload["n"]; ok {
		var n int
		if json.Unmarshal(v, &n) != nil || n != 1 {
			return q, errors.New("central billing requires one completion")
		}
	}
	if v, ok := payload["service_tier"]; ok {
		var tier string
		if json.Unmarshal(v, &tier) != nil {
			return q, errors.New("invalid service tier")
		}
		if tier != "" && tier != "auto" {
			q.ServiceTier = tier
		}
	}
	if v, ok := payload["speed"]; ok {
		var speed string
		_ = json.Unmarshal(v, &speed)
		if speed == "fast" {
			q.ServiceTier = "anthropic_fast"
		}
	}
	q.ProductKey = "ai:" + model
	// Account transforms may add hidden instructions after this middleware.
	// The model's full input cap also bounds vision, references and WS history.
	capability, _ := pricing.GetTextModelCapabilities(model)
	if capability.MaxInputTokens <= 0 {
		return q, errors.New("central billing requires a catalogued input limit")
	}
	input := bc.Decimal(strconv.FormatInt(capability.MaxInputTokens, 10))
	output := bc.Decimal(strconv.FormatInt(maxOutput, 10))
	q.MaximumUsage = map[string]bc.Decimal{"input_tokens": input, "output_tokens": output, "cache_read_tokens": input, "cache_write_tokens": input, "cache_write_5m_tokens": input, "cache_write_1h_tokens": input, "image_output_tokens": "0", "request_count": "1", "image_count": "0"}
	return q, nil
}

// TextModelCapabilities comes from an exact catalog entry or an explicit
// compatibility alias. Fuzzy price matching must never promise an upper usage
// limit for an unknown model.
type TextModelCapabilities struct {
	MaxInputTokens   int64
	MaxOutputTokens  int64
	MayUseCodexOAuth bool
}

func (s *PricingService) GetTextModelCapabilities(model string) (TextModelCapabilities, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	// Reasoning aliases are accepted only when the compatibility adapter has an
	// explicit mapping to this exact model. Unknown suffixes remain unbounded.
	if normalized, _, ok := splitOpenAICompatReasoningModel(model); ok && normalized == "gpt-6.1-sol" {
		model = normalized
	}
	var pricing *LiteLLMModelPricing
	if s != nil {
		s.mu.RLock()
		if entry := s.pricingData[model]; entry != nil {
			copy := *entry
			pricing = &copy
		}
		s.mu.RUnlock()
	}
	// Keep newly released models usable if the remote catalog has not caught up.
	var fallback *LiteLLMModelPricing
	switch model {
	case "gpt-6-astra":
		fallback = openAIGPT6AstraFallbackPricing
	case "gpt-6.1-sol":
		fallback = openAIGPT61SolFallbackPricing
	case "gpt-6-sol":
		fallback = openAIGPT6SolFallbackPricing
	case "gpt-6-luna":
		fallback = openAIGPT6LunaFallbackPricing
	case "gpt-5.6", "gpt-5.6-sol":
		fallback = openAIGPT56SolFallbackPricing
	case "gpt-5.6-terra":
		fallback = openAIGPT56TerraFallbackPricing
	case "gpt-5.6-luna":
		fallback = openAIGPT56LunaFallbackPricing
	case "claude-opus-5-5":
		fallback = anthropicOpus55FallbackPricing
	case "claude-opus-5":
		fallback = anthropicOpus5FallbackPricing
	}
	if pricing == nil || pricing.MaxOutputTokens <= 0 {
		pricing = fallback
	}
	if pricing == nil || pricing.MaxOutputTokens <= 0 || pricing.MaxOutputTokens > 1000000 {
		return TextModelCapabilities{}, false
	}
	input := pricing.MaxInputTokens
	if input <= 0 && fallback != nil {
		input = fallback.MaxInputTokens
	}
	return TextModelCapabilities{MaxInputTokens: int64(input), MaxOutputTokens: int64(pricing.MaxOutputTokens), MayUseCodexOAuth: strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") || pricing.LiteLLMProvider == "openai"}, true
}

func billingCanonicalTextPath(path string) string {
	if path == "/antigravity/v1/messages" {
		return "/v1/messages"
	}
	return path
}

func isBillingTextPath(path string) bool {
	switch billingCanonicalTextPath(path) {
	case "/v1/messages", "/v1/responses", "/responses", "/v1/chat/completions", "/chat/completions", "/ws/responses":
		return true
	default:
		return false
	}
}

// BoundRequest supplies an executable output limit to ordinary API upstreams.
// Only central mode calls this method; shadow requests retain the original body.
func (s *GatewayBillingCoordinator) BoundRequest(path string, body []byte) ([]byte, error) {
	if !isBillingTextPath(path) {
		return body, nil
	}
	bounded, _, err := boundTextRequest(path, body, s.pricing)
	return bounded, err
}

func boundTextRequest(path string, body []byte, pricing *PricingService) ([]byte, int64, error) {
	path = billingCanonicalTextPath(path)
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, 0, errors.New("invalid model request")
	}
	var model string
	if json.Unmarshal(payload["model"], &model) != nil || model == "" {
		return nil, 0, errors.New("model is required")
	}
	efforts := []string{""}
	for _, name := range []string{"reasoning_effort", "reasoning", "output_config"} {
		raw := payload[name]
		if name == "reasoning_effort" {
			var effort string
			if json.Unmarshal(raw, &effort) == nil && effort != "" {
				efforts = append(efforts, effort)
			}
		} else {
			var options struct {
				Effort string `json:"effort"`
			}
			if json.Unmarshal(raw, &options) == nil && options.Effort != "" {
				efforts = append(efforts, options.Effort)
			}
		}
	}
	for _, effort := range efforts {
		if err := validateGPT61SolReasoning(model, normalizeCodexModel(model), effort); err != nil {
			return nil, 0, err
		}
	}
	capability, known := pricing.GetTextModelCapabilities(model)
	if !known {
		return nil, 0, errors.New("central billing requires a catalogued model output limit")
	}
	maxOutput := int64(0)
	for _, name := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if raw, found := payload[name]; found {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n <= 0 || n > capability.MaxOutputTokens {
				return nil, 0, errors.New("output token bound exceeds model capability")
			}
			// Conflicting aliases must not allow a later transform to select a
			// larger limit than the one the caller expected.
			if maxOutput != 0 && maxOutput != n {
				return nil, 0, errors.New("conflicting output token bounds")
			}
			maxOutput = n
		}
	}
	if maxOutput == 0 {
		maxOutput = capability.MaxOutputTokens
	}
	for _, name := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		delete(payload, name)
	}
	field := "max_output_tokens"
	if path == "/v1/messages" {
		field = "max_tokens"
	} else if strings.HasSuffix(path, "/chat/completions") {
		field = "max_completion_tokens"
	}
	payload[field] = json.RawMessage(strconv.FormatInt(maxOutput, 10))
	if capability.MayUseCodexOAuth && path != "/v1/messages" {
		var tier string
		if raw, exists := payload["service_tier"]; !exists || (json.Unmarshal(raw, &tier) == nil && (tier == "" || tier == "auto")) {
			payload["service_tier"] = json.RawMessage(`"default"`)
		}
	}
	bounded, err := json.Marshal(payload)
	reservedOutput := maxOutput
	// Account routing happens after reserve. Codex OAuth rejects all output
	// limit parameters, so any request that may reach Codex reserves the hard
	// model cap even if its API-key branch can enforce the smaller user limit.
	if capability.MayUseCodexOAuth || path == "/ws/responses" {
		reservedOutput = capability.MaxOutputTokens
	}
	return bounded, reservedOutput, err
}

// Validate the final transformed body before either Responses transport. This
// catches account model mappings whose OAuth output cap exceeds the reservation
// and API-key transforms that accidentally discarded the executable limit.
func (s *OpenAIGatewayService) validateCentralOutputBound(ctx context.Context, account *Account, body []byte) error {
	e := bc.ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	bounds, boundsErr := gatewayTextBounds(e)
	if boundsErr != nil {
		return boundsErr
	}
	rawReserved, text := bounds["output_tokens"]
	if !text {
		return nil
	}
	reserved, err := strconv.ParseInt(string(rawReserved), 10, 64)
	if err != nil || reserved <= 0 {
		return errors.New("central output reservation is missing")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return errors.New("invalid final supplier request")
	}
	var model string
	_ = json.Unmarshal(payload["model"], &model)
	var pricing *PricingService
	if s.billingService != nil {
		pricing = s.billingService.pricingService
	}
	{

		capability, known := pricing.GetTextModelCapabilities(model)
		input, parseErr := strconv.ParseInt(string(bounds["input_tokens"]), 10, 64)
		if !known || capability.MaxInputTokens <= 0 || parseErr != nil || capability.MaxInputTokens > input {
			return errors.New("mapped model input exceeds reserved capability")
		}
	}
	if account.Type == AccountTypeOAuth {
		capability, known := pricing.GetTextModelCapabilities(model)
		if !known || capability.MaxOutputTokens > reserved {
			return errors.New("mapped OAuth model output exceeds reserved capability")
		}
		return nil
	}
	var limit int64
	if json.Unmarshal(payload["max_output_tokens"], &limit) != nil || limit <= 0 || limit > reserved {
		return errors.New("supplier output limit exceeds central reservation")
	}
	return nil
}

// ValidateBillingModelCaps is used after a WebSocket turn resolves its actual
// upstream model, before dispatching the separately reserved turn.
func (s *GatewayBillingCoordinator) ValidateBillingModelCaps(e *bc.Execution, upstreamModel string) error {
	if e == nil || e.Mode != "central" {
		return nil
	}
	bounds, boundsErr := gatewayTextBounds(e)
	if boundsErr != nil {
		return boundsErr
	}
	capability, known := s.pricing.GetTextModelCapabilities(upstreamModel)
	if !known || capability.MaxInputTokens <= 0 {
		return errors.New("unknown mapped model capability")
	}
	for meter, cap := range map[string]int64{"input_tokens": capability.MaxInputTokens, "output_tokens": capability.MaxOutputTokens} {
		reserved, err := strconv.ParseInt(string(bounds[meter]), 10, 64)
		if err != nil || reserved < cap {
			return errors.New("mapped model capability exceeds reserved usage")
		}
	}
	return nil
}

// Anthropic and Bedrock accept max_tokens, including their OAuth/API-key paths.
// Check the transformed body just before request construction rather than
// trusting a pre-transform client parameter.
func (s *GatewayService) validateCentralAnthropicBound(ctx context.Context, body []byte, model string) error {
	e := bc.ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	bounds, boundsErr := gatewayTextBounds(e)
	if boundsErr != nil {
		return boundsErr
	}
	rawReserved, text := bounds["output_tokens"]
	if !text {
		return nil
	}
	reserved, err := strconv.ParseInt(string(rawReserved), 10, 64)
	var payload map[string]json.RawMessage
	if err != nil || reserved <= 0 || json.Unmarshal(body, &payload) != nil {
		return errors.New("invalid central Anthropic reservation")
	}
	var limit int64
	if json.Unmarshal(payload["max_tokens"], &limit) != nil || limit <= 0 || limit > reserved {
		return errors.New("Anthropic output limit exceeds central reservation")
	}
	if model == "" {
		_ = json.Unmarshal(payload["model"], &model)
	}
	// Bedrock's explicit namespace and inference-profile region are transport
	// wrappers, not price-family inference.
	for _, prefix := range []string{"global.", "us.", "eu.", "apac."} {
		model = strings.TrimPrefix(model, prefix)
	}
	model = strings.TrimPrefix(model, "anthropic.")
	model = strings.TrimSuffix(model, ":0")
	model = strings.TrimSuffix(model, "-v1")
	var pricing *PricingService
	if s.billingService != nil {
		pricing = s.billingService.pricingService
	}
	capability, known := pricing.GetTextModelCapabilities(model)
	input, err := strconv.ParseInt(string(bounds["input_tokens"]), 10, 64)
	if !known || capability.MaxInputTokens <= 0 || err != nil || capability.MaxInputTokens > input {
		return errors.New("mapped Anthropic model input exceeds central reservation")
	}
	return nil
}

func gatewayTextBounds(e *bc.Execution) (map[string]bc.Decimal, error) {
	if e == nil {
		return nil, bc.ErrState
	}
	if _, generic := e.Quote.Request.MaximumUsage[gatewayCreditMeter]; !generic {
		return e.Quote.Request.MaximumUsage, nil
	}
	if e.OriginalMaximumUsage["input_tokens"] == "" || e.OriginalMaximumUsage["output_tokens"] == "" {
		return nil, fmt.Errorf("%w: gateway text usage bounds are missing", bc.ErrState)
	}
	return e.OriginalMaximumUsage, nil
}
