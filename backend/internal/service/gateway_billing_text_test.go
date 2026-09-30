package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBillingTextNoMaxUsesKnownCatalogAndEnforceableField(t *testing.T) {
	s := &GatewayBillingCoordinator{}
	for _, tc := range []struct{ path, model, field string }{
		{"/v1/responses", "gpt-6.1-sol", "max_output_tokens"},
		{"/v1/messages", "gpt-6.1-sol-high", "max_tokens"},
		{"/v1/responses", "gpt-6-sol", "max_output_tokens"},
		{"/v1/chat/completions", "gpt-6-luna", "max_completion_tokens"},
		{"/v1/messages", "claude-opus-5-5", "max_tokens"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","input":"hi"}`)
			bounded, err := s.BoundRequest(tc.path, body)
			require.NoError(t, err)
			require.EqualValues(t, 128000, gjson.GetBytes(bounded, tc.field).Int())
			again, err := s.BoundRequest(tc.path, bounded)
			require.NoError(t, err)
			require.Equal(t, bounded, again, "bound rewrite must be deterministic for idempotency")
			quote, err := textBillingQuoteRequest(tc.path, "op", bounded)
			require.NoError(t, err)
			require.Equal(t, bc.Decimal("128000"), quote.MaximumUsage["output_tokens"])
		})
	}
	raw := []byte(`{"model":"video","seconds":10}`)
	unchanged, err := s.BoundRequest("/v1/videos", raw)
	require.NoError(t, err)
	require.Equal(t, raw, unchanged)
}

func TestBillingTextGPT61SolRejectsUnsupportedReasoningBeforeReserve(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"gpt-6.1-sol","reasoning":{"effort":"none"}}`},
		{"/v1/responses", `{"model":"gpt-6.1-sol","reasoning_effort":"none","reasoning":{"effort":"low"}}`},
		{"/v1/chat/completions", `{"model":"gpt-6.1-sol","reasoning_effort":"minimal"}`},
		{"/v1/messages", `{"model":"gpt-6.1-sol","output_config":{"effort":"none"}}`},
		{"/v1/messages", `{"model":"gpt-6.1-sol-none"}`},
		{"/v1/responses", `{"model":"gpt-6.1-sol-minimal"}`},
	} {
		_, err := (&GatewayBillingCoordinator{}).BoundRequest(tc.path, []byte(tc.body))
		require.ErrorContains(t, err, "not supported", tc.body)
	}
	_, err := (&GatewayBillingCoordinator{}).BoundRequest("/v1/responses", []byte(`{"model":"gpt-6-sol","reasoning":{"effort":"none"}}`))
	require.NoError(t, err, "the older GPT-6 Sol still supports none")
}

func TestBillingTextSmallCodexLimitStillReservesHardCap(t *testing.T) {
	raw := []byte(`{"model":"gpt-6-sol","max_output_tokens":23,"input":"hi"}`)
	bounded, max, err := boundTextRequest("/v1/responses", raw, nil)
	require.NoError(t, err)
	require.EqualValues(t, 128000, max)
	require.EqualValues(t, 23, gjson.GetBytes(bounded, "max_output_tokens").Int())
	var transformed map[string]any
	require.NoError(t, json.Unmarshal(bounded, &transformed))
	applyCodexOAuthTransform(transformed, false, false)
	require.NotContains(t, transformed, "max_output_tokens")
	quote, err := textBillingQuoteRequest("/v1/responses", "op", raw)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("128000"), quote.MaximumUsage["output_tokens"])
	// Shadow quoting reads but does not mutate or normalize the user's payload.
	require.JSONEq(t, `{"model":"gpt-6-sol","max_output_tokens":23,"input":"hi"}`, string(raw))
	claude, err := textBillingQuoteRequest("/v1/messages", "op", []byte(`{"model":"claude-opus-5-5","max_tokens":23}`))
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("23"), claude.MaximumUsage["output_tokens"])
}

func TestBillingTextUnknownConflictingOrExcessiveCapsFailClosed(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-6.1-sol-invented","max_output_tokens":5}`,
		`{"model":"gpt-6.1-sol","max_output_tokens":128001}`,
		`{"model":"gpt-6-sol-invented","max_output_tokens":5}`,
		`{"model":"gpt-6-sol","max_output_tokens":128001}`,
		`{"model":"gpt-6-sol","max_output_tokens":2.5}`,
		`{"model":"gpt-6-sol","max_output_tokens":5,"max_tokens":6}`,
	} {
		_, _, err := boundTextRequest("/v1/responses", []byte(body), nil)
		require.Error(t, err, body)
	}
}

func TestBillingTextVisionAndWebSocketContinuationReserveContextCap(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"gpt-6-sol","input":[{"type":"input_image","image_url":"https://example.test/img"}]}`},
		{"/ws/responses", `{"type":"response.create","model":"gpt-6-sol","previous_response_id":"prior","input":[]}`},
	} {
		quote, err := textBillingQuoteRequest(tc.path, "op", []byte(tc.body))
		require.NoError(t, err)
		require.Equal(t, bc.Decimal("922000"), quote.MaximumUsage["input_tokens"])
		require.Equal(t, bc.Decimal("128000"), quote.MaximumUsage["output_tokens"])
		coordinator := &GatewayBillingCoordinator{}
		e := &bc.Execution{Mode: "central", Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: quote.MaximumUsage}}}
		require.NoError(t, coordinator.ValidateBillingModelCaps(e, "gpt-6-luna"))
		require.Error(t, coordinator.ValidateBillingModelCaps(e, "claude-opus-5-5"), "larger input mapping cannot inherit a smaller reservation")
	}
	_, err := textBillingQuoteRequest("/v1/responses", "op", []byte(`{"model":"gpt-6-sol","previous_response_id":"hidden"}`))
	require.Error(t, err)
}

func TestBillingTextCapabilitiesKeepExactDynamicLimits(t *testing.T) {
	pricing := &PricingService{}
	entries, err := pricing.parsePricingData([]byte(`{"custom-model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"max_input_tokens":8000,"max_output_tokens":2000,"litellm_provider":"custom"}}`))
	require.NoError(t, err)
	pricing.pricingData = entries
	cap, ok := pricing.GetTextModelCapabilities("custom-model")
	require.True(t, ok)
	require.EqualValues(t, 8000, cap.MaxInputTokens)
	require.EqualValues(t, 2000, cap.MaxOutputTokens)
	require.False(t, cap.MayUseCodexOAuth)
	_, ok = pricing.GetTextModelCapabilities("custom-model-invented")
	require.False(t, ok)
}

func TestBillingTextFinalUpstreamBoundAndMappingValidation(t *testing.T) {
	svc := &OpenAIGatewayService{billingService: &BillingService{pricingService: &PricingService{pricingData: map[string]*LiteLLMModelPricing{"mapped": {MaxOutputTokens: 200000}}}}}
	e := &bc.Execution{Mode: "central", Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{"output_tokens": "128000", "input_tokens": "922000"}}}}
	ctx := bc.WithExecution(context.Background(), e)
	require.NoError(t, svc.validateCentralOutputBound(ctx, &Account{Type: AccountTypeOAuth}, []byte(`{"model":"gpt-6-sol"}`)))
	require.Error(t, svc.validateCentralOutputBound(ctx, &Account{Type: AccountTypeOAuth}, []byte(`{"model":"mapped"}`)))
	require.Error(t, svc.validateCentralOutputBound(ctx, &Account{Type: AccountTypeAPIKey}, []byte(`{"model":"gpt-6-sol"}`)))
	require.NoError(t, svc.validateCentralOutputBound(ctx, &Account{Type: AccountTypeAPIKey}, []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`)))
	e.Mode = "shadow"
	require.NoError(t, svc.validateCentralOutputBound(ctx, &Account{Type: AccountTypeAPIKey}, []byte(`{"model":"gpt-6-sol"}`)))
}

func TestBillingTextCentralAPIKeyResponsesRetainsMaxAcrossForward(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "passthrough"}[passthrough], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			e := &bc.Execution{Mode: "central", Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{"output_tokens": "128000", "input_tokens": "922000"}}}}
			ctx := bc.WithExecution(context.Background(), e)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Request.Header.Set("User-Agent", "client/1.0")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-only"}, Extra: map[string]any{"openai_passthrough": passthrough}, RateMultiplier: f64p(1)}
			_, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-6-sol","max_output_tokens":23,"input":"hi"}`))
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.EqualValues(t, 23, gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
		})
	}
}

func TestBillingTextAllInputsCoverInjectedInstructionsAndAliases(t *testing.T) {
	quote, err := textBillingQuoteRequest("/v1/responses", "op", []byte(`{"model":"gpt-6-sol","input":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("922000"), quote.MaximumUsage["input_tokens"])
	alias, err := textBillingQuoteRequest("/antigravity/v1/messages", "op", []byte(`{"model":"claude-opus-5-5","max_tokens":23}`))
	require.NoError(t, err)
	direct, err := textBillingQuoteRequest("/v1/messages", "op", []byte(`{"model":"claude-opus-5-5","max_tokens":23}`))
	require.NoError(t, err)
	require.Equal(t, direct.MaximumUsage, alias.MaximumUsage)
	require.NotEqual(t, direct.RequestPayloadHash, alias.RequestPayloadHash)
	for _, typ := range []string{"file_search", "mcp", "web_search_20250305", "code_execution_20260101"} {
		_, err = textBillingQuoteRequest("/v1/responses", "op", []byte(`{"model":"gpt-6-sol","tools":[{"type":"`+typ+`"}]}`))
		require.Error(t, err)
	}
}
