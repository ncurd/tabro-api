package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayModelTokenLimitsUsesExactCatalogAndKeepsUnknownLimitsAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	cfg.Pricing.RemoteURL = "https://example.com/pricing.json"
	pricing := service.NewPricingService(cfg, modelCatalogPricingRemote{})
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)
	// A price may match this dated name fuzzily. Capacity must still be unknown.
	require.NotNil(t, pricing.GetModelPricing("trusted-custom-20261012"))
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"trusted-custom":          "trusted-custom",
			"trusted-custom-20261012": "trusted-custom",
			"gpt-6.1-sol-high":        "gpt-6.1-sol",
		}},
	}}}
	models := readGatewayModelTokenCatalog(t, gatewayModelsCatalogRouter(repo, service.PlatformOpenAI, pricing))

	require.Len(t, models, 3, "metadata must not expand the authorized model catalog")
	require.EqualValues(t, 65536, models["trusted-custom"]["max_input_tokens"])
	require.EqualValues(t, 4096, models["trusted-custom"]["max_output_tokens"])
	require.Equal(t, "trusted-custom", models["trusted-custom"]["display_name"])
	require.NotContains(t, models["trusted-custom-20261012"], "max_input_tokens")
	require.NotContains(t, models["trusted-custom-20261012"], "max_output_tokens")
	require.EqualValues(t, 922000, models["gpt-6.1-sol-high"]["max_input_tokens"], "explicit supported reasoning aliases use the exact base catalog")
	require.EqualValues(t, 128000, models["gpt-6.1-sol-high"]["max_output_tokens"])
	for _, model := range models {
		require.NotContains(t, model, "context_window", "independent input/output bounds do not establish combined capacity")
	}
}

func TestGatewayModelTokenLimitsPreservesOpenAIAndClaudeMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		platform string
		model    string
		input    int
	}{
		{service.PlatformOpenAI, "gpt-6.1-sol", 922000},
		{service.PlatformAnthropic, "claude-opus-5-5", 1000000},
	} {
		t.Run(test.platform, func(t *testing.T) {
			repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
				ID: 1, Platform: test.platform, Type: service.AccountTypeAPIKey,
			}}}
			models := readGatewayModelTokenCatalog(t, gatewayModelsCatalogRouter(repo, test.platform))
			model := models[test.model]
			require.NotNil(t, model)
			require.Equal(t, "model", model["type"])
			require.NotEmpty(t, model["display_name"])
			require.EqualValues(t, test.input, model["max_input_tokens"])
			require.EqualValues(t, 128000, model["max_output_tokens"])
			require.NotContains(t, model, "context_window")
		})
	}
}

func TestGatewayModelTokenLimitsDoesNotInventTextLimitsForMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformAzureSpeech, Type: service.AccountTypeAPIKey,
	}}}
	models := readGatewayModelTokenCatalog(t, gatewayModelsCatalogRouter(repo, service.PlatformAzureSpeech))
	require.NotEmpty(t, models)
	for _, model := range models {
		require.NotContains(t, model, "max_input_tokens")
		require.NotContains(t, model, "max_output_tokens")
		require.NotContains(t, model, "context_window")
	}
}

func readGatewayModelTokenCatalog(t *testing.T, router *gin.Engine) map[string]map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	models := make(map[string]map[string]any, len(response.Data))
	for _, model := range response.Data {
		models[model["id"].(string)] = model
	}
	return models
}

type modelCatalogPricingRemote struct{}

func (modelCatalogPricingRemote) FetchPricingJSON(context.Context, string) ([]byte, error) {
	return []byte(`{"trusted-custom":{"max_input_tokens":65536,"max_output_tokens":4096,"input_cost_per_token":0.000001}}`), nil
}

func (modelCatalogPricingRemote) FetchHashText(context.Context, string) (string, error) {
	return "", nil
}
