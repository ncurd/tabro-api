package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOAuthRetiredModelsUseAvailableReplacements(t *testing.T) {
	oauth := &Account{Type: AccountTypeOAuth}
	apiKey := &Account{Type: AccountTypeAPIKey}
	for _, tt := range []struct{ model, replacement string }{
		{"gpt-5.4", "gpt-6-sol"},
		{"gpt-5.4-pro", "gpt-6-sol"},
		{"gpt-5.4-2026-03-05", "gpt-6-sol"},
		{"openai/gpt-5.4-high", "gpt-6-sol"},
		{"gpt-5.4-mini", "gpt-6-luna"},
		{"gpt-5.4-mini-2026-03-17", "gpt-6-luna"},
		{"gpt-5.5", "gpt-6.1-sol"},
		{"gpt-5.5-pro-2026-04-23", "gpt-6.1-sol"},
		{"gpt-5.5-xhigh", "gpt-6.1-sol"},
		{"gpt-6-sol", "gpt-6-sol"},
		{"gpt-6.1-sol-max", "gpt-6.1-sol"},
	} {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.replacement, normalizeOpenAIModelForUpstream(oauth, tt.model))
			require.Equal(t, tt.model, normalizeOpenAIModelForUpstream(apiKey, tt.model), "API-key accounts have an independent model lifecycle")
		})
	}
	for _, model := range []string{"", "unknown-model", "unrecognized-codex"} {
		require.Equal(t, "gpt-6-sol", normalizeOpenAIModelForUpstream(oauth, model))
	}
	// Generic alias parsing also serves API-key requests and historical pricing.
	require.Equal(t, "gpt-5.4", normalizeCodexModel("gpt-5.4-high"))
	require.Equal(t, "gpt-5.5", normalizeCodexModel("gpt-5.5"))
}

func TestApplyOAuthTransformMigratesRetiredAliasesWithoutLosingEffort(t *testing.T) {
	for _, explicitEffort := range []string{"", "low"} {
		body := map[string]any{"model": "openai/gpt-5.4-high", "input": []any{}}
		if explicitEffort != "" {
			body["reasoning"] = map[string]any{"effort": explicitEffort}
		}
		result := applyCodexOAuthTransform(body, false, false)
		require.Equal(t, "gpt-6-sol", body["model"])
		require.Equal(t, "gpt-6-sol", result.NormalizedModel)
		effort := "high"
		if explicitEffort != "" {
			effort = explicitEffort
		}
		require.Equal(t, effort, body["reasoning"].(map[string]any)["effort"])
	}
}

func retirementForwardFixture(t *testing.T, endpoint string, body []byte, accountType, model string) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account, *httpUpstreamRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	response := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_retirement\",\"model\":%q}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retirement\",\"object\":\"response\",\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\ndata: [DONE]\n\n", model, model)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response))}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "oauth-test", "chatgpt_account_id": "acct-test", "api_key": "sk-test", "base_url": "https://proxy.example.com/v1"}}
	return svc, c, rec, account, upstream
}

func TestOAuthRetirementChatForwardsCurrentModelForSyncAndStream(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, stream := range []bool{false, true} {
			for _, tt := range []struct{ model, replacement string }{{"gpt-5.4", "gpt-6-sol"}, {"gpt-5.4-mini", "gpt-6-luna"}, {"gpt-5.5", "gpt-6.1-sol"}, {"gpt-5.4-high", "gpt-6-sol"}} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", accountType, tt.model, stream), func(t *testing.T) {
					wantModel := tt.replacement
					if accountType == AccountTypeAPIKey {
						wantModel = tt.model
					}
					body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, tt.model, stream))
					svc, c, rec, account, upstream := retirementForwardFixture(t, "/v1/chat/completions", body, accountType, wantModel)
					result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					require.NoError(t, err)
					require.Equal(t, wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, tt.model, result.Model)
					require.Equal(t, wantModel, result.UpstreamModel)
					if accountType == AccountTypeOAuth {
						require.Equal(t, wantModel, result.BillingModel)
					}
					require.Equal(t, http.StatusOK, rec.Code)
					if accountType == AccountTypeOAuth && tt.model == "gpt-5.4-high" {
						require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
					}
				})
			}
		}
	}
}

func TestOAuthRetirementChatMappingsKeepAccountPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, accountType, accountMapping, channelDefault, wantModel string
	}{
		{"account overrides channel", AccountTypeOAuth, "gpt-5.4-mini", "gpt-5.5", "gpt-6-luna"},
		{"channel fallback", AccountTypeOAuth, "", "gpt-5.4", "gpt-6-sol"},
		{"current account target remains", AccountTypeOAuth, "gpt-6-luna", "gpt-5.5", "gpt-6-luna"},
		{"API key mapping remains", AccountTypeAPIKey, "gpt-5.4", "gpt-6-sol", "gpt-5.4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`)
			svc, c, _, account, upstream := retirementForwardFixture(t, "/v1/chat/completions", body, tt.accountType, tt.wantModel)
			if tt.accountMapping != "" {
				account.Credentials["model_mapping"] = map[string]any{"client-model": tt.accountMapping}
			}
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", tt.channelDefault)
			require.NoError(t, err)
			require.Equal(t, "client-model", result.Model)
			require.Equal(t, tt.wantModel, result.UpstreamModel)
			require.Equal(t, tt.wantModel, result.BillingModel)
			require.Equal(t, tt.wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}

func TestOAuthRetirementChatResponsesShapePreservesEffort(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-high","input":"hello","stream":false}`)
	svc, c, _, account, upstream := retirementForwardFixture(t, "/v1/chat/completions", body, AccountTypeOAuth, "gpt-6-sol")
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4-high", result.Model)
	require.Equal(t, "gpt-6-sol", result.UpstreamModel)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
}

func TestOAuthRetirementPassthroughRetainsRequestedAndUpstreamModels(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-high","input":"hello","instructions":"help","stream":true}`)
	svc, c, _, account, upstream := retirementForwardFixture(t, "/v1/responses", body, AccountTypeOAuth, "gpt-6-sol")
	account.Extra = map[string]any{"openai_passthrough": true}
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4-high", result.Model)
	require.Equal(t, "gpt-6-sol", result.UpstreamModel)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
}

func TestOAuthRetirementUnsupportedEffortRejectedBeforeSending(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/passthrough"} {
		t.Run(endpoint, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5-none","messages":[{"role":"user","content":"hello"}],"max_tokens":128,"input":"hello","instructions":"help"}`)
			svc, c, rec, account, upstream := retirementForwardFixture(t, endpoint, body, AccountTypeOAuth, "gpt-6.1-sol")
			var err error
			switch endpoint {
			case "/v1/chat/completions":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			case "/v1/messages":
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			default:
				if endpoint == "/passthrough" {
					account.Extra = map[string]any{"openai_passthrough": true}
				}
				_, err = svc.Forward(context.Background(), c, account, body)
			}
			require.ErrorIs(t, err, ErrUnsupportedGPT61SolReasoning)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestOAuthRetirementPreservesCentralCapabilityReservation(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"hello"}]}`)
	svc, c, _, account, upstream := retirementForwardFixture(t, "/v1/chat/completions", body, AccountTypeOAuth, "gpt-6-luna")
	pricing := &PricingService{}
	capability, known := pricing.GetTextModelCapabilities("gpt-6-luna")
	require.True(t, known)
	ctx := bc.WithExecution(context.Background(), &bc.Execution{Mode: "central", Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{
		"input_tokens": bc.Decimal(strconv.FormatInt(capability.MaxInputTokens, 10)), "output_tokens": bc.Decimal(strconv.FormatInt(capability.MaxOutputTokens-1, 10)),
	}}}})
	result, err := svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
	require.ErrorContains(t, err, "mapped OAuth model output exceeds reserved capability")
	require.Nil(t, result)
	require.Nil(t, upstream.lastReq)
}

func TestOAuthRetirementAccountTestPayloadUsesProductionMigration(t *testing.T) {
	for _, model := range []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.4-high"} {
		oauth := createOpenAITestPayload(model, true)
		require.Equal(t, normalizeOpenAIModelForUpstream(&Account{Type: AccountTypeOAuth}, model), oauth["model"])
		apiKey := createOpenAITestPayload(model, false)
		require.Equal(t, model, apiKey["model"])
	}
}

func TestOAuthRetirementAccountWhitelistUsesAuthorizedReplacementOnly(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping": map[string]any{"gpt-6-sol": "gpt-6-sol"},
	}}
	for _, model := range []string{"gpt-5.4", "gpt-5.4-high", "gpt-5.4-pro-2026-03-05"} {
		require.True(t, account.IsModelSupported(model))
		mapped, matched := account.ResolveMappedModel(model)
		require.True(t, matched)
		require.Equal(t, "gpt-6-sol", mapped)
	}
	for _, model := range []string{"gpt-5.4-mini", "gpt-5.5", "gpt-5.5-mini", "unknown-model", "custom-gpt-5.4-model", "gpt-5.4-unsupported-suffix", ""} {
		require.False(t, account.IsModelSupported(model), model)
	}
	apiKey := *account
	apiKey.Type = AccountTypeAPIKey
	require.False(t, apiKey.IsModelSupported("gpt-5.4"))
	_, matched := apiKey.ResolveMappedModel("gpt-5.4")
	require.False(t, matched)

	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.6-sol", "gpt-6-sol": "gpt-6-sol"}
	// Exact account configuration takes precedence over compatibility migration.
	require.Equal(t, "gpt-5.6-sol", account.GetMappedModel("gpt-5.4"))
}

func TestOAuthRetirementReplacementStillHonorsUpstreamChannelRestriction(t *testing.T) {
	groupID := int64(81)
	channels := newOpenAIWSTurnChannelService(groupID, Channel{
		ID: 82, Status: StatusActive, RestrictModels: true, BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-5.4"}}},
	})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	svc := &OpenAIGatewayService{channelService: channels}
	require.True(t, svc.isUpstreamModelRestrictedByChannel(context.Background(), groupID, account, "gpt-5.4"))
	require.Equal(t, "gpt-6-sol", resolveAccountUpstreamModel(account, "gpt-5.4"))
	_, _, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), &groupID, account, "gpt-5.4")
	require.ErrorContains(t, err, "upstream model")
	require.ErrorContains(t, err, "restricted")
}

func TestOAuthRetirementResponsesMessagesAndWebSocketResolution(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-high","input":"hello","instructions":"help","stream":false}`)
	svc, c, _, account, upstream := retirementForwardFixture(t, "/v1/responses", body, AccountTypeOAuth, "gpt-6-sol")
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-sol", result.UpstreamModel)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())

	normalized, _, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(normalized, "model").String())
	require.Equal(t, "high", gjson.GetBytes(normalized, "reasoning.effort").String())

	body = []byte(`{"model":"gpt-5.4-high","messages":[{"role":"user","content":"hello"}],"max_tokens":128}`)
	svc, c, _, account, upstream = retirementForwardFixture(t, "/v1/messages", body, AccountTypeOAuth, "gpt-6-sol")
	result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, "gpt-6-sol", result.UpstreamModel)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())

	for _, requested := range []string{"gpt-5.4", "gpt-5.4-high", "gpt-5.5"} {
		_, model, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), nil, account, requested)
		require.NoError(t, err)
		require.Equal(t, normalizeOpenAIModelForUpstream(account, requested), model)
		require.NotEqual(t, requested, model)
	}
}
