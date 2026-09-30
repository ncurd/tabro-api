//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func latestModelBillingServices(t *testing.T) map[string]*BillingService {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	bundled := &PricingService{}
	bundled.pricingData, err = bundled.parsePricingData(body)
	require.NoError(t, err)
	return map[string]*BillingService{
		"billing_fallback": NewBillingService(&config.Config{}, nil),
		"dynamic_fallback": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"gpt-6":         {InputCostPerToken: 999},
			"claude-opus-5": {InputCostPerToken: 999},
		}}),
		"bundled": NewBillingService(&config.Config{}, bundled),
	}
}

func TestBillingLatestGPT6Models_AllPricingSourcesAndPaths(t *testing.T) {
	for source, svc := range latestModelBillingServices(t) {
		t.Run(source, func(t *testing.T) {
			for _, model := range []struct {
				name       string
				input      float64
				output     float64
				cacheWrite float64
				cacheRead  float64
			}{
				{"gpt-6.1-sol", 2e-6, 10e-6, 2.5e-6, 0.1e-6},
				{"gpt-6-sol", 2e-6, 10e-6, 2.5e-6, 0.2e-6},
				{"gpt-6-luna", 0.1e-6, 0.5e-6, 0.125e-6, 0.01e-6},
			} {
				t.Run(model.name, func(t *testing.T) {
					for _, ctx := range []struct {
						name          string
						cacheRead     int
						inputMultiple float64
						outMultiple   float64
					}{
						{"below_threshold", 1999, 1, 1},
						{"at_threshold", 2000, 1, 1},
						{"above_threshold", 2001, 2, 1.5},
					} {
						t.Run(ctx.name, func(t *testing.T) {
							tokens := UsageTokens{InputTokens: 250000, OutputTokens: 10000, CacheCreationTokens: 20000, CacheReadTokens: ctx.cacheRead}
							for _, tier := range []struct {
								name       string
								multiplier float64
							}{
								{"default", 1}, {"priority", 2}, {"fast", 2}, {"flex", 0.5},
							} {
								t.Run(tier.name, func(t *testing.T) {
									for _, path := range []string{"legacy", "unified"} {
										t.Run(path, func(t *testing.T) {
											var cost *CostBreakdown
											var err error
											if path == "legacy" {
												cost, err = svc.CalculateCostWithServiceTier(model.name, tokens, 1.7, tier.name)
											} else {
												cost, err = svc.CalculateCostUnified(CostInput{
													Ctx: context.Background(), Model: model.name, Tokens: tokens,
													RateMultiplier: 1.7, ServiceTier: tier.name, Resolver: NewModelPricingResolver(nil, svc),
												})
											}
											require.NoError(t, err)
											expectedInput := 250000 * model.input * ctx.inputMultiple * tier.multiplier
											expectedOutput := 10000 * model.output * ctx.outMultiple * tier.multiplier
											expectedWrite := 20000 * model.cacheWrite * ctx.inputMultiple * tier.multiplier
											expectedRead := float64(ctx.cacheRead) * model.cacheRead * ctx.inputMultiple * tier.multiplier
											require.InDelta(t, expectedInput, cost.InputCost, 1e-12)
											require.InDelta(t, expectedOutput, cost.OutputCost, 1e-12)
											require.InDelta(t, expectedWrite, cost.CacheCreationCost, 1e-12)
											require.InDelta(t, expectedRead, cost.CacheReadCost, 1e-12)
											expectedTotal := expectedInput + expectedOutput + expectedWrite + expectedRead
											require.InDelta(t, expectedTotal, cost.TotalCost, 1e-12)
											require.InDelta(t, expectedTotal*1.7, cost.ActualCost, 1e-12)
										})
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

func TestBillingOpus55_AllPricingSourcesAndPaths(t *testing.T) {
	for source, svc := range latestModelBillingServices(t) {
		t.Run(source, func(t *testing.T) {
			for _, model := range []string{"claude-opus-5-5", "claude-opus-5.5", "anthropic.claude-opus-5-5-v1:0"} {
				t.Run(model, func(t *testing.T) {
					for _, tier := range []struct {
						name       string
						multiplier float64
					}{
						{"default", 1}, {billingServiceTierAnthropicFast, 2},
					} {
						t.Run(tier.name, func(t *testing.T) {
							// Above both 200K and 272K; Opus 5.5 must retain standard context prices.
							tokens := UsageTokens{
								InputTokens: 300000, OutputTokens: 1000, CacheReadTokens: 50000,
								CacheCreationTokens: 5000, CacheCreation5mTokens: 2000, CacheCreation1hTokens: 3000,
							}
							for _, path := range []string{"legacy", "unified"} {
								t.Run(path, func(t *testing.T) {
									var cost *CostBreakdown
									var err error
									if path == "legacy" {
										cost, err = svc.CalculateCostWithServiceTier(model, tokens, 1.7, tier.name)
									} else {
										cost, err = svc.CalculateCostUnified(CostInput{
											Ctx: context.Background(), Model: model, Tokens: tokens,
											RateMultiplier: 1.7, ServiceTier: tier.name, Resolver: NewModelPricingResolver(nil, svc),
										})
									}
									require.NoError(t, err)
									require.InDelta(t, 1.2*tier.multiplier, cost.InputCost, 1e-12)
									require.InDelta(t, 0.02*tier.multiplier, cost.OutputCost, 1e-12)
									require.InDelta(t, 0.034*tier.multiplier, cost.CacheCreationCost, 1e-12)
									require.InDelta(t, 0.01*tier.multiplier, cost.CacheReadCost, 1e-12)
									require.InDelta(t, 1.264*tier.multiplier, cost.TotalCost, 1e-12)
									require.InDelta(t, 1.264*tier.multiplier*1.7, cost.ActualCost, 1e-12)
								})
							}
						})
					}
				})
			}
			cost, err := svc.CalculateCost("claude-opus-5-5", UsageTokens{CacheCreationTokens: 5000}, 1)
			require.NoError(t, err)
			require.InDelta(t, 0.025, cost.CacheCreationCost, 1e-12, "missing cache TTL details should bill at 5-minute rate")
		})
	}
}
