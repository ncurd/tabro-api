package service

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func image26CatalogPricing(t *testing.T) (*PricingService, map[string]map[string]json.RawMessage) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	service := &PricingService{}
	service.pricingData, err = service.parsePricingData(body)
	require.NoError(t, err)
	var raw map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	return service, raw
}

func TestGPTImage26CatalogUsesPublishedStandardRatesOnly(t *testing.T) {
	pricing, raw := image26CatalogPricing(t)
	var registered []string
	for model := range raw {
		if strings.HasPrefix(model, "gpt-image-2.6") {
			registered = append(registered, model)
		}
	}
	require.ElementsMatch(t, []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"}, registered)
	for _, model := range registered {
		price := pricing.GetModelPricing(model)
		require.NotNil(t, price)
		require.InDelta(t, 5e-6, price.InputCostPerToken, 1e-12)
		require.InDelta(t, 8e-6, price.InputCostPerImageToken, 1e-12)
		require.InDelta(t, 1.25e-6, price.CacheReadInputTokenCost, 1e-12)
		require.InDelta(t, 2e-6, price.CacheReadInputImageTokenCost, 1e-12)
		require.InDelta(t, 30e-6, price.OutputCostPerImageToken, 1e-12)
		require.Zero(t, price.OutputCostPerToken)
		require.Zero(t, price.OutputCostPerImage)
		for field := range raw[model] {
			require.NotContains(t, field, "batch")
		}
	}
	for _, unpublished := range []string{"gpt-image-2.6", "gpt-image-2.6-sunburst-2026-11-08", "gpt-image-2.6-unpublished"} {
		require.Nil(t, pricing.GetModelPricing(unpublished), unpublished)
	}
}

func TestGPTImage26BillingSeparatesTextImageAndCachedModalities(t *testing.T) {
	catalog, _ := image26CatalogPricing(t)
	for _, source := range []struct {
		name    string
		pricing *PricingService
	}{
		{"bundled catalog", catalog},
		{"stale remote catalog", &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-5.1": {InputCostPerToken: 1}}}},
		{"billing fallback", nil},
	} {
		for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
			t.Run(source.name+"/"+model, func(t *testing.T) {
				billing := NewBillingService(&config.Config{}, source.pricing)
				cost, err := billing.CalculateCost(model, UsageTokens{
					InputTokens: 1500, ImageInputTokens: 1000,
					CacheReadTokens: 300, CacheReadImageTokens: 200,
					OutputTokens: 1000, ImageOutputTokens: 1000,
				}, 1.5)
				require.NoError(t, err)
				// 500 text + 1000 image uncached inputs; 100 text + 200 image cached.
				require.InDelta(t, 0.0105, cost.InputCost, 1e-12)
				require.InDelta(t, 0.000525, cost.CacheReadCost, 1e-12)
				require.InDelta(t, 0.03, cost.ImageOutputCost, 1e-12)
				require.Zero(t, cost.OutputCost)
				require.InDelta(t, 0.041025, cost.TotalCost, 1e-12)
				require.InDelta(t, 0.0615375, cost.ActualCost, 1e-12)
			})
		}
	}
}

func TestGPTImage26ChannelPricesOverrideAllInputModalities(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	tokens := UsageTokens{InputTokens: 1500, ImageInputTokens: 1000, CacheReadTokens: 300, CacheReadImageTokens: 200, OutputTokens: 1000, ImageOutputTokens: 1000}
	input, cached, imageOutput := 1e-6, 0.1e-6, 10e-6
	channel := &ChannelModelPricing{InputPrice: &input, CacheReadPrice: &cached, ImageOutputPrice: &imageOutput}
	cost, err := billing.calculateCostInternal("gpt-image-2.6-sunburst", tokens, 1, "", channel)
	require.NoError(t, err)
	require.InDelta(t, 0.0015, cost.InputCost, 1e-12)
	require.InDelta(t, 0.00003, cost.CacheReadCost, 1e-12)
	require.InDelta(t, 0.01, cost.ImageOutputCost, 1e-12)

	base, err := billing.GetModelPricing("gpt-image-2.6-sunburst")
	require.NoError(t, err)
	require.InDelta(t, 8e-6, base.ImageInputPricePerToken, 1e-12, "channel prices must not mutate shared official fallback")
	require.InDelta(t, 2e-6, base.CacheReadImagePricePerToken, 1e-12)
	resolved := &ResolvedPricing{BasePricing: base, Mode: BillingModeToken}
	resolver := NewModelPricingResolver(nil, billing)
	resolver.applyTokenOverrides(channel, resolved)
	cost, err = billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "gpt-image-2.6-sunburst", Tokens: tokens, RateMultiplier: 1, Resolver: resolver, Resolved: resolved})
	require.NoError(t, err)
	require.InDelta(t, 0.01153, cost.TotalCost, 1e-12)

	zero := 0.0
	free := &ResolvedPricing{BasePricing: base, Mode: BillingModeToken}
	resolver.applyTokenOverrides(&ChannelModelPricing{InputPrice: &zero, CacheReadPrice: &zero, ImageOutputPrice: &zero}, free)
	cost, err = billing.CalculateCostUnified(CostInput{Model: "gpt-image-2.6-sunburst", Tokens: tokens, Resolver: resolver, Resolved: free})
	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)

	interval := intervalToModelPricing(&PricingInterval{InputPrice: &input, CacheReadPrice: &cached}, base, false)
	cost = billing.computeTokenBreakdown(interval, tokens, 1, "", false)
	require.InDelta(t, 0.0015, cost.InputCost, 1e-12)
	require.InDelta(t, 0.00003, cost.CacheReadCost, 1e-12)
}

func TestGPTImage26RequiresExplicitPerImagePrice(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		missing := billing.CalculateImageCost(model, "1K", 1, nil, 1)
		require.True(t, missing.PricingFailed)
		require.Zero(t, missing.TotalCost)
		require.Zero(t, billing.getDefaultImagePrice(model, "1K"))
		price := 0.25
		configured := billing.CalculateImageCost(model, "1K", 2, &ImagePriceConfig{Price1K: &price}, 1.5)
		require.False(t, configured.PricingFailed)
		require.InDelta(t, 0.5, configured.TotalCost, 1e-12)
		require.InDelta(t, 0.75, configured.ActualCost, 1e-12)
		wrongTier := billing.CalculateImageCost(model, "2K", 1, &ImagePriceConfig{Price1K: &price}, 1)
		require.True(t, wrongTier.PricingFailed)
		zero := 0.0
		free := billing.CalculateImageCost(model, "1K", 1, &ImagePriceConfig{Price1K: &zero}, 1)
		require.False(t, free.PricingFailed)
		require.Zero(t, free.TotalCost)
	}
}

func TestGPTImage26TieredCatalogRetainsImageInputRates(t *testing.T) {
	pricing := &PricingService{}
	entries, err := pricing.parsePricingData([]byte(`{"gpt-image-2.6-flare":{"tiered_pricing":[{"input_cost_per_token":0.000005,"input_cost_per_image_token":0.000008,"cache_read_input_token_cost":0.00000125,"cache_read_input_image_token_cost":0.000002,"output_cost_per_image_token":0.00003}]}}`))
	require.NoError(t, err)
	require.InDelta(t, 8e-6, entries["gpt-image-2.6-flare"].InputCostPerImageToken, 1e-12)
	require.InDelta(t, 2e-6, entries["gpt-image-2.6-flare"].CacheReadInputImageTokenCost, 1e-12)
}

func TestGPTImage26PricePageExposesAllModalitiesWithEffectiveRate(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		pricing, err := billing.GetModelPricing(model)
		require.NoError(t, err)
		page := &ModelPricingPageService{resolver: modelPricingResolverStub{byModel: map[string]*ResolvedPricing{
			model: {Mode: BillingModeToken, BasePricing: pricing},
		}}}
		result := page.resolveModelPricing(context.Background(), 1, model, 1.5)
		require.True(t, result.PricingAvailable)
		require.InDelta(t, 7.5, result.InputPricePerMillion, 1e-12)
		require.NotNil(t, result.ImageInputPricePerMillion)
		require.InDelta(t, 12, *result.ImageInputPricePerMillion, 1e-12)
		require.InDelta(t, 1.875, result.CacheReadPricePerMillion, 1e-12)
		require.NotNil(t, result.CachedImageInputPricePerMillion)
		require.InDelta(t, 3, *result.CachedImageInputPricePerMillion, 1e-12)
		require.NotNil(t, result.ImageOutputPricePerMillion)
		require.InDelta(t, 45, *result.ImageOutputPricePerMillion, 1e-12)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"image_input_price_per_million":12`)
		require.Contains(t, string(encoded), `"cached_image_input_price_per_million":3`)
		require.True(t, finiteConvertedModelPricing(result))
		infinity, imagePrice := math.Inf(1), 12.0
		result.ImageInputPricePerMillion = &infinity
		require.False(t, finiteConvertedModelPricing(result))
		result.ImageInputPricePerMillion = &imagePrice
		result.CachedImageInputPricePerMillion = &infinity
		require.False(t, finiteConvertedModelPricing(result))
	}
}

func TestGPTImage26PricePageDistinguishesFreeImageInputsFromMissingRates(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	base, err := billing.GetModelPricing("gpt-image-2.6-flare")
	require.NoError(t, err)
	zero := 0.0
	resolved := &ResolvedPricing{Mode: BillingModeToken, BasePricing: base}
	resolver := NewModelPricingResolver(nil, billing)
	resolver.applyTokenOverrides(&ChannelModelPricing{InputPrice: &zero, CacheReadPrice: &zero}, resolved)
	page := &ModelPricingPageService{resolver: modelPricingResolverStub{byModel: map[string]*ResolvedPricing{"gpt-image-2.6-flare": resolved}}}
	result := page.resolveModelPricing(context.Background(), 1, "gpt-image-2.6-flare", 1)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"image_input_price_per_million":0`)
	require.Contains(t, string(encoded), `"cached_image_input_price_per_million":0`)

	// Generic text-input overrides must not make a text model advertise image rates.
	input, cached := 2e-6, 0.2e-6
	text := &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: 1e-6}}
	resolver.applyTokenOverrides(&ChannelModelPricing{InputPrice: &input, CacheReadPrice: &cached}, text)
	page.resolver = modelPricingResolverStub{byModel: map[string]*ResolvedPricing{"text-model": text}}
	result = page.resolveModelPricing(context.Background(), 1, "text-model", 1)
	encoded, err = json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "image_input_price_per_million")
	require.NotContains(t, string(encoded), "cached_image_input_price_per_million")
}

func TestGPTImage26ExplicitFreeImageRatesDoNotInheritTextPrices(t *testing.T) {
	pricing := &PricingService{}
	var err error
	pricing.pricingData, err = pricing.parsePricingData([]byte(`{"gpt-image-2.6-flare":{"input_cost_per_token":0.000005,"input_cost_per_image_token":0,"cache_read_input_token_cost":0.00000125,"cache_read_input_image_token_cost":0,"output_cost_per_image_token":0.00003}}`))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, pricing)
	cost, err := billing.CalculateCost("gpt-image-2.6-flare", UsageTokens{
		InputTokens: 1500, ImageInputTokens: 1000, CacheReadTokens: 300, CacheReadImageTokens: 200,
		OutputTokens: 1000, ImageOutputTokens: 1000,
	}, 1)
	require.NoError(t, err)
	require.InDelta(t, 0.0025, cost.InputCost, 1e-12, "only 500 uncached text tokens are billed")
	require.InDelta(t, 0.000125, cost.CacheReadCost, 1e-12, "only 100 cached text tokens are billed")
	require.InDelta(t, 0.032625, cost.TotalCost, 1e-12)
}

func TestGPTImage26ExplicitFreeImageOutputDoesNotInheritTextPrice(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	textOutput, freeImage := 2e-6, 0.0
	channel := &ChannelModelPricing{OutputPrice: &textOutput, ImageOutputPrice: &freeImage}
	tokens := UsageTokens{OutputTokens: 1500, ImageOutputTokens: 1000}
	cost, err := billing.calculateCostInternal("gpt-image-2.6-flare", tokens, 1, "", channel)
	require.NoError(t, err)
	require.InDelta(t, 0.001, cost.OutputCost, 1e-12)
	require.Zero(t, cost.ImageOutputCost)
	require.InDelta(t, 0.001, cost.TotalCost, 1e-12)

	base, err := billing.GetModelPricing("gpt-image-2.6-flare")
	require.NoError(t, err)
	resolved := &ResolvedPricing{Mode: BillingModeToken, BasePricing: base}
	resolver := NewModelPricingResolver(nil, billing)
	resolver.applyTokenOverrides(channel, resolved)
	cost, err = billing.CalculateCostUnified(CostInput{Model: "gpt-image-2.6-flare", Tokens: tokens, Resolved: resolved, Resolver: resolver})
	require.NoError(t, err)
	require.InDelta(t, 0.001, cost.TotalCost, 1e-12)
	page := &ModelPricingPageService{resolver: modelPricingResolverStub{byModel: map[string]*ResolvedPricing{"gpt-image-2.6-flare": resolved}}}
	result := page.resolveModelPricing(context.Background(), 1, "gpt-image-2.6-flare", 1)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"image_output_price_per_million":0`)
	// Unconfigured legacy image-output prices retain their existing generic fallback.
	legacy := billing.computeTokenBreakdown(&ModelPricing{OutputPricePerToken: textOutput}, tokens, 1, "", false)
	require.InDelta(t, 0.003, legacy.TotalCost, 1e-12)
}

func TestGPTImage26AccountStatsKeepModalitiesDisjoint(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	modelFile := tryModelFilePricing(billing, "gpt-image-2.6-flare", UsageTokens{
		InputTokens: 1500, ImageInputTokens: 1000, CacheReadTokens: 300, CacheReadImageTokens: 200,
		OutputTokens: 1000, ImageOutputTokens: 1000,
	})
	require.NotNil(t, modelFile)
	require.InDelta(t, 0.041025, *modelFile, 1e-12)

	input, output, imageOutput, cached := 0.001, 0.002, 0.01, 0.0003
	price := ChannelModelPricing{Models: []string{"gpt-image-2.6-flare"}, Platform: PlatformOpenAI,
		InputPrice: &input, OutputPrice: &output, ImageOutputPrice: &imageOutput, CacheReadPrice: &cached}
	channel := &Channel{AccountStatsPricingRules: []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{price}}}}
	tokens := UsageTokens{InputTokens: 100, ImageInputTokens: 60, OutputTokens: 50, ImageOutputTokens: 10, CacheReadTokens: 40, CacheReadImageTokens: 30}
	cost := tryCustomRules(channel, 1, 1, PlatformOpenAI, "gpt-image-2.6-flare", tokens, 1)
	require.NotNil(t, cost)
	// Generic input/cache rules cover each aggregate once. Only 40 output tokens
	// use the text-output rate; the other 10 use the image-output rate.
	require.InDelta(t, 0.292, *cost, 1e-12)

	intervalInput, intervalOutput, intervalCached := 0.0005, 0.001, 0.0001
	channel.AccountStatsPricingRules[0].Pricing[0].Intervals = []PricingInterval{{
		MinTokens: 0, InputPrice: &intervalInput, OutputPrice: &intervalOutput, CacheReadPrice: &intervalCached,
	}}
	cost = tryCustomRules(channel, 1, 1, PlatformOpenAI, "gpt-image-2.6-flare", tokens, 1)
	require.NotNil(t, cost)
	require.InDelta(t, 0.194, *cost, 1e-12, "interval pricing retains the separately configured image-output rate")

	channel.AccountStatsPricingRules[0].Pricing[0].Intervals = nil
	channel.AccountStatsPricingRules[0].Pricing[0].ImageOutputPrice = nil
	cost = tryCustomRules(channel, 1, 1, PlatformOpenAI, "gpt-image-2.6-flare", tokens, 1)
	require.InDelta(t, 0.212, *cost, 1e-12, "without a separate image rate, the generic output rate covers all output once")
	zero := 0.0
	channel.AccountStatsPricingRules[0].Pricing[0].ImageOutputPrice = &zero
	cost = tryCustomRules(channel, 1, 1, PlatformOpenAI, "gpt-image-2.6-flare", tokens, 1)
	require.InDelta(t, 0.192, *cost, 1e-12, "an explicit free image-output rate must remain free")
}
