//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminDurationPricesPersistAllDirectionsAndPreserveExplicitZero(t *testing.T) {
	policies, store := newAdminPolicyFixture()
	ctx := context.Background()
	yes := true
	price := &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(0), OutputAudioPricePerSecond: testPtrFloat64(0.03), OutputVideoPricePerSecond: testPtrFloat64(0.2)}
	row, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "multimedia-model", Enabled: &yes, Pricing: price})
	require.NoError(t, err)
	require.Equal(t, price, row.Pricing)
	require.Equal(t, "manual", row.Source)
	replica := NewAdminModelPolicyService(store, nil, nil, nil, nil)
	runtime, err := replica.GetPricing(ctx, "multimedia-model")
	require.NoError(t, err)
	require.Equal(t, BillingModeDuration, runtime.BillingMode)
	require.Equal(t, "second", runtime.PriceUnit)
	require.NotNil(t, runtime.InputAudioPricePerSecond)
	require.Zero(t, *runtime.InputAudioPricePerSecond)
	require.Equal(t, 0.03, *runtime.OutputAudioPricePerSecond)
	require.Equal(t, 0.2, *runtime.OutputVideoPricePerSecond)
	require.Nil(t, runtime.PerRequestPrice, "direction prices must not turn into per-request or free default prices")
	require.Nil(t, runtime.InputPrice)
	row, err = replica.ResetPrice(ctx, "multimedia-model")
	require.NoError(t, err)
	require.Nil(t, row.Pricing)
}

func TestAdminDurationPricesValidateDirectionsAndDoNotMixOtherUnits(t *testing.T) {
	valid := func() *AdminModelPrice {
		return &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(0)}
	}
	for _, price := range []*AdminModelPrice{
		{BillingMode: BillingModeDuration, PriceUnit: "second"},
		{BillingMode: BillingModeDuration, PriceUnit: "character", InputAudioPricePerSecond: testPtrFloat64(1)},
		{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(-1)},
		{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: testPtrFloat64(math.Inf(1))},
		{BillingMode: BillingModeDuration, PriceUnit: "second", OutputVideoPricePerSecond: testPtrFloat64(math.NaN())},
		{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(1), UnitPrice: testPtrFloat64(1)},
		{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(1), InputPricePerMillion: testPtrFloat64(1)},
		{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(1), OutputPricePerMillion: testPtrFloat64(1), InputAudioPricePerSecond: testPtrFloat64(1)},
		{BillingMode: BillingModeAudio, PriceUnit: "second", UnitPrice: testPtrFloat64(1), InputAudioPricePerSecond: testPtrFloat64(1)},
	} {
		require.ErrorIs(t, validateAdminModelPrice("model", price), ErrAdminModelPolicyInvalid)
	}
	require.NoError(t, validateAdminModelPrice("model", valid()))
	require.NoError(t, validateAdminModelPrice("paraformer-realtime-v2", valid()))
	require.ErrorIs(t, validateAdminModelPrice("paraformer-realtime-v2", &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: testPtrFloat64(1)}), ErrAdminModelPolicyInvalid)
	require.True(t, IsQwenRealtimeTTSModel("qwen3-tts-flash-realtime"))
	require.NoError(t, validateAdminModelPrice("qwen3-tts-flash-realtime", &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: testPtrFloat64(0)}))
	require.ErrorIs(t, validateAdminModelPrice("qwen3-tts-flash-realtime", valid()), ErrAdminModelPolicyInvalid)
	require.ErrorIs(t, validateAdminModelPrice("azure-tts", valid()), ErrAdminModelPolicyInvalid)
	require.ErrorIs(t, validateAdminModelPrice("qwen3-asr-flash", &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", OutputVideoPricePerSecond: testPtrFloat64(1)}), ErrAdminModelPolicyInvalid)
	// Legacy units remain accepted and retain their original semantics.
	require.NoError(t, validateAdminModelPrice("paraformer-realtime-v2", &AdminModelPrice{BillingMode: BillingModeAudio, PriceUnit: "second", UnitPrice: testPtrFloat64(0.01)}))
	require.NoError(t, validateAdminModelPrice("qwen3-tts-flash-realtime", &AdminModelPrice{BillingMode: BillingModeAudio, PriceUnit: "character", UnitPrice: testPtrFloat64(0.01)}))
}

func TestDurationResolverAndUserPricingExposeIndependentConvertedDirections(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	ctx := context.Background()
	yes := true
	_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "multimedia-model", Enabled: &yes, Pricing: &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(0), OutputAudioPricePerSecond: testPtrFloat64(0.02), OutputVideoPricePerSecond: testPtrFloat64(0.3)}})
	require.NoError(t, err)
	resolver, _ := noChannelCreditPricer()
	r := resolver.gateway.resolver
	r.SetAdminModelPolicies(policies)
	resolved := r.Resolve(ctx, PricingInput{Model: "multimedia-model"})
	require.NoError(t, resolved.Error)
	require.Equal(t, BillingModeDuration, resolved.Mode)
	require.Equal(t, "second", resolved.PriceUnit)
	require.False(t, resolved.DefaultPriceConfigured)
	require.Zero(t, resolved.DefaultPerRequestPrice)
	require.Empty(t, resolved.RequestTiers)
	// All three directional prices survive the same JSON snapshot contract.
	raw, err := json.Marshal(resolved)
	require.NoError(t, err)
	var frozen ResolvedPricing
	require.NoError(t, json.Unmarshal(raw, &frozen))
	require.Equal(t, resolved.InputAudioPricePerSecond, frozen.InputAudioPricePerSecond)
	require.Equal(t, resolved.OutputAudioPricePerSecond, frozen.OutputAudioPricePerSecond)
	require.Equal(t, resolved.OutputVideoPricePerSecond, frozen.OutputVideoPricePerSecond)
	page := NewModelPricingPageService(nil, nil, r, nil)
	row := page.resolveModelPricing(ctx, 1, "multimedia-model", 6)
	require.True(t, row.PricingAvailable)
	require.Equal(t, BillingModeDuration, row.BillingMode)
	require.Equal(t, "second", row.PriceUnit)
	require.Nil(t, row.UnitPrice)
	require.NotNil(t, row.InputAudioPricePerSecond)
	require.Zero(t, *row.InputAudioPricePerSecond)
	require.InDelta(t, 0.12, *row.OutputAudioPricePerSecond, 1e-12)
	require.InDelta(t, 1.8, *row.OutputVideoPricePerSecond, 1e-12)
	require.True(t, finiteConvertedModelPricing(row))
	row.OutputVideoPricePerSecond = testPtrFloat64(math.Inf(1))
	require.False(t, finiteConvertedModelPricing(row))
	_, err = r.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: "multimedia-model", Resolver: r, Resolved: resolved})
	require.ErrorContains(t, err, "measured media duration", "a duration model cannot silently use token prices on a text endpoint")
}

func TestChannelOverridesClearAdministratorDurationMetadataAndRejectUnsupportedPersistence(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	yes := true
	_, err := policies.Update(context.Background(), AdminModelPolicyUpdate{Model: "media-model", Enabled: &yes, Pricing: &AdminModelPrice{BillingMode: BillingModeDuration, PriceUnit: "second", InputAudioPricePerSecond: testPtrFloat64(1)}})
	require.NoError(t, err)
	r := newResolverWithChannel(t, []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"media-model"}, BillingMode: BillingModePerRequest, PerRequestPrice: testPtrFloat64(0.5)}})
	r.SetAdminModelPolicies(policies)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "media-model", GroupID: groupIDPtr()})
	require.NoError(t, resolved.Error)
	require.Equal(t, PricingSourceChannel, resolved.Source)
	require.Equal(t, BillingModePerRequest, resolved.Mode)
	require.Nil(t, resolved.InputAudioPricePerSecond)
	require.Nil(t, resolved.OutputAudioPricePerSecond)
	require.Nil(t, resolved.OutputVideoPricePerSecond)
	require.True(t, resolved.DefaultPriceConfigured)
	require.Equal(t, 0.5, resolved.DefaultPerRequestPrice)
	for _, price := range []ChannelModelPricing{
		{BillingMode: BillingModeDuration, InputAudioPricePerSecond: testPtrFloat64(1)},
		{BillingMode: BillingModeToken, InputAudioPricePerSecond: testPtrFloat64(1)},
	} {
		require.ErrorContains(t, validatePricingBillingMode([]ChannelModelPricing{price}), "administrator model management")
	}
	require.True(t, BillingModeDuration.IsValid())
}
