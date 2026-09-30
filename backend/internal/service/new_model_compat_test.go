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

	"github.com/Wei-Shaw/sub2api/internal/config"
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

func TestForwardAsAnthropic_GPT61SolDefaultsToMediumAndKeepsMax(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		model  string
		effort string
	}{
		{name: "default medium", model: "gpt-6.1-sol", effort: "medium"},
		{name: "max suffix", model: "gpt-6.1-sol-max", effort: "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`, tc.model))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			upstreamBody := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\ndata: [DONE]\n\n"
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
					"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"},
				},
			}
			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-6.1-sol")
			require.NoError(t, err)
			require.Equal(t, "gpt-6.1-sol", result.UpstreamModel)
			require.Equal(t, "gpt-6.1-sol", result.BillingModel)
			require.Equal(t, "gpt-6.1-sol", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, tc.effort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			require.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestGPT61SolUnsupportedEffortRejectedBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	for _, tc := range []struct {
		name    string
		path    string
		body    string
		forward func(*gin.Context, []byte) error
	}{
		{
			name: "messages alias none",
			path: "/v1/messages",
			body: `{"model":"gpt-6.1-sol-none","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			forward: func(c *gin.Context, body []byte) error {
				_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-6.1-sol")
				return err
			},
		},
		{
			name: "messages explicit minimal",
			path: "/v1/messages",
			body: `{"model":"gpt-6.1-sol","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"output_config":{"effort":"minimal"}}`,
			forward: func(c *gin.Context, body []byte) error {
				_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-6.1-sol")
				return err
			},
		},
		{
			name: "chat completions explicit none",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`,
			forward: func(c *gin.Context, body []byte) error {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				return err
			},
		},
		{
			name: "responses explicit minimal",
			path: "/v1/responses",
			body: `{"model":"gpt-6.1-sol","input":"hello","reasoning":{"effort":"minimal"}}`,
			forward: func(c *gin.Context, body []byte) error {
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
		},
		{
			name: "responses flat none",
			path: "/v1/responses",
			body: `{"model":"gpt-6.1-sol","input":"hello","reasoning_effort":"none"}`,
			forward: func(c *gin.Context, body []byte) error {
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			require.ErrorContains(t, tc.forward(c, body), "not supported by gpt-6.1-sol")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "not supported by gpt-6.1-sol")
		})
	}
}

func TestGPT61SolChatCompletionsToolsUseResponsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol"}}`,
			``,
			`data: {"type":"response.output_text.delta","delta":"ok"}`,
			``,
			`data: {"type":"response.done"}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-api-key"},
	}
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
	require.Equal(t, "gpt-6.1-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	require.Equal(t, http.StatusOK, rec.Code)
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
