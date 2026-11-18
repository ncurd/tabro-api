package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAliyunGatewayModelCapabilitiesRespectConfiguredModelScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
			"model_mapping": map[string]any{
				"qwen4-max":             "qwen4-max",
				"dashscope/qwen3.8-max": "qwen3.8-max",
				"glm-5":                 "glm-5",
				"ZHIPU/GLM-5":           "ZHIPU/GLM-5",
				"deepseek-v4.1-flash":   "deepseek-v4.1-flash",
				"MiniMax/MiniMax-M3":    "MiniMax/MiniMax-M3",
				"friendly-name":         "qwen4-max",
				"qwen4-max-2026-11-18":  "qwen4-max",
			},
		},
	}}}
	models := readGatewayModelTokenCatalog(t, gatewayModelsCatalogRouter(repo, service.PlatformOpenAI))
	require.Len(t, models, 8, "capability metadata must not expand the configured model whitelist")
	for _, model := range []struct {
		id                     string
		context, input, output int64
	}{
		{"qwen4-max", 1000000, 991808, 262144},
		{"dashscope/qwen3.8-max", 1000000, 991808, 131072},
		{"glm-5", 202752, 169984, 16384},
		{"ZHIPU/GLM-5", 204800, 204800, 131072},
		{"deepseek-v4.1-flash", 1000000, 1000000, 393216},
	} {
		entry := models[model.id]
		require.NotNil(t, entry, model.id)
		require.EqualValues(t, model.context, entry["context_window"], model.id)
		require.EqualValues(t, model.input, entry["max_input_tokens"], model.id)
		require.EqualValues(t, model.output, entry["max_output_tokens"], model.id)
		require.Equal(t, "model", entry["type"])
		require.NotEmpty(t, entry["display_name"])
	}
	partial := models["MiniMax/MiniMax-M3"]
	require.EqualValues(t, 1048576, partial["context_window"])
	require.EqualValues(t, 1048576, partial["max_input_tokens"])
	require.NotContains(t, partial, "max_output_tokens", "unknown is omitted rather than exposed as a zero or invented limit")
	for _, id := range []string{"friendly-name", "qwen4-max-2026-11-18"} {
		require.Contains(t, models, id, "an explicitly configured alias remains visible")
		for _, field := range []string{"context_window", "max_input_tokens", "max_output_tokens"} {
			require.NotContains(t, models[id], field, "an upstream mapping does not establish the requested alias's exact capabilities")
		}
	}
	require.NotContains(t, models, "glm-5.2")
	require.NotContains(t, models, "gpt-6.2-sol")
}

func TestAliyunGatewayDefaultModelsExposeDocumentedTokenLimitsWithoutPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
	}}}
	models := readGatewayModelTokenCatalog(t, gatewayModelsCatalogRouter(repo, service.PlatformOpenAI))
	for _, id := range []string{"qwen4-max", "qwen3.8-max", "glm-5.2", "deepseek-v4-pro"} {
		require.Contains(t, models, id)
		require.Positive(t, models[id]["context_window"])
		require.Positive(t, models[id]["max_input_tokens"])
		require.Positive(t, models[id]["max_output_tokens"])
	}
	require.NotContains(t, models, "gpt-6.2-sol")
	require.NotContains(t, models, "glm-4.5")
}
