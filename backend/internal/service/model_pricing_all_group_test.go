//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllGroupPricingUsesConfiguredPlatformsAndOIDCMultiplier(t *testing.T) {
	const id int64 = 5
	group := Group{ID: id, Platform: PlatformAll, Status: StatusActive, Hydrated: true, RateMultiplier: 1.5}
	account := func(platform string, mappings map[string]any) Account {
		return Account{Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": mappings}}
	}
	accounts := []Account{
		account(PlatformAnthropic, map[string]any{"claude-opus-5": "claude-opus-5", "shared": "claude-opus-5"}),
		account(PlatformOpenAI, map[string]any{"gpt-6-sol": "gpt-6-sol", "shared": "gpt-6-sol"}),
		account(PlatformGemini, map[string]any{"gemini-3.1-pro-preview": "gemini-3.1-pro-preview"}),
	}
	price := func(platform, model string, input, output float64) ChannelModelPricing {
		return ChannelModelPricing{Platform: platform, Models: []string{model}, InputPrice: &input, OutputPrice: &output}
	}
	channels := newTestChannelService(makeStandardRepo(Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{id}, BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			price(PlatformAnthropic, "claude-opus-5", 1e-6, 2e-6),
			price(PlatformOpenAI, "gpt-6-sol", 3e-6, 4e-6),
			price(PlatformGemini, "gemini-3.1-pro-preview", 5e-6, 6e-6),
			price(PlatformAnthropic, "shared", 8e-6, 8e-6),
			price(PlatformOpenAI, "shared", 9e-6, 9e-6),
		}}, map[int64]string{id: PlatformAll}))
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{group}, rates: map[int64]float64{id: 2}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{id: accounts}},
		NewModelPricingResolver(channels, NewBillingService(nil, nil)),
		&modelPricingPolicyStub{policy: OIDCBillingPolicy{Enabled: true, RateMultiplier: 10}})
	for i := 0; i < 2; i++ {
		result, err := svc.ListAvailablePricing(context.Background(), 1)
		require.NoError(t, err)
		require.Len(t, result.Groups, 1)
		require.Equal(t, PlatformAll, result.Groups[0].Platform)
		require.Equal(t, 20.0, result.Groups[0].EffectiveRateMultiplier)
		require.Len(t, result.Groups[0].Models, 4)
		for _, expected := range []struct {
			model         string
			input, output float64
		}{
			{"claude-opus-5", 20, 40}, {"gpt-6-sol", 60, 80}, {"gemini-3.1-pro-preview", 100, 120}, {"shared", 180, 180},
		} {
			model, ok := availableModelByID(result.Groups[0].Models, expected.model)
			require.True(t, ok)
			require.True(t, model.PricingAvailable)
			require.InDelta(t, expected.input, model.InputPricePerMillion, 1e-8)
			require.InDelta(t, expected.output, model.OutputPricePerMillion, 1e-8)
		}
		// Provider preference for a shared alias is stable regardless of account
		// list order, matching the default Responses routing preference.
		accounts[0], accounts[1] = accounts[1], accounts[0]
	}
}

func TestAllGroupPricingWithoutAccountsHasEmptyCatalogue(t *testing.T) {
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{{ID: 1, Platform: PlatformAll, Status: StatusActive, RateMultiplier: 1}}},
		modelPricingAccountsStub{}, modelPricingResolverStub{}, nil)
	result, err := svc.ListAvailablePricing(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.NotNil(t, result.Groups[0].Models)
	require.Empty(t, result.Groups[0].Models)
}

func TestAllGroupPricingExplicitAliasBeforeUnrestrictedProvider(t *testing.T) {
	const id int64 = 4
	group := Group{ID: id, Platform: PlatformAll, Status: StatusActive, RateMultiplier: 1}
	accounts := []Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"named": "claude-opus-5"}}},
	}
	wrong, correct := 9e-6, 2e-6
	channels := newTestChannelService(makeStandardRepo(Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{id}, BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"named"}, InputPrice: &wrong, OutputPrice: &wrong},
			{Platform: PlatformAnthropic, Models: []string{"named"}, InputPrice: &correct, OutputPrice: &correct},
		}}, map[int64]string{id: PlatformAll}))
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{group}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{id: accounts}},
		NewModelPricingResolver(channels, NewBillingService(nil, nil)), nil)
	result, err := svc.ListAvailablePricing(context.Background(), 1)
	require.NoError(t, err)
	model, ok := availableModelByID(result.Groups[0].Models, "named")
	require.True(t, ok)
	require.InDelta(t, 2.0, model.InputPricePerMillion, 1e-8)
}

func TestAllGroupPricingUnmappedAccountsKeepEveryProviderDefault(t *testing.T) {
	accounts := []Account{
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		{Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
	}
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{{ID: 1, Platform: PlatformAll, Status: StatusActive, RateMultiplier: 1}}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{1: accounts}}, modelPricingResolverStub{}, nil)
	result, err := svc.ListAvailablePricing(context.Background(), 1)
	require.NoError(t, err)
	ids := availableModelIDs(result.Groups[0].Models)
	require.Contains(t, ids, "claude-opus-5")
	require.Contains(t, ids, "gpt-6-sol")
	require.Contains(t, ids, "gemini-3.1-pro-preview")
}

func TestAllGroupPricingExcludesIneligibleAccountAliases(t *testing.T) {
	accounts := []Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"blocked": "gpt-6-sol"}}},
	}
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{{ID: 1, Platform: PlatformAll, Status: StatusActive, RateMultiplier: 1, RequireOAuthOnly: true}}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{1: accounts}}, modelPricingResolverStub{}, nil)
	result, err := svc.ListAvailablePricing(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, result.Groups[0].Models)
}

func TestAllGroupPricingSkipsRestrictedProviderForSharedAlias(t *testing.T) {
	const id int64 = 4
	accounts := []Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"shared": "gpt-6-sol"}}},
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"shared": "claude-opus-5"}}},
	}
	price := 2e-6
	channels := newTestChannelService(makeStandardRepo(Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{id}, BillingModelSource: BillingModelSourceRequested, RestrictModels: true,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"other"}, InputPrice: &price, OutputPrice: &price},
			{Platform: PlatformAnthropic, Models: []string{"shared"}, InputPrice: &price, OutputPrice: &price},
		}}, map[int64]string{id: PlatformAll}))
	svc := NewModelPricingPageService(modelPricingGroupsStub{groups: []Group{{ID: id, Platform: PlatformAll, Status: StatusActive, RateMultiplier: 1}}},
		modelPricingAccountsStub{byGroup: map[int64][]Account{id: accounts}}, NewModelPricingResolver(channels, NewBillingService(nil, nil)), nil)
	result, err := svc.ListAvailablePricing(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, result.Groups[0].Models, 1)
	require.Equal(t, "shared", result.Groups[0].Models[0].ID)
	require.Equal(t, PricingSourceChannel, result.Groups[0].Models[0].Source)
	require.InDelta(t, 2.0, result.Groups[0].Models[0].InputPricePerMillion, 1e-8)
}
