package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT6SolAndLunaPreserveModelAndReasoning(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			for _, alias := range []string{model, "openai/" + model, strings.ToUpper(model), strings.ReplaceAll(model, "-", " ")} {
				require.Equal(t, model, normalizeCodexModel(alias))
			}
			for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
				t.Run(effort, func(t *testing.T) {
					alias := "openai/" + model + "-" + effort
					require.Equal(t, model, normalizeOpenAIModelForUpstream(&Account{Type: AccountTypeOAuth}, alias))
					req := &apicompat.AnthropicRequest{Model: alias}
					applyOpenAICompatModelNormalization(req)
					require.Equal(t, model, req.Model)
					require.NotNil(t, req.OutputConfig)
					require.Equal(t, effort, req.OutputConfig.Effort)
					converted, err := apicompat.AnthropicToResponses(req)
					require.NoError(t, err)
					require.Equal(t, model, converted.Model)
					require.NotNil(t, converted.Reasoning)
					require.Equal(t, effort, converted.Reasoning.Effort)
				})
			}
		})
	}
}

func TestForwardAsAnthropic_GPT6SolAndLunaKeepUpstreamAndBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, effort := range []string{"none", "xhigh", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				alias := model + "-" + effort
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`, alias))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				upstreamBody := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\ndata: [DONE]\n\n", model)
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(upstreamBody)),
				}}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				account := &Account{
					ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{
						"access_token": "oauth-token", "chatgpt_account_id": "account-id",
						"model_mapping": map[string]any{model: model},
					},
				}
				result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.6-sol")
				require.NoError(t, err)
				require.Equal(t, model, result.UpstreamModel)
				require.Equal(t, model, result.BillingModel)
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, effort, *result.ReasoningEffort)
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, effort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, alias, gjson.GetBytes(rec.Body.Bytes(), "model").String())
			})
		}
	}
}

func TestGPT6CatalogPreservesCustomMappings(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"gpt-6-sol":    "gpt-6-luna",
			"custom-model": "gpt-6-sol",
		}},
	}
	require.ElementsMatch(t, []string{"gpt-6-sol", "custom-model"}, accountCatalogModelIDs(account.AvailableOpenAIModels()))
	require.Equal(t, "gpt-6-luna", resolveOpenAIForwardModel(account, "gpt-6-sol", "gpt-5.6-sol"))
	require.Equal(t, "gpt-6-sol", resolveOpenAIForwardModel(account, "custom-model", "gpt-5.6-sol"))
}

func TestClaudeOpus55BedrockResolution(t *testing.T) {
	account := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeBedrock,
		Credentials: map[string]any{"aws_region": "eu-west-1"},
	}
	model, ok := ResolveBedrockModelID(account, "claude-opus-5-5")
	require.True(t, ok)
	require.Equal(t, "anthropic.claude-opus-5-5", model)
	require.True(t, isBedrockClaude45OrNewer(model))

	account.Credentials["model_mapping"] = map[string]any{"claude-opus-5-5": "custom-inference-profile"}
	require.Equal(t, "custom-inference-profile", account.GetMappedModel("claude-opus-5-5"))
}
