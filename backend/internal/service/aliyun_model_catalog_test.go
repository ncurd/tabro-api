package service

import (
	"encoding/json"
	"strconv"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAliyunTextModelCapabilitiesUseDocumentedCatalogBeforeRemotePrices(t *testing.T) {
	for _, source := range []struct {
		name    string
		pricing *PricingService
	}{
		{name: "nil_pricing"},
		{name: "empty_remote", pricing: &PricingService{}},
		{name: "outdated_remote", pricing: &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"qwen4-max": {MaxInputTokens: 8192, MaxOutputTokens: 2048},
			"glm-5":     {MaxInputTokens: 204800, MaxOutputTokens: 131072},
		}}},
	} {
		t.Run(source.name, func(t *testing.T) {
			for _, model := range []struct {
				id                     string
				context, input, output int64
			}{
				{"qwen4-max", 1000000, 991808, 262144},
				{"aliyun/qwen4-max", 1000000, 991808, 262144},
				{"dashscope/qwen3.8-max", 1000000, 991808, 131072},
				{"qwen3.8-max-2026-09-02", 1000000, 991808, 131072},
				{"glm-5", 202752, 169984, 16384},
				{"ZHIPU/GLM-5", 204800, 204800, 131072},
				{"aliyun/ZHIPU/GLM-5", 204800, 204800, 131072},
				{"glm-5.2", 1048576, 1048576, 131072},
				{"deepseek-v4-pro", 1000000, 1000000, 393216},
				{"deepseek-v4.1-flash-1017", 1000000, 1000000, 393216},
				{"vanchin/deepseek-v4-pro", 1048576, 1048576, 393216},
			} {
				t.Run(model.id, func(t *testing.T) {
					caps, known := source.pricing.GetTextModelCapabilities(model.id)
					require.True(t, known)
					require.Equal(t, model.input, caps.MaxInputTokens)
					require.Equal(t, model.output, caps.MaxOutputTokens)
					require.Equal(t, model.context, caps.ContextWindow, "combined capacity is a separate documented field")
					require.False(t, caps.MayUseCodexOAuth)
				})
			}
		})
	}
}

func TestAliyunTextModelCapabilitiesDoNotInferUnknownAliasesOrMissingOutput(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"trusted-custom":     {MaxInputTokens: 65536, MaxOutputTokens: 4096},
		"minimax/minimax-m3": {MaxInputTokens: 1048576, MaxOutputTokens: 131072},
	}}
	for _, model := range []string{
		"qwen4-max-invented", "qwen3.8-max-2026-11-18", "glm-5-latest",
		"ZHIPU/GLM-5-invented", "deepseek-v4.1-flash-2026-11-18",
		"trusted-custom-20261012", "my-qwen4-max",
	} {
		_, known := pricing.GetTextModelCapabilities(model)
		require.False(t, known, model)
	}
	for _, service := range []*PricingService{nil, pricing} {
		caps, bounded := service.GetTextModelCapabilities("MiniMax/MiniMax-M3")
		require.False(t, bounded, "a remote price must not invent an undocumented output limit")
		require.EqualValues(t, 1048576, caps.ContextWindow)
		require.EqualValues(t, 1048576, caps.MaxInputTokens)
		require.Zero(t, caps.MaxOutputTokens)
	}
}

func TestAliyunAccountModelDefaultsRecognizeOfficialAPIKeyEndpoints(t *testing.T) {
	for _, baseURL := range []string{
		"https://dashscope.aliyuncs.com/compatible-mode/v1",
		"https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		"https://dashscope-us.aliyuncs.com/compatible-mode/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": baseURL}}
			models := account.AvailableOpenAIModels()
			ids := accountCatalogModelIDs(models)
			for _, id := range []string{"qwen4-max", "qwen3.8-max", "glm-5", "ZHIPU/GLM-5", "deepseek-v4-pro", "deepseek-v4.1-flash"} {
				require.Contains(t, ids, id)
			}
			require.NotContains(t, ids, "gpt-6.2-sol")
			require.NotContains(t, ids, "glm-4.5", "historical models remain configurable but are not defaults")
			for _, model := range models {
				require.Equal(t, "aliyun", model.OwnedBy)
				require.Equal(t, "model", model.Object)
				require.Equal(t, model.ID, model.DisplayName)
			}
		})
	}
	for _, test := range []struct {
		name, accountType, baseURL string
	}{
		{"openai_api", AccountTypeAPIKey, "https://api.openai.com/v1"},
		{"third_party", AccountTypeAPIKey, "https://example.com/dashscope.aliyuncs.com/compatible-mode/v1"},
		{"lookalike_host", AccountTypeAPIKey, "https://dashscope.aliyuncs.com.example.com/v1"},
		{"oauth", AccountTypeOAuth, "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: test.accountType, Credentials: map[string]any{"base_url": test.baseURL}}
			require.ElementsMatch(t, openai.ModelsForAccount(account.IsOAuth()), account.AvailableOpenAIModels())
		})
	}
}

func TestAliyunAccountModelMappingRemainsAnExplicitWhitelist(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
			"model_mapping": map[string]any{
				"private-name": "qwen4-max",
				"glm-5":        "glm-5",
				"ZHIPU/GLM-5":  "ZHIPU/GLM-5",
				"future-model": "vendor/unknown-model",
			},
		},
	}
	require.Equal(t, []string{"ZHIPU/GLM-5", "future-model", "glm-5", "private-name"}, accountCatalogModelIDs(account.AvailableOpenAIModels()))
}

func TestAliyunBillingTextBoundsAndQuotesUseDocumentedOutputModes(t *testing.T) {
	for _, test := range []struct {
		name, path, field, request string
		input, output              int64
	}{
		{"chat_default", "/v1/chat/completions", "max_completion_tokens", `{"model":"qwen4-max","messages":[{"role":"user","content":"hi"}]}`, 991808, 262144},
		{"responses_default", "/v1/responses", "max_output_tokens", `{"model":"dashscope/deepseek-v4.1-flash","input":"hi"}`, 1000000, 393216},
		{"messages_default", "/v1/messages", "max_tokens", `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`, 1048576, 131072},
		{"non_thinking_default", "/v1/chat/completions", "max_completion_tokens", `{"model":"qwen4-max","enable_thinking":false,"messages":[]}`, 991808, 131072},
		{"non_thinking_explicit", "/v1/chat/completions", "max_completion_tokens", `{"model":"aliyun/qwen4-max","enable_thinking":false,"max_tokens":131072,"messages":[]}`, 991808, 131072},
		{"smaller_user_limit", "/v1/chat/completions", "max_completion_tokens", `{"model":"qwen4-max","max_tokens":23,"messages":[]}`, 991808, 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			bounded, reserved, err := boundTextRequest(test.path, []byte(test.request), nil)
			require.NoError(t, err)
			require.Equal(t, test.output, reserved)
			require.Equal(t, test.output, gjson.GetBytes(bounded, test.field).Int())
			for _, field := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
				if field != test.field {
					require.False(t, gjson.GetBytes(bounded, field).Exists())
				}
			}
			again, _, err := boundTextRequest(test.path, bounded, nil)
			require.NoError(t, err)
			require.JSONEq(t, string(bounded), string(again))
			quote, err := textBillingQuoteRequest(test.path, "op", []byte(test.request))
			require.NoError(t, err)
			require.Equal(t, bc.Decimal(strconv.FormatInt(test.input, 10)), quote.MaximumUsage["input_tokens"])
			require.Equal(t, bc.Decimal(strconv.FormatInt(test.output, 10)), quote.MaximumUsage["output_tokens"])
			var original map[string]any
			require.NoError(t, json.Unmarshal([]byte(test.request), &original))
			if thinking, present := original["enable_thinking"]; present {
				require.Equal(t, thinking, gjson.GetBytes(bounded, "enable_thinking").Bool())
			}
		})
	}
	for _, request := range []string{
		`{"model":"qwen4-max","enable_thinking":false,"max_completion_tokens":131073}`,
		`{"model":"MiniMax/MiniMax-M3","input":"hi"}`,
		`{"model":"MiniMax/MiniMax-M3","max_tokens":1,"input":"hi"}`,
		`{"model":"qwen4-max-invented","max_tokens":1,"input":"hi"}`,
	} {
		_, _, err := boundTextRequest("/v1/chat/completions", []byte(request), nil)
		require.Error(t, err, request)
		_, err = textBillingQuoteRequest("/v1/chat/completions", "op", []byte(request))
		require.Error(t, err, request)
	}
}
