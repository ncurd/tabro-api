package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

type modelPricingPolicyStub struct {
	policy OIDCBillingPolicy
	err    error
	calls  int
}

func (s *modelPricingPolicyStub) GetOIDCBillingPolicy(context.Context) (OIDCBillingPolicy, error) {
	s.calls++
	return s.policy, s.err
}

func oidcModelPricingFixture(policy ModelPricingBillingPolicyProvider, rates map[int64]float64) *ModelPricingPageService {
	videoPrice, videoTierPrice := .12, .25
	return NewModelPricingPageService(
		modelPricingGroupsStub{groups: []Group{{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1.5}}, rates: rates},
		modelPricingAccountsStub{byGroup: map[int64][]Account{1: {{Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-credit": "gpt-credit", "wan3.0-video": "wan3.0-video", "missing": "missing"},
		}}}}},
		modelPricingResolverStub{byModel: map[string]*ResolvedPricing{
			"gpt-credit": {Mode: BillingModeToken, BasePricing: &ModelPricing{
				InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6,
				CacheCreationPricePerToken: 3e-6, CacheReadPricePerToken: 4e-6,
				InputPricePerTokenPriority: 5e-6, OutputPricePerTokenPriority: 6e-6,
				CacheReadPricePerTokenPriority: 7e-6, ImageOutputPricePerToken: 8e-6,
			}},
			"wan3.0-video": {Mode: BillingModeVideo, DefaultPriceConfigured: true,
				DefaultPerRequestPrice: videoPrice, RequestTiers: []PricingInterval{{TierLabel: "1080P", PerRequestPrice: &videoTierPrice}}},
		}}, policy,
	)
}

func TestModelPricingOIDCConversionUsesUserOverrideOnceForAllPriceColumns(t *testing.T) {
	policy := &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: 2.5}}
	svc := oidcModelPricingFixture(policy, map[int64]float64{1: 2})
	result, err := svc.ListAvailablePricing(context.Background(), 23)
	require.NoError(t, err)
	require.Equal(t, 1, policy.calls)
	require.True(t, result.OIDCBillingEnabled)
	require.Equal(t, 2.5, result.OIDCBillingRateMultiplier)
	group := result.Groups[0]
	require.Equal(t, 1.5, group.RateMultiplier)
	require.Equal(t, 5.0, group.EffectiveRateMultiplier, "user rate replaces, rather than multiplies, the group rate")
	model, ok := availableModelByID(group.Models, "gpt-credit")
	require.True(t, ok)
	require.Equal(t, 5.0, model.InputPricePerMillion)
	require.Equal(t, 10.0, model.OutputPricePerMillion)
	require.Equal(t, 15.0, model.CacheWritePricePerMillion)
	require.Equal(t, 20.0, model.CacheReadPricePerMillion)
	require.Equal(t, 25.0, model.PriorityInputPricePerMillion)
	require.Equal(t, 30.0, model.PriorityOutputPricePerMillion)
	require.Equal(t, 35.0, model.PriorityCacheReadPricePerMillion)
	require.Equal(t, 40.0, model.ImageOutputPricePerMillion)
	resolved := svc.resolver.Resolve(context.Background(), PricingInput{Model: model.ID})
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: model.ID, RateMultiplier: group.EffectiveRateMultiplier, Resolved: resolved})
	require.NoError(t, err)
	gateway := &GatewayService{billingService: &BillingService{}, resolver: &ModelPricingResolver{}}
	actual, _, _, err := gateway.frozenGatewayCreditCost(context.Background(), &bc.Execution{GatewayPricingSnapshot: snapshot},
		&ForwardResult{Usage: ClaudeUsage{InputTokens: 200, OutputTokens: 100, CacheCreationInputTokens: 50, CacheReadInputTokens: 25}}, &recordUsageOpts{})
	require.NoError(t, err)
	shownCost := (200*model.InputPricePerMillion + 100*model.OutputPricePerMillion +
		50*model.CacheWritePricePerMillion + 25*model.CacheReadPricePerMillion) / pricePerMillionMultiplier
	require.InDelta(t, actual.ActualCost, shownCost, 1e-12, "displayed prices must match frozen Auth debit without another conversion")
	video, ok := availableModelByID(group.Models, "wan3.0-video")
	require.True(t, ok)
	require.Equal(t, "second", video.PriceUnit)
	require.InDelta(t, .6, *video.UnitPrice, 1e-12)
	require.Equal(t, 1.25, video.Tiers[0].UnitPrice)
	missing, ok := availableModelByID(group.Models, "missing")
	require.True(t, ok)
	require.False(t, missing.PricingAvailable)

	// Settings changes affect the next displayed price, without modifying the
	// resolver's base price or compounding the previous conversion.
	policy.policy.RateMultiplier = 3
	again, err := svc.ListAvailablePricing(context.Background(), 23)
	require.NoError(t, err)
	updated, _ := availableModelByID(again.Groups[0].Models, "gpt-credit")
	require.Equal(t, 6.0, updated.InputPricePerMillion)
}

func TestModelPricingOIDCConversionKeepsDisabledAndFreePrices(t *testing.T) {
	for _, tt := range []struct {
		name    string
		enabled bool
		rate    float64
		want    float64
	}{
		{"disabled", false, 2, 2},
		{"enabled", true, 2, 5},
		{"free override", true, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: tt.enabled, RateMultiplier: 2.5}}
			result, err := oidcModelPricingFixture(policy, map[int64]float64{1: tt.rate}).ListAvailablePricing(context.Background(), 23)
			require.NoError(t, err)
			require.Equal(t, tt.enabled, result.OIDCBillingEnabled)
			require.Equal(t, tt.want, result.Groups[0].EffectiveRateMultiplier)
			model, _ := availableModelByID(result.Groups[0].Models, "gpt-credit")
			require.True(t, model.PricingAvailable)
			require.Equal(t, tt.want, model.InputPricePerMillion)
			if !tt.enabled {
				require.Equal(t, 1.0, result.OIDCBillingRateMultiplier, "a configured but disabled conversion must not change prices")
			}
		})
	}
}

func TestModelPricingOIDCConversionFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy *modelPricingPolicyStub
		rate   float64
	}{
		{"settings unavailable", &modelPricingPolicyStub{err: errors.New("settings unavailable")}, 1},
		{"prerequisite lost", &modelPricingPolicyStub{err: errors.New("OIDC issuer mismatch")}, 1},
		{"invalid conversion", &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: math.NaN()}}, 1},
		{"invalid user rate", &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: 2.5}}, -1},
		{"overflow", &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: 2.5}}, math.MaxFloat64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := oidcModelPricingFixture(tt.policy, map[int64]float64{1: tt.rate}).ListAvailablePricing(context.Background(), 23)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}

func TestModelPricingOIDCConversionRejectsPriceOverflowWithFiniteMultiplier(t *testing.T) {
	for _, tt := range []struct {
		name     string
		resolved *ResolvedPricing
	}{
		{"token output", &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6}}},
		{"unit price", &ResolvedPricing{Mode: BillingModeVideo, DefaultPriceConfigured: true, DefaultPerRequestPrice: 2}},
		{"tier price", &ResolvedPricing{Mode: BillingModeVideo, RequestTiers: []PricingInterval{{TierLabel: "1080P", PerRequestPrice: modelPricingFloatPtr(2)}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := &modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: 1e308}}
			svc := NewModelPricingPageService(
				modelPricingGroupsStub{groups: []Group{{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}}},
				modelPricingAccountsStub{byGroup: map[int64][]Account{1: {{Credentials: map[string]any{"model_mapping": map[string]any{"test-model": "test-model"}}}}}},
				modelPricingResolverStub{byModel: map[string]*ResolvedPricing{"test-model": tt.resolved}}, policy,
			)
			result, err := svc.ListAvailablePricing(context.Background(), 23)
			require.ErrorContains(t, err, "invalid converted model price")
			require.Nil(t, result)
		})
	}
}

func modelPricingFloatPtr(value float64) *float64 { return &value }
