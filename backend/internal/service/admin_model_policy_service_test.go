//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type adminPolicySettings struct {
	SettingRepository
	values     map[string]string
	fail       error
	batchReads int
}

func (s *adminPolicySettings) GetValue(_ context.Context, key string) (string, error) {
	if s.fail != nil {
		return "", s.fail
	}
	v, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (s *adminPolicySettings) Set(_ context.Context, key, value string) error {
	if s.fail != nil {
		return s.fail
	}
	s.values[key] = value
	return nil
}
func (s *adminPolicySettings) GetAll(context.Context) (map[string]string, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	return s.values, nil
}
func (s *adminPolicySettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.batchReads++
	if s.fail != nil {
		return nil, s.fail
	}
	m := map[string]string{}
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			m[key] = v
		}
	}
	return m, nil
}

func newAdminPolicyFixture() (*AdminModelPolicyService, *adminPolicySettings) {
	store := &adminPolicySettings{values: map[string]string{}}
	return NewAdminModelPolicyService(store, nil, newTestBillingServiceForResolver(), nil, nil), store
}

func TestAdminModelPolicyChangesPersistAcrossReplicasAndPriceResetPreservesDisable(t *testing.T) {
	svc, store := newAdminPolicyFixture()
	ctx := context.Background()
	enabled := false
	zero := 0.0
	row, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: "Paraformer-realtime-v2", Enabled: &enabled, Pricing: &AdminModelPrice{BillingMode: BillingModeAudio, PriceUnit: "second", UnitPrice: &zero}})
	require.NoError(t, err)
	require.False(t, row.Enabled)
	require.Equal(t, "manual", row.Source)
	replica := NewAdminModelPolicyService(store, nil, nil, nil, nil)
	allowed, err := replica.IsModelEnabled(ctx, "paraformer-realtime-v2")
	require.NoError(t, err)
	require.False(t, allowed)
	ids, err := replica.FilterEnabledModels(ctx, []string{"paraformer-realtime-v2", "another-model"})
	require.NoError(t, err)
	require.Equal(t, []string{"another-model"}, ids)
	require.Equal(t, 1, store.batchReads)
	price, err := replica.GetPricing(ctx, "paraformer-realtime-v2")
	require.NoError(t, err)
	require.NotNil(t, price.PerRequestPrice)
	require.Zero(t, *price.PerRequestPrice)
	row, err = svc.ResetPrice(ctx, "paraformer-realtime-v2")
	require.NoError(t, err)
	require.False(t, row.Enabled)
	require.Nil(t, row.Pricing)
	price, err = replica.GetPricing(ctx, "paraformer-realtime-v2")
	require.NoError(t, err)
	require.Nil(t, price, "restoring an unpriced model must not create a free price")
	for key := range store.values {
		require.LessOrEqual(t, len(key), 100, "settings key schema limit")
		require.NotContains(t, key, "paraformer")
	}
}

func TestAdminModelPriceControlsNewResolverResultsAndLeavesFrozenSnapshotUntouched(t *testing.T) {
	svc, _ := newAdminPolicyFixture()
	ctx := context.Background()
	enabled := true
	r := NewModelPricingResolver(nil, newTestBillingServiceForResolver())
	r.SetAdminModelPolicies(svc)
	old := r.Resolve(ctx, PricingInput{Model: "claude-sonnet-4"})
	_, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: "claude-sonnet-4", Enabled: &enabled, Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(7), OutputPricePerMillion: testPtrFloat64(11)}})
	require.NoError(t, err)
	newPrice := r.Resolve(ctx, PricingInput{Model: "claude-sonnet-4"})
	require.NoError(t, newPrice.Error)
	require.Equal(t, PricingSourceManual, newPrice.Source)
	require.InDelta(t, 7e-6, newPrice.BasePricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 11e-6, newPrice.BasePricing.OutputPricePerToken, 1e-15)
	require.InDelta(t, 3e-6, old.BasePricing.InputPricePerToken, 1e-15, "admission snapshot cannot change after an edit")
	channel := newResolverWithChannel(t, []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(9e-6)}})
	channel.SetAdminModelPolicies(svc)
	resolved := channel.Resolve(ctx, PricingInput{Model: "claude-sonnet-4", GroupID: groupIDPtr()})
	require.Equal(t, PricingSourceChannel, resolved.Source)
	require.InDelta(t, 9e-6, resolved.BasePricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 11e-6, resolved.BasePricing.OutputPricePerToken, 1e-15)
}

func TestAdminModelPolicyStorageFailuresCannotFallBackToFreeOrDefaultPrice(t *testing.T) {
	svc, store := newAdminPolicyFixture()
	ctx := context.Background()
	store.fail = errors.New("database unavailable")
	allowed, err := svc.IsModelEnabled(ctx, "model")
	require.Error(t, err)
	require.False(t, allowed)
	_, err = svc.FilterEnabledModels(ctx, []string{"model"})
	require.Error(t, err)
	r := NewModelPricingResolver(nil, newTestBillingServiceForResolver())
	r.SetAdminModelPolicies(svc)
	resolved := r.Resolve(ctx, PricingInput{Model: "claude-sonnet-4"})
	require.Error(t, resolved.Error)
	_, err = r.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: "claude-sonnet-4", Resolver: r, Resolved: resolved})
	require.Error(t, err)
	store.fail = nil
	store.values[adminModelPolicyKey("model")] = `{"model":"different-model","enabled":true}`
	_, err = svc.GetPricing(ctx, "model")
	require.Error(t, err)
}

func TestAdminModelMediaAliasUnitsReachUserPricePage(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	ctx := context.Background()
	enabled := true
	resolver := newResolverWithChannel(t, nil)
	resolver.SetAdminModelPolicies(policies)
	page := &ModelPricingPageService{resolver: resolver}
	for model, unit := range map[string]string{"dictation": "second", "spoken-reply": "character"} {
		_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: model, Enabled: &enabled,
			Pricing: &AdminModelPrice{BillingMode: BillingModeAudio, PriceUnit: unit, UnitPrice: testPtrFloat64(0.0123)}})
		require.NoError(t, err)
		price := page.resolveModelPricing(ctx, 100, model, 2)
		require.NoError(t, price.Error)
		require.True(t, price.PricingAvailable)
		require.Equal(t, unit, price.PriceUnit)
		require.NotNil(t, price.UnitPrice)
		require.InDelta(t, 0.0246, *price.UnitPrice, 1e-12)
	}
}

func TestAdminModelTokenPriceDoesNotInheritAutomaticTiersAndSurvivesSnapshotReplay(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	ctx := context.Background()
	yes := true
	billing := newTestBillingServiceForResolver()
	billing.fallbackPrices["gpt-6-sol"] = &ModelPricing{
		InputPricePerToken: 10e-6, OutputPricePerToken: 20e-6,
		InputPricePerTokenPriority: 30e-6, OutputPricePerTokenPriority: 60e-6,
		CacheReadPricePerToken: 1e-6, CacheReadPricePerTokenPriority: 3e-6,
		LongContextInputThreshold: 100, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 3,
		ImageOutputPricePerToken: 100e-6, HasImageOutputPricing: true,
	}
	r := NewModelPricingResolver(nil, billing)
	r.SetAdminModelPolicies(policies)
	_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "gpt-6-sol", Enabled: &yes,
		Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(2), OutputPricePerMillion: testPtrFloat64(4)}})
	require.NoError(t, err)
	resolved := r.Resolve(ctx, PricingInput{Model: "gpt-6-sol"})
	require.NoError(t, resolved.Error)
	require.True(t, resolved.BasePricing.FlatTokenPrices)
	require.Equal(t, 100, resolved.BasePricing.LongContextInputThreshold, "catalogue metadata stays dormant while the manual flat flag is set")
	// Pricing snapshots are serialized by the central gateway. Replay must
	// preserve the flat-price contract even after the administrator edits it.
	raw, err := json.Marshal(resolved)
	require.NoError(t, err)
	var frozen ResolvedPricing
	require.NoError(t, json.Unmarshal(raw, &frozen))
	_, err = policies.Update(ctx, AdminModelPolicyUpdate{Model: "gpt-6-sol", Enabled: &yes,
		Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(99), OutputPricePerMillion: testPtrFloat64(99)}})
	require.NoError(t, err)
	for _, tier := range []string{"", "priority", "fast", "flex", billingServiceTierAnthropicFast} {
		cost, err := billing.CalculateCostUnified(CostInput{Ctx: ctx, Model: "gpt-6-sol", Resolver: r, Resolved: &frozen,
			Tokens: UsageTokens{InputTokens: 300000, OutputTokens: 1000, ImageOutputTokens: 500, CacheReadTokens: 1000}, RateMultiplier: 1, ServiceTier: tier})
		require.NoError(t, err, tier)
		require.InDelta(t, 0.605, cost.ActualCost, 1e-12, tier)
	}
	_, err = policies.Update(ctx, AdminModelPolicyUpdate{Model: "gpt-6-sol", Enabled: &yes,
		Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(0), OutputPricePerMillion: testPtrFloat64(0), CacheReadPricePerMillion: testPtrFloat64(0)}})
	require.NoError(t, err)
	free, err := billing.CalculateCostUnified(CostInput{Ctx: ctx, Model: "gpt-6-sol", Resolver: r,
		Tokens: UsageTokens{InputTokens: 300000, OutputTokens: 1000, ImageOutputTokens: 500, CacheReadTokens: 1000}, ServiceTier: "priority"})
	require.NoError(t, err)
	require.Zero(t, free.ActualCost)
}

func TestChannelPricingRetainsTierAndLongContextBehaviorAboveManualBase(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	ctx := context.Background()
	yes := true
	_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "claude-sonnet-4", Enabled: &yes,
		Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(99), OutputPricePerMillion: testPtrFloat64(99)}})
	require.NoError(t, err)
	flat := ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken,
		InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(4e-6), CacheWritePrice: testPtrFloat64(3e-6), CacheReadPrice: testPtrFloat64(1e-6)}
	interval := ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}, BillingMode: BillingModeToken,
		Intervals: []PricingInterval{{InputPrice: flat.InputPrice, OutputPrice: flat.OutputPrice, CacheWritePrice: flat.CacheWritePrice, CacheReadPrice: flat.CacheReadPrice}}}
	for _, channel := range []ChannelModelPricing{flat, interval} {
		baseline := newResolverWithChannel(t, []ChannelModelPricing{channel})
		withManual := newResolverWithChannel(t, []ChannelModelPricing{channel})
		for _, resolver := range []*ModelPricingResolver{baseline, withManual} {
			base := resolver.billingService.fallbackPrices["claude-sonnet-4"]
			base.LongContextInputThreshold, base.LongContextInputMultiplier, base.LongContextOutputMultiplier = 100, 2, 3
		}
		withManual.SetAdminModelPolicies(policies)
		before := baseline.Resolve(ctx, PricingInput{Model: "claude-sonnet-4", GroupID: groupIDPtr()})
		after := withManual.Resolve(ctx, PricingInput{Model: "claude-sonnet-4", GroupID: groupIDPtr()})
		require.Equal(t, PricingSourceChannel, after.Source)
		require.False(t, after.BasePricing.FlatTokenPrices)
		require.Equal(t, 100, after.BasePricing.LongContextInputThreshold)
		for _, tier := range []string{"", "priority", "fast", "flex", billingServiceTierAnthropicFast} {
			input := CostInput{Ctx: ctx, Model: "claude-sonnet-4", GroupID: groupIDPtr(), Tokens: UsageTokens{InputTokens: 300000, OutputTokens: 1000, CacheCreationTokens: 1000, CacheReadTokens: 1000}, RateMultiplier: 1, ServiceTier: tier}
			input.Resolver, input.Resolved = baseline, before
			expected, err := baseline.billingService.CalculateCostUnified(input)
			require.NoError(t, err)
			input.Resolver, input.Resolved = withManual, after
			actual, err := withManual.billingService.CalculateCostUnified(input)
			require.NoError(t, err)
			require.Equal(t, expected, actual, "a lower-priority manual rule cannot change channel tier semantics")
		}
	}
}

func TestAdminModelPolicyValidatesPricesUnitsAndExactIDs(t *testing.T) {
	svc, _ := newAdminPolicyFixture()
	yes := true
	ctx := context.Background()
	for _, price := range []*AdminModelPrice{
		{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(1)},
		{BillingMode: BillingModeAudio, PriceUnit: "second"},
		{BillingMode: BillingModeAudio, PriceUnit: "minute", UnitPrice: testPtrFloat64(1)},
		{BillingMode: BillingModePerRequest, PriceUnit: "request", UnitPrice: testPtrFloat64(-1)},
		{BillingMode: BillingModePerRequest, PriceUnit: "request", UnitPrice: testPtrFloat64(math.Inf(1))},
	} {
		_, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: "model", Enabled: &yes, Pricing: price})
		require.ErrorIs(t, err, ErrAdminModelPolicyInvalid)
	}
	for _, model := range []string{"", "prefix-*", "a b", strings.Repeat("x", 301)} {
		_, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: model, Enabled: &yes})
		require.ErrorIs(t, err, ErrAdminModelPolicyInvalid)
	}
	_, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: "model"})
	require.ErrorIs(t, err, ErrAdminModelPolicyInvalid)
	_, err = svc.Update(ctx, AdminModelPolicyUpdate{Model: "paraformer-realtime-v2", Enabled: &yes, Pricing: &AdminModelPrice{BillingMode: BillingModeAudio, PriceUnit: "character", UnitPrice: testPtrFloat64(1)}})
	require.ErrorIs(t, err, ErrAdminModelPolicyInvalid)
}

func TestAdminModelListFiltersBeforePaginationAndDoesNotExposeOtherSettings(t *testing.T) {
	svc, store := newAdminPolicyFixture()
	ctx := context.Background()
	no := false
	store.values["oidc_client_secret"] = "must-not-appear"
	for _, model := range []string{"alpha", "beta", "gamma"} {
		_, err := svc.Update(ctx, AdminModelPolicyUpdate{Model: model, Enabled: &no})
		require.NoError(t, err)
	}
	list, err := svc.List(ctx, "", &no, 2, 1)
	require.NoError(t, err)
	require.Equal(t, 3, list.Total)
	require.Equal(t, "beta", list.Items[0].Model)
	list, err = svc.List(ctx, "gamma", nil, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, list.Total)
	require.Equal(t, "gamma", list.Items[0].Model)
}

func TestUserModelPricesDoNotAdvertiseDisabledPublicModels(t *testing.T) {
	policies, _ := newAdminPolicyFixture()
	ctx := context.Background()
	disabled := false
	_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "hidden-model", Enabled: &disabled})
	require.NoError(t, err)
	page := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{{ID: 1, Name: "models", Platform: PlatformOpenAI, RateMultiplier: 1}}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{1: {{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"hidden-model": "upstream-a", "visible-model": "upstream-b"}}}}}},
		modelPricingResolverStub{}, nil)
	page.SetAdminModelPolicies(policies)
	result, err := page.ListAvailablePricing(ctx, 1)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.Equal(t, []string{"visible-model"}, availableModelIDs(result.Groups[0].Models))
}

func TestAdministratorBasePricesReachLocalTokenAndImageCostCalculations(t *testing.T) {
	policies, store := newAdminPolicyFixture()
	ctx := context.Background()
	enabled := true
	pricer, key := noChannelCreditPricer()
	gateway := pricer.gateway
	gateway.resolver.SetAdminModelPolicies(policies)
	_, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "claude-sonnet-4", Enabled: &enabled, Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(4), OutputPricePerMillion: testPtrFloat64(8)}})
	require.NoError(t, err)
	cost := gateway.calculateTokenCost(ctx, &ForwardResult{Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 1000}}, key, "claude-sonnet-4", 2, &recordUsageOpts{})
	require.False(t, cost.PricingFailed)
	require.InDelta(t, 0.024, cost.ActualCost, 1e-12)
	_, err = policies.Update(ctx, AdminModelPolicyUpdate{Model: "custom-image", Enabled: &enabled, Pricing: &AdminModelPrice{BillingMode: BillingModeImage, PriceUnit: "image", UnitPrice: testPtrFloat64(0.2)}})
	require.NoError(t, err)
	cost = gateway.calculateImageCost(ctx, &ForwardResult{ImageCount: 3}, key, "custom-image", 2)
	require.False(t, cost.PricingFailed)
	require.InDelta(t, 1.2, cost.ActualCost, 1e-12)
	store.fail = errors.New("policy storage unavailable")
	require.True(t, gateway.calculateTokenCost(ctx, &ForwardResult{}, key, "claude-sonnet-4", 1, &recordUsageOpts{}).PricingFailed)
	require.True(t, gateway.calculateImageCost(ctx, &ForwardResult{ImageCount: 1}, key, "custom-image", 1).PricingFailed)
}

func TestLocalModelPolicyReadFailureDoesNotRecordSuccessfulZeroCostUsage(t *testing.T) {
	policies, store := newAdminPolicyFixture()
	store.fail = errors.New("pricing database unavailable")
	usage := &openAIRecordUsageBestEffortLogRepoStub{}
	gateway := newGatewayRecordUsageServiceForTest(usage, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	pricer, key := noChannelCreditPricer()
	gateway.resolver = pricer.gateway.resolver
	gateway.resolver.SetAdminModelPolicies(policies)
	key.ID, key.UserID = 5, 6
	err := gateway.RecordUsage(context.Background(), &RecordUsageInput{Result: &ForwardResult{Model: "gpt-6-sol", RequestID: "failed-local-price", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 5}},
		APIKey: key, User: &User{ID: 6}, Account: &Account{ID: 7}})
	require.Error(t, err)
	require.Zero(t, usage.bestEffortCalls)
	require.Zero(t, usage.createCalls)
}
