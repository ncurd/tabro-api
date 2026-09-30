//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise provider usage through the gateway and into the atomic billing command.
// Expected amounts are calculated from the published rates, independently of the
// pricing resolver, so a wrong model fallback cannot make this test pass.
func TestNewModelsRecordUsageChargesPublishedPrices(t *testing.T) {
	for _, model := range []struct {
		id       string
		platform string
		input    float64
		output   float64
		write    float64
		read     float64
	}{
		{"gpt-6-sol", PlatformOpenAI, 2, 10, 2.5, 0.2},
		{"gpt-6-luna", PlatformOpenAI, 0.1, 0.5, 0.125, 0.01},
		{"claude-opus-5-5", PlatformAnthropic, 4, 20, 5, 0.2},
	} {
		for _, tier := range []string{"standard", "fast"} {
			for _, payment := range []string{"balance", "subscription"} {
				t.Run(model.id+"/"+tier+"/"+payment, func(t *testing.T) {
					usageRepo := &openAIRecordUsageLogRepoStub{}
					billingRepo := &openAIRecordUsageBillingRepoStub{}
					userRepo := &openAIRecordUsageUserRepoStub{}
					subRepo := &openAIRecordUsageSubRepoStub{}
					apiKey := &APIKey{ID: 11, Quota: 100, RateLimit5h: 10}
					var subscription *UserSubscription
					if payment == "subscription" {
						apiKey.Group = &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription}
						subscription = &UserSubscription{ID: 33}
					}
					accountRate := 0.8
					account := &Account{
						ID: 44, Platform: model.platform, Type: AccountTypeAPIKey,
						RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 100.0},
					}
					user := &User{ID: 55}
					quota := &openAIRecordUsageAPIKeyQuotaStub{}
					var err error
					if model.platform == PlatformOpenAI {
						svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
						err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
							Result: &OpenAIForwardResult{
								RequestID: "new-model-usage", Model: model.id, ServiceTier: &tier, Duration: time.Second,
								// OpenAI input includes both cache categories: 100 fresh + 40 writes + 20 reads.
								Usage: OpenAIUsage{InputTokens: 160, OutputTokens: 50, CacheCreationInputTokens: 40, CacheReadInputTokens: 20},
							},
							APIKey: apiKey, User: user, Account: account, Subscription: subscription, APIKeyService: quota,
						})
					} else {
						svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo)
						err = svc.RecordUsage(context.Background(), &RecordUsageInput{
							Result: &ForwardResult{
								RequestID: "new-model-usage", Model: model.id, Duration: time.Second,
								Usage: ClaudeUsage{
									InputTokens: 100, OutputTokens: 50, CacheCreationInputTokens: 40, CacheReadInputTokens: 20,
									CacheCreation5mTokens: 10, CacheCreation1hTokens: 30, Speed: tier,
								},
							},
							APIKey: apiKey, User: user, Account: account, Subscription: subscription, APIKeyService: quota,
						})
					}
					require.NoError(t, err)
					require.Equal(t, 1, billingRepo.calls)
					cmd := billingRepo.lastCmd
					require.NotNil(t, cmd)
					require.Equal(t, model.id, cmd.Model)
					require.Equal(t, 100, cmd.InputTokens)
					require.Equal(t, 40, cmd.CacheCreationTokens)
					require.Equal(t, 20, cmd.CacheReadTokens)
					writeCost := 40 * model.write / 1e6
					if model.platform == PlatformAnthropic {
						writeCost = (10*5 + 30*8) / 1e6
					}
					tierRate := 1.0
					if tier == "fast" {
						tierRate = 2
					}
					total := (100*model.input/1e6 + 50*model.output/1e6 + writeCost + 20*model.read/1e6) * tierRate
					actual := total * 1.1 // Test gateway's configured customer multiplier.
					require.InDelta(t, writeCost*tierRate, cmd.CacheCreationCost, 1e-12)
					require.InDelta(t, total, cmd.TotalCost, 1e-12)
					require.InDelta(t, actual, cmd.ActualCost, 1e-12)
					require.InDelta(t, actual, cmd.APIKeyQuotaCost, 1e-12)
					require.InDelta(t, actual, cmd.APIKeyRateLimitCost, 1e-12)
					require.InDelta(t, total*accountRate, cmd.AccountQuotaCost, 1e-12)
					if payment == "subscription" {
						require.InDelta(t, total, cmd.SubscriptionCost, 1e-12)
						require.Zero(t, cmd.BalanceCost)
					} else {
						require.InDelta(t, actual, cmd.BalanceCost, 1e-12)
						require.Zero(t, cmd.SubscriptionCost)
					}
					require.NotNil(t, usageRepo.lastLog)
					require.InDelta(t, actual, usageRepo.lastLog.ActualCost, 1e-12)
					// The atomic repository owns the debit; legacy deductors must not run as well.
					require.Zero(t, userRepo.deductCalls)
					require.Zero(t, subRepo.incrementCalls)
					require.Zero(t, quota.quotaCalls)
				})
			}
		}
	}
}

func TestNewModelsPricingPageUsesBillingPrices(t *testing.T) {
	resolver := newResolverWithChannel(t, nil)
	resolver.billingService = NewBillingService(nil, nil)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{groups: []Group{
			{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1.5},
			{ID: 2, Platform: PlatformAnthropic, RateMultiplier: 1.5},
		}},
		modelPricingAccountsStub{}, resolver,
	)
	result, err := svc.ListAvailablePricing(context.Background(), 55)
	require.NoError(t, err)
	expected := map[string][4]float64{
		"gpt-6-sol":       {2, 10, 2.5, 0.2},
		"gpt-6-luna":      {0.1, 0.5, 0.125, 0.01},
		"claude-opus-5-5": {4, 20, 5, 0.2},
	}
	for _, group := range result.Groups {
		for _, model := range group.Models {
			prices, ok := expected[model.ID]
			if !ok {
				continue
			}
			require.True(t, model.PricingAvailable, model.ID)
			require.InDelta(t, prices[0]*1.5, model.InputPricePerMillion, 1e-12)
			require.InDelta(t, prices[1]*1.5, model.OutputPricePerMillion, 1e-12)
			require.InDelta(t, prices[2]*1.5, model.CacheWritePricePerMillion, 1e-12)
			require.InDelta(t, prices[3]*1.5, model.CacheReadPricePerMillion, 1e-12)
			delete(expected, model.ID)
		}
	}
	require.Empty(t, expected, "every new model must appear in its platform's price list")
}
