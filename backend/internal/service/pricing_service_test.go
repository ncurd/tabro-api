package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pricingRemoteClientStub struct {
	body       []byte
	fetchJSONs int
}

func (s *pricingRemoteClientStub) FetchPricingJSON(_ context.Context, _ string) ([]byte, error) {
	s.fetchJSONs++
	return s.body, nil
}

func (s *pricingRemoteClientStub) FetchHashText(_ context.Context, _ string) (string, error) {
	return "", nil
}

func TestFallbackPricingFile_ContainsLatestOpenAIAndAnthropicModels(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	svc := &PricingService{}
	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	for _, model := range []string{
		"gpt-6-astra",
		"gpt-5.6",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-image-2",
		"gpt-image-2-2026-04-21",
		"gpt-realtime-1.5",
		"gpt-realtime-2",
		"gpt-realtime-mini",
		"gpt-realtime-translate",
		"claude-fable-5",
		"claude-fable-5-1",
		"claude-opus-5",
	} {
		require.Contains(t, data, model)
	}
}

func TestFallbackPricingFile_NewModelsUseOfficialPricing(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	svc := &PricingService{}
	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	tests := []struct {
		model        string
		input        float64
		output       float64
		cacheWrite   float64
		cacheRead    float64
		cacheWrite1h float64
	}{
		{model: "gpt-6-astra", input: 10e-6, output: 50e-6, cacheWrite: 12.5e-6, cacheRead: 1e-6},
		{model: "gpt-5.6", input: 4e-6, output: 20e-6, cacheWrite: 5e-6, cacheRead: 0.4e-6},
		{model: "gpt-5.6-sol", input: 4e-6, output: 20e-6, cacheWrite: 5e-6, cacheRead: 0.4e-6},
		{model: "gpt-5.6-terra", input: 2e-6, output: 12e-6, cacheWrite: 2.5e-6, cacheRead: 0.2e-6},
		{model: "gpt-5.6-luna", input: 0.2e-6, output: 1.2e-6, cacheWrite: 0.25e-6, cacheRead: 0.02e-6},
		{model: "claude-fable-5-1", input: 10e-6, output: 50e-6, cacheWrite: 12.5e-6, cacheRead: 0.25e-6, cacheWrite1h: 20e-6},
		{model: "claude-opus-5", input: 5e-6, output: 25e-6, cacheWrite: 6.25e-6, cacheRead: 0.5e-6, cacheWrite1h: 10e-6},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := data[tt.model]
			require.NotNil(t, pricing)
			require.InDelta(t, tt.input, pricing.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.output, pricing.OutputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheWrite, pricing.CacheCreationInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheWrite1h, pricing.CacheCreationInputTokenCostAbove1hr, 1e-12)
		})
	}
}

func TestFallbackPricingFile_NewModelsIncludeOfficialBatchPricing(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	type batchPricing struct {
		Input            float64            `json:"input_cost_per_token_batches"`
		Output           float64            `json:"output_cost_per_token_batches"`
		CacheWrite       float64            `json:"cache_creation_input_token_cost_batches"`
		CacheRead        float64            `json:"cache_read_input_token_cost_batches"`
		SupportsMax      bool               `json:"supports_max_reasoning_effort"`
		SupportsTier     bool               `json:"supports_service_tier"`
		SupportsSpeed    bool               `json:"supports_speed"`
		ProviderSpecific map[string]float64 `json:"provider_specific_entry"`
		DeprecationDate  *string            `json:"deprecation_date"`
	}
	var data map[string]batchPricing
	require.NoError(t, json.Unmarshal(body, &data))

	tests := []struct {
		model      string
		input      float64
		output     float64
		cacheWrite float64
		cacheRead  float64
	}{
		{model: "gpt-6-astra", input: 5e-6, output: 25e-6, cacheWrite: 6.25e-6, cacheRead: 0.5e-6},
		{model: "gpt-5.6", input: 2e-6, output: 10e-6, cacheWrite: 2.5e-6, cacheRead: 0.2e-6},
		{model: "gpt-5.6-sol", input: 2e-6, output: 10e-6, cacheWrite: 2.5e-6, cacheRead: 0.2e-6},
		{model: "gpt-5.6-terra", input: 1e-6, output: 6e-6, cacheWrite: 1.25e-6, cacheRead: 0.1e-6},
		{model: "gpt-5.6-luna", input: 0.1e-6, output: 0.6e-6, cacheWrite: 0.125e-6, cacheRead: 0.01e-6},
		{model: "claude-fable-5-1", input: 5e-6, output: 25e-6, cacheWrite: 6.25e-6, cacheRead: 0.125e-6},
		{model: "claude-opus-5", input: 2.5e-6, output: 12.5e-6, cacheWrite: 3.125e-6, cacheRead: 0.25e-6},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, ok := data[tt.model]
			require.True(t, ok)
			require.InDelta(t, tt.input, pricing.Input, 1e-12)
			require.InDelta(t, tt.output, pricing.Output, 1e-12)
			require.InDelta(t, tt.cacheWrite, pricing.CacheWrite, 1e-12)
			require.InDelta(t, tt.cacheRead, pricing.CacheRead, 1e-12)
			if tt.model == "gpt-6-astra" {
				require.True(t, pricing.SupportsTier)
			}
			if tt.model == "gpt-6-astra" || strings.HasPrefix(tt.model, "gpt-5.6") {
				require.True(t, pricing.SupportsMax)
			}
			if tt.model == "claude-opus-5" {
				require.True(t, pricing.SupportsSpeed)
				require.InDelta(t, 2.0, pricing.ProviderSpecific["fast"], 1e-12)
			}
			if strings.HasPrefix(tt.model, "claude-") {
				require.Nil(t, pricing.DeprecationDate)
			}
		})
	}
}

func TestFallbackPricingFile_ContainsAliyunAdjacentModelFamilies(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	svc := &PricingService{}
	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	for _, model := range []string{
		"dashscope/qwen-plus",
		"minimax/MiniMax-M2.5",
		"meta.llama3-3-70b-instruct-v1:0",
		"moonshot/kimi-latest",
		"deepseek/deepseek-v3",
		"zai/glm-4.5",
	} {
		require.Contains(t, data, model)
	}
}

func TestGetModelPricing_ProviderCandidatesCoverAliyunAdjacentModelFamilies(t *testing.T) {
	qwenPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	qwenOpenRouterPricing := &LiteLLMModelPricing{InputCostPerToken: 7}
	minimaxPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	llamaPricing := &LiteLLMModelPricing{InputCostPerToken: 3}
	kimiPricing := &LiteLLMModelPricing{InputCostPerToken: 4}
	deepseekPricing := &LiteLLMModelPricing{InputCostPerToken: 5}
	glmPricing := &LiteLLMModelPricing{InputCostPerToken: 6}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"dashscope/qwen-plus":             qwenPricing,
			"openrouter/qwen/qwen3.6-plus":    qwenOpenRouterPricing,
			"minimax/MiniMax-M2.5":            minimaxPricing,
			"meta.llama3-3-70b-instruct-v1:0": llamaPricing,
			"moonshot/kimi-latest":            kimiPricing,
			"deepseek/deepseek-v3":            deepseekPricing,
			"zai/glm-4.5":                     glmPricing,
		},
	}

	require.Same(t, qwenPricing, svc.GetModelPricing("qwen-plus"))
	require.Same(t, qwenOpenRouterPricing, svc.GetModelPricing("qwen3.6-plus"))
	require.Same(t, minimaxPricing, svc.GetModelPricing("minimax-m2.5"))
	require.Same(t, llamaPricing, svc.GetModelPricing("llama-3.3-70b-instruct"))
	require.Same(t, kimiPricing, svc.GetModelPricing("kimi-latest"))
	require.Same(t, deepseekPricing, svc.GetModelPricing("deepseek-v3"))
	require.Same(t, glmPricing, svc.GetModelPricing("glm-4.5"))
}

func TestGetModelPricing_AnthropicFable5UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	pricing := svc.GetModelPricing("claude-fable-5")
	require.NotNil(t, pricing)
	require.InDelta(t, 10e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 50e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 12.5e-6, pricing.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 20e-6, pricing.CacheCreationInputTokenCostAbove1hr, 1e-12)
	require.InDelta(t, 1e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.True(t, pricing.SupportsPromptCaching)
}

func TestGetModelPricing_AnthropicLatestModelsUseDedicatedStaticFallbacks(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-fable-5": {InputCostPerToken: 999},
		},
	}

	for _, model := range []string{"claude-fable-5-1", "claude-fable-5.1"} {
		fable := svc.GetModelPricing(model)
		require.NotNil(t, fable)
		require.InDelta(t, 10e-6, fable.InputCostPerToken, 1e-12)
		require.InDelta(t, 50e-6, fable.OutputCostPerToken, 1e-12)
		require.InDelta(t, 0.25e-6, fable.CacheReadInputTokenCost, 1e-12)
		require.NotEqual(t, float64(999), fable.InputCostPerToken, "Fable 5.1 must not inherit Fable 5 through prefix matching")
	}

	opus := svc.GetModelPricing("claude-opus-5")
	require.NotNil(t, opus)
	require.InDelta(t, 5e-6, opus.InputCostPerToken, 1e-12)
	require.InDelta(t, 25e-6, opus.OutputCostPerToken, 1e-12)
	require.InDelta(t, 6.25e-6, opus.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 10e-6, opus.CacheCreationInputTokenCostAbove1hr, 1e-12)
	require.InDelta(t, 0.5e-6, opus.CacheReadInputTokenCost, 1e-12)
}

func TestParsePricingData_KeepsImageOnlyPricingEntries(t *testing.T) {
	svc := &PricingService{}

	data, err := svc.parsePricingData([]byte(`{
		"dashscope/qwen-image-future": {
			"output_cost_per_image": 0.025,
			"litellm_provider": "dashscope",
			"mode": "image_generation"
		}
	}`))

	require.NoError(t, err)
	require.Contains(t, data, "dashscope/qwen-image-future")
	require.InDelta(t, 0.025, data["dashscope/qwen-image-future"].OutputCostPerImage, 1e-12)
}

func TestLoadPricingData_MergesMissingModelsFromFallbackFile(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "local.json")
	fallbackFile := filepath.Join(dir, "fallback.json")

	require.NoError(t, os.WriteFile(localFile, []byte(`{
		"gpt-local": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`), 0644))
	require.NoError(t, os.WriteFile(fallbackFile, []byte(`{
		"dashscope/qwen-plus": {
			"input_cost_per_token": 0.0000004,
			"output_cost_per_token": 0.0000012,
			"litellm_provider": "dashscope",
			"mode": "chat"
		}
	}`), 0644))

	svc := &PricingService{
		cfg: &config.Config{},
	}
	svc.cfg.Pricing.FallbackFile = fallbackFile

	require.NoError(t, svc.loadPricingData(localFile))
	require.Contains(t, svc.pricingData, "gpt-local")
	require.Contains(t, svc.pricingData, "dashscope/qwen-plus")
	require.Same(t, svc.pricingData["dashscope/qwen-plus"], svc.GetModelPricing("qwen-plus"))
}

func TestCheckAndUpdatePricing_DownloadsWhenRemoteSourceChanges(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model_pricing.json"), []byte(`{
		"gpt-local": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model_pricing.source"), []byte("https://old.example/prices.json\n"), 0644))

	remote := &pricingRemoteClientStub{body: []byte(`{
		"dashscope/qwen-plus": {
			"input_cost_per_token": 0.0000004,
			"output_cost_per_token": 0.0000012,
			"litellm_provider": "dashscope",
			"mode": "chat"
		}
	}`)}
	svc := NewPricingService(&config.Config{}, remote)
	svc.cfg.Pricing.DataDir = dir
	svc.cfg.Pricing.RemoteURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	svc.cfg.Pricing.HashURL = ""

	require.NoError(t, svc.checkAndUpdatePricing())
	require.Equal(t, 1, remote.fetchJSONs)
	require.Contains(t, svc.pricingData, "dashscope/qwen-plus")

	source, err := os.ReadFile(filepath.Join(dir, "model_pricing.source"))
	require.NoError(t, err)
	require.Equal(t, svc.cfg.Pricing.RemoteURL, strings.TrimSpace(string(source)))
}

func TestResolveFallbackPricingFileFindsBackendResourcesFromRepositoryRoot(t *testing.T) {
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	repoRoot := filepath.Join("..", "..", "..")
	require.NoError(t, os.Chdir(repoRoot))

	svc := &PricingService{cfg: &config.Config{}}
	svc.cfg.Pricing.FallbackFile = "./resources/model-pricing/model_prices_and_context_window.json"

	resolved, err := svc.resolveFallbackPricingFile()
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(filepath.ToSlash(resolved), "backend/resources/model-pricing/model_prices_and_context_window.json"))
}

func TestFallbackPricingFile_ContainsDashScopeMiniMaxAndLlamaModels(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	svc := &PricingService{}
	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	for _, model := range []string{
		"dashscope/qwen-plus",
		"dashscope/qwen3-coder-plus",
		"minimax/MiniMax-M2.5",
		"meta.llama3-3-70b-instruct-v1:0",
	} {
		require.Contains(t, data, model)
	}
}

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_creation_input_token_cost_priority": 0.000005,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_UsesFirstTierWhenTopLevelPricesMissing(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"dashscope/qwen3-coder-plus": {
			"litellm_provider": "dashscope",
			"mode": "chat",
			"tiered_pricing": [
				{
					"input_cost_per_token": 0.000001,
					"input_cost_per_token_priority": 0.000002,
					"output_cost_per_token": 0.000005,
					"output_cost_per_token_priority": 0.00001,
					"cache_creation_input_token_cost": 0.00000125,
					"cache_creation_input_token_cost_priority": 0.0000025,
					"cache_read_input_token_cost": 0.0000001,
					"cache_read_input_token_cost_priority": 0.0000002
				}
			]
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["dashscope/qwen3-coder-plus"]
	require.NotNil(t, pricing)
	require.InDelta(t, 1e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 2e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 10e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 1.25e-6, pricing.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 2.5e-6, pricing.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 1e-7, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 2e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
}

func TestGetModelPricing_ProviderAliases(t *testing.T) {
	qwenPricing := &LiteLLMModelPricing{InputCostPerToken: 1.6e-6}
	llamaPricing := &LiteLLMModelPricing{InputCostPerToken: 7.2e-7}
	minimaxPricing := &LiteLLMModelPricing{InputCostPerToken: 3e-7}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"dashscope/qwen-max":              qwenPricing,
			"meta.llama3-3-70b-instruct-v1:0": llamaPricing,
			"minimax.minimax-m2.5":            minimaxPricing,
		},
	}

	require.Same(t, qwenPricing, svc.GetModelPricing("qwen-max"))
	require.Same(t, llamaPricing, svc.GetModelPricing("llama-3.3-70b-instruct"))
	require.Same(t, minimaxPricing, svc.GetModelPricing("minimax-m2.5"))
}

func TestGetModelPricing_Gpt53CodexSparkUsesGpt51CodexPricing(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	gpt53Pricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": sparkPricing,
			"gpt-5.3":       gpt53Pricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex-spark")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt53CodexFallbackStillUsesGpt52Codex(t *testing.T) {
	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)
}

func TestGetModelPricing_OpenAIFallbackMatchedLoggedAsInfo(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)

	require.True(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "warn"))
}

func TestGetModelPricing_Gpt54UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": &LiteLLMModelPricing{InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_Gpt56UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	tests := []struct {
		model       string
		inputPrice  float64
		outputPrice float64
		cacheWrite  float64
		cacheRead   float64
	}{
		{model: "gpt-5.6", inputPrice: 4e-6, outputPrice: 20e-6, cacheWrite: 5e-6, cacheRead: 0.4e-6},
		{model: "gpt-5.6-sol", inputPrice: 4e-6, outputPrice: 20e-6, cacheWrite: 5e-6, cacheRead: 0.4e-6},
		{model: "gpt-5.6-terra", inputPrice: 2e-6, outputPrice: 12e-6, cacheWrite: 2.5e-6, cacheRead: 0.2e-6},
		{model: "gpt-5.6-luna", inputPrice: 0.2e-6, outputPrice: 1.2e-6, cacheWrite: 0.25e-6, cacheRead: 0.02e-6},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := svc.GetModelPricing(tt.model)
			require.NotNil(t, got)
			require.InDelta(t, tt.inputPrice, got.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.outputPrice, got.OutputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheWrite, got.CacheCreationInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheRead, got.CacheReadInputTokenCost, 1e-12)
			require.Equal(t, 272000, got.LongContextInputTokenThreshold)
			require.True(t, got.SupportsServiceTier)
		})
	}
}

func TestGetModelPricing_Gpt6AstraUsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	got := svc.GetModelPricing("gpt-6-astra-max")
	require.NotNil(t, got)
	require.InDelta(t, 10e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 50e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 12.5e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 20e-6, got.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 100e-6, got.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 25e-6, got.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 2e-6, got.CacheReadInputTokenCostPriority, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
	require.True(t, got.SupportsServiceTier)
}

func TestGetModelPricing_Gpt54MiniUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-mini")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                     2.5e-6,
			"input_cost_per_token_priority":            5e-6,
			"output_cost_per_token":                    15e-6,
			"output_cost_per_token_priority":           30e-6,
			"cache_creation_input_token_cost":          2.5e-6,
			"cache_creation_input_token_cost_priority": 5e-6,
			"cache_read_input_token_cost":              0.25e-6,
			"cache_read_input_token_cost_priority":     0.5e-6,
			"supports_service_tier":                    true,
			"supports_prompt_caching":                  true,
			"litellm_provider":                         "openai",
			"mode":                                     "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 2.5e-6, pricing.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_creation_input_token_cost_priority": 0.000005,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.0000025, pricing.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 0.000005, pricing.CacheCreationInputTokenCostPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}
