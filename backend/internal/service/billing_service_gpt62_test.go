//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestBillingGPT62OfficialRatesAcrossSourcesTiersAndContext(t *testing.T) {
	for source, billing := range latestModelBillingServices(t) {
		t.Run(source, func(t *testing.T) {
			for _, model := range []struct {
				id                                   string
				input, output, cacheWrite, cacheRead float64
			}{
				{"gpt-6.2-astra", 8e-6, 40e-6, 10e-6, .4e-6},
				{"gpt-6.2-sol", 2e-6, 10e-6, 2.5e-6, .1e-6},
			} {
				for _, input := range []int{249999, 250000, 250001} {
					inputMultiplier, outputMultiplier := 1.0, 1.0
					if input+20000+2000 > 272000 {
						inputMultiplier, outputMultiplier = 2, 1.5
					}
					for _, tier := range []struct {
						name       string
						multiplier float64
					}{{"default", 1}, {"fast", 2}, {"priority", 2}, {"ultrafast", openAIGPT62UltrafastMultiplierAt(time.Now())}} {
						tokens := UsageTokens{InputTokens: input, OutputTokens: 1000, CacheCreationTokens: 20000, CacheReadTokens: 2000,
							CacheCreation30mTokens: 12500, CacheCreation12hTokens: 7500}
						legacy, err := billing.CalculateCostWithServiceTier(model.id, tokens, 1.7, tier.name)
						require.NoError(t, err)
						unified, err := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: model.id, Tokens: tokens,
							RateMultiplier: 1.7, ServiceTier: tier.name, Resolver: NewModelPricingResolver(nil, billing)})
						require.NoError(t, err)
						for _, cost := range []*CostBreakdown{legacy, unified} {
							require.InDelta(t, float64(input)*model.input*inputMultiplier*tier.multiplier, cost.InputCost, 1e-12, model.id+"/"+tier.name)
							require.InDelta(t, 1000*model.output*outputMultiplier*tier.multiplier, cost.OutputCost, 1e-12)
							require.InDelta(t, (12500*model.cacheWrite+7500*model.cacheWrite*2)*inputMultiplier*tier.multiplier, cost.CacheCreationCost, 1e-12)
							require.InDelta(t, 2000*model.cacheRead*inputMultiplier*tier.multiplier, cost.CacheReadCost, 1e-12)
							require.InDelta(t, cost.TotalCost*1.7, cost.ActualCost, 1e-12)
						}
					}
				}
			}
		})
	}
}

func TestBillingGPT62CacheTTLUsesAggregateBoundsAndDefaultForRemainder(t *testing.T) {
	billing := newTestBillingService()
	for _, tc := range []struct {
		name   string
		tokens UsageTokens
		cost   float64
	}{
		{"mixed_with_unclassified_writes", UsageTokens{CacheCreationTokens: 100, CacheCreation30mTokens: 20, CacheCreation12hTokens: 30}, 70*10e-6 + 30*20e-6},
		{"split_cannot_expand_total", UsageTokens{CacheCreationTokens: 100, CacheCreation30mTokens: 200, CacheCreation12hTokens: 200}, 100 * 20e-6},
		{"missing_aggregate", UsageTokens{CacheCreation30mTokens: 10, CacheCreation12hTokens: 20}, 10*10e-6 + 20*20e-6},
		{"negative_detail", UsageTokens{CacheCreationTokens: 100, CacheCreation30mTokens: 50, CacheCreation12hTokens: -20}, 100 * 10e-6},
		{"no_details", UsageTokens{CacheCreationTokens: 100}, 100 * 10e-6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost, err := billing.CalculateCost("gpt-6.2-astra", tc.tokens, 1)
			require.NoError(t, err)
			require.InDelta(t, tc.cost, cost.CacheCreationCost, 1e-12)
		})
	}
}

func TestGPT62UltrafastPromotionBoundaryAndFrozenPricing(t *testing.T) {
	cutoff := time.Date(2027, time.March, 1, 8, 0, 0, 0, time.UTC)
	require.Equal(t, 3.0, openAIGPT62UltrafastMultiplierAt(cutoff.Add(-time.Nanosecond)))
	require.Equal(t, 6.0, openAIGPT62UltrafastMultiplierAt(cutoff))
	billing := newTestBillingService()
	price, err := billing.GetModelPricing("gpt-6.2-astra")
	require.NoError(t, err)
	price.UltrafastMultiplier = 3 // retained in a quote issued during the promotion
	require.Equal(t, 3.0, billing.applyModelSpecificPricingPolicy("gpt-6.2-astra", price).UltrafastMultiplier)
	price.FlatTokenPrices = true
	cost := billing.computeTokenBreakdown(price, UsageTokens{InputTokens: 300000}, 1, "ultrafast", true)
	require.InDelta(t, 300000*8e-6, cost.InputCost, 1e-12, "manual prices bypass automatic tier and long-context multipliers")
}

func TestGPT62CreditReservationBoundsUltrafastProjectDefaults(t *testing.T) {
	billing := newTestBillingService()
	price := NewModelPricingResolver(nil, billing).Resolve(context.Background(), PricingInput{Model: "gpt-6.2-astra"})
	maximum, err := maximumGatewayTokenCost(billing, price, "gpt-6.2-astra", map[string]bc.Decimal{
		"input_tokens": "300000", "output_tokens": "1000", "cache_read_tokens": "0",
		"cache_write_tokens": "0", "cache_write_5m_tokens": "0", "cache_write_1h_tokens": "0", "image_output_tokens": "0",
	}, "default", 1)
	require.NoError(t, err)
	actual, err := billing.CalculateCostWithServiceTier("gpt-6.2-astra", UsageTokens{InputTokens: 300000, OutputTokens: 1000}, 1, "ultrafast")
	require.NoError(t, err)
	require.InDelta(t, actual.ActualCost, maximum, 1e-12)
}
