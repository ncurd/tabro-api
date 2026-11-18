//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rates are the published Singapore/International USD prices, not a configured
// test price: https://www.alibabacloud.com/help/en/model-studio/model-pricing.
func TestQwen4MaxOfficialDefaultPricingAcrossSources(t *testing.T) {
	for source, billing := range latestModelBillingServices(t) {
		t.Run(source, func(t *testing.T) {
			if source == "bundled" {
				require.Contains(t, billing.pricingService.pricingData, "dashscope/qwen4-max", "the release resource must contain actual prices")
				bundled := billing.pricingService.pricingData["dashscope/qwen4-max"]
				require.Same(t, bundled, billing.pricingService.GetModelPricing("qwen4-max"))
				require.Equal(t, 991808, bundled.MaxInputTokens)
				require.Equal(t, 262144, bundled.MaxOutputTokens)
			}
			for _, model := range []string{"qwen4-max", "dashscope/qwen4-max", "aliyun/qwen4-max", "models/qwen4-max"} {
				pricing, err := billing.GetModelPricing(model)
				require.NoError(t, err, model)
				require.InDelta(t, 1.5e-6, pricing.InputPricePerToken, 1e-15)
				require.InDelta(t, 4.5e-6, pricing.OutputPricePerToken, 1e-15)
				require.InDelta(t, 0.075e-6, pricing.CacheReadPricePerToken, 1e-15)
				require.InDelta(t, 1.875e-6, pricing.CacheCreationPricePerToken, 1e-15)
				require.InDelta(t, 1.875e-6, pricing.CacheCreation5mPrice, 1e-15)
				require.InDelta(t, 3e-6, pricing.CacheCreation1hPrice, 1e-15)
				require.True(t, pricing.SupportsCacheBreakdown)
				require.Zero(t, pricing.LongContextInputThreshold)
			}
			// The whole 1M input range is one price band. In particular, Qwen4
			// does not inherit the older Qwen tiers or OpenAI's 272K surcharge.
			for _, input := range []int{32000, 32001, 128001, 272001, 900000} {
				tokens := UsageTokens{InputTokens: input, OutputTokens: 100, CacheReadTokens: 2000,
					CacheCreationTokens: 3000, CacheCreation5mTokens: 1000, CacheCreation1hTokens: 2000}
				legacy, err := billing.CalculateCost("qwen4-max", tokens, 1.7)
				require.NoError(t, err)
				unified, err := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "qwen4-max", Tokens: tokens,
					RateMultiplier: 1.7, Resolver: NewModelPricingResolver(nil, billing)})
				require.NoError(t, err)
				for _, cost := range []*CostBreakdown{legacy, unified} {
					require.InDelta(t, float64(input)*1.5e-6, cost.InputCost, 1e-12)
					require.InDelta(t, 100*4.5e-6, cost.OutputCost, 1e-12)
					require.InDelta(t, 1000*1.875e-6+2000*3e-6, cost.CacheCreationCost, 1e-12)
					require.InDelta(t, 2000*0.075e-6, cost.CacheReadCost, 1e-12)
					require.InDelta(t, (float64(input)*1.5e-6+100*4.5e-6+1000*1.875e-6+2000*3e-6+2000*0.075e-6)*1.7, cost.ActualCost, 1e-12)
				}
			}
			unclassified, err := billing.CalculateCost("qwen4-max", UsageTokens{CacheCreationTokens: 1000}, 1)
			require.NoError(t, err)
			require.InDelta(t, 1000*1.875e-6, unclassified.CacheCreationCost, 1e-12)
		})
	}
}

func TestQwen4MaxDefaultPricingDoesNotInferPreviewOrSnapshots(t *testing.T) {
	for source, billing := range latestModelBillingServices(t) {
		t.Run(source, func(t *testing.T) {
			for _, model := range []string{"qwen4-max-preview", "qwen4.0-max-preview", "qwen4-max-20261118", "dashscope/qwen4-max-preview", "my-qwen4-max", "custom/qwen4-max", "openrouter/qwen/qwen4-max"} {
				_, err := billing.GetModelPricing(model)
				require.Error(t, err, model)
			}
		})
	}
}

func TestQwen4AndAnthropicCacheTTLUsesAggregateAndShortRateForRemainder(t *testing.T) {
	billing := newTestBillingService()
	for _, model := range []struct {
		id          string
		short, long float64
	}{
		{id: "qwen4-max", short: 1.875e-6, long: 3e-6},
		{id: "claude-opus-5-5", short: 5e-6, long: 8e-6},
	} {
		for _, test := range []struct {
			name        string
			tokens      UsageTokens
			short, long int
		}{
			{name: "complete", tokens: UsageTokens{CacheCreationTokens: 300, CacheCreation5mTokens: 100, CacheCreation1hTokens: 200}, short: 100, long: 200},
			{name: "partial", tokens: UsageTokens{CacheCreationTokens: 350, CacheCreation5mTokens: 100, CacheCreation1hTokens: 200}, short: 150, long: 200},
			{name: "aggregate_only", tokens: UsageTokens{CacheCreationTokens: 350}, short: 350},
			{name: "details_only", tokens: UsageTokens{CacheCreation5mTokens: 100, CacheCreation1hTokens: 200}, short: 100, long: 200},
			{name: "details_cannot_expand_total", tokens: UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 200, CacheCreation1hTokens: 200}, long: 100},
			{name: "negative_detail", tokens: UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 50, CacheCreation1hTokens: -20}, short: 100},
			{name: "negative_aggregate", tokens: UsageTokens{CacheCreationTokens: -20, CacheCreation5mTokens: 10, CacheCreation1hTokens: 20}, short: 10, long: 20},
			{name: "all_negative", tokens: UsageTokens{CacheCreationTokens: -20, CacheCreation5mTokens: -10, CacheCreation1hTokens: -20}},
		} {
			t.Run(model.id+"/"+test.name, func(t *testing.T) {
				cost, err := billing.CalculateCost(model.id, test.tokens, 1)
				require.NoError(t, err)
				require.InDelta(t, float64(test.short)*model.short+float64(test.long)*model.long, cost.CacheCreationCost, 1e-12)
			})
		}
	}
}
