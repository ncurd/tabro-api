package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type modelPricingGroupsStub struct {
	groups []Group
	rates  map[int64]float64
}

func (s modelPricingGroupsStub) GetModelPricingGroups(_ context.Context, _ int64) ([]Group, error) {
	return s.groups, nil
}

func (s modelPricingGroupsStub) GetUserGroupRates(_ context.Context, _ int64) (map[int64]float64, error) {
	return s.rates, nil
}

type modelPricingAccountsStub struct {
	byGroup map[int64][]Account
}

func (s modelPricingAccountsStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return s.byGroup[groupID], nil
}

type modelPricingResolverStub struct {
	byModel map[string]*ResolvedPricing
}

func (s modelPricingResolverStub) Resolve(_ context.Context, input PricingInput) *ResolvedPricing {
	if s.byModel == nil {
		return nil
	}
	return s.byModel[input.Model]
}

func (s modelPricingResolverStub) GetIntervalPricing(resolved *ResolvedPricing, _ int) *ModelPricing {
	if resolved == nil {
		return nil
	}
	return resolved.BasePricing
}

func TestModelPricingPageServiceListAvailablePricingUsesMappedModelsAndEffectiveUserRate(t *testing.T) {
	groupID := int64(10)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{
			groups: []Group{{ID: groupID, Name: "OpenAI Plus", Platform: PlatformOpenAI, RateMultiplier: 1.5}},
			rates:  map[int64]float64{groupID: 2},
		},
		modelPricingAccountsStub{byGroup: map[int64][]Account{
			groupID: {
				{Credentials: map[string]any{"model_mapping": map[string]any{
					"gpt-z": "upstream-z",
					"gpt-a": "upstream-a",
				}}},
			},
		}},
		modelPricingResolverStub{byModel: map[string]*ResolvedPricing{
			"gpt-a": {
				Source: PricingSourceLiteLLM,
				BasePricing: &ModelPricing{
					InputPricePerToken:             1e-6,
					OutputPricePerToken:            2e-6,
					CacheCreationPricePerToken:     3e-6,
					CacheReadPricePerToken:         4e-6,
					InputPricePerTokenPriority:     5e-6,
					OutputPricePerTokenPriority:    6e-6,
					CacheReadPricePerTokenPriority: 7e-6,
				},
			},
			"gpt-z": {
				Source:      PricingSourceFallback,
				BasePricing: nil,
			},
		}},
	)

	result, err := svc.ListAvailablePricing(context.Background(), 123)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)

	group := result.Groups[0]
	require.Equal(t, groupID, group.ID)
	require.Equal(t, "OpenAI Plus", group.Name)
	require.Equal(t, PlatformOpenAI, group.Platform)
	require.Equal(t, 1.5, group.RateMultiplier)
	require.Equal(t, 2.0, group.EffectiveRateMultiplier)
	require.Len(t, group.Models, 2)

	require.Equal(t, "gpt-a", group.Models[0].ID)
	require.True(t, group.Models[0].PricingAvailable)
	require.Equal(t, 2.0, group.Models[0].InputPricePerMillion)
	require.Equal(t, 4.0, group.Models[0].OutputPricePerMillion)
	require.Equal(t, 6.0, group.Models[0].CacheWritePricePerMillion)
	require.Equal(t, 8.0, group.Models[0].CacheReadPricePerMillion)
	require.Equal(t, 10.0, group.Models[0].PriorityInputPricePerMillion)
	require.Equal(t, 12.0, group.Models[0].PriorityOutputPricePerMillion)
	require.Equal(t, 14.0, group.Models[0].PriorityCacheReadPricePerMillion)
	require.Equal(t, PricingSourceLiteLLM, group.Models[0].Source)

	require.Equal(t, "gpt-z", group.Models[1].ID)
	require.False(t, group.Models[1].PricingAvailable)
	require.Equal(t, PricingSourceFallback, group.Models[1].Source)
}

func TestModelPricingPageServiceListAvailablePricingFallsBackToPlatformDefaults(t *testing.T) {
	groupID := int64(20)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{
			groups: []Group{{ID: groupID, Name: "Default OpenAI", Platform: PlatformOpenAI, RateMultiplier: 1}},
		},
		modelPricingAccountsStub{byGroup: map[int64][]Account{
			groupID: {
				{Credentials: map[string]any{}},
			},
		}},
		modelPricingResolverStub{},
	)

	result, err := svc.ListAvailablePricing(context.Background(), 123)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.NotEmpty(t, result.Groups[0].Models)
	require.Equal(t, "gpt-5.5", result.Groups[0].Models[0].ID)
	require.False(t, result.Groups[0].Models[0].PricingAvailable)
}

func TestModelPricingPageServiceListAvailablePricingFallsBackToAliyunDefaults(t *testing.T) {
	groupID := int64(21)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{
			groups: []Group{{ID: groupID, Name: "Aliyun", Platform: "aliyun", RateMultiplier: 1}},
		},
		modelPricingAccountsStub{byGroup: map[int64][]Account{
			groupID: {
				{Credentials: map[string]any{}},
			},
		}},
		modelPricingResolverStub{},
	)

	result, err := svc.ListAvailablePricing(context.Background(), 123)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.NotEmpty(t, result.Groups[0].Models)
	require.Equal(t, "qwen-turbo", result.Groups[0].Models[0].ID)
}

func TestModelPricingPageServiceListAvailablePricingUsesAliyunDefaultsForDashScopeCompatibleBaseURL(t *testing.T) {
	groupID := int64(22)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{
			groups: []Group{{ID: groupID, Name: "Aliyun OpenAI Compat", Platform: PlatformOpenAI, RateMultiplier: 1}},
		},
		modelPricingAccountsStub{byGroup: map[int64][]Account{
			groupID: {
				{
					Platform: PlatformOpenAI,
					Type:     AccountTypeAPIKey,
					Credentials: map[string]any{
						"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
					},
				},
			},
		}},
		modelPricingResolverStub{byModel: map[string]*ResolvedPricing{
			"qwen-plus": {
				Source: PricingSourceFallback,
				BasePricing: &ModelPricing{
					InputPricePerToken:  1e-6,
					OutputPricePerToken: 2e-6,
				},
			},
		}},
	)

	result, err := svc.ListAvailablePricing(context.Background(), 123)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.NotEmpty(t, result.Groups[0].Models)
	require.Equal(t, "qwen-turbo", result.Groups[0].Models[0].ID)
	require.Contains(t, availableModelIDs(result.Groups[0].Models), "qwen-plus")

	qwenPlus, ok := availableModelByID(result.Groups[0].Models, "qwen-plus")
	require.True(t, ok)
	require.True(t, qwenPlus.PricingAvailable)
	require.Equal(t, 1.0, qwenPlus.InputPricePerMillion)
	require.Equal(t, 2.0, qwenPlus.OutputPricePerMillion)
}

func TestModelPricingPageServiceListAvailablePricingKeepsZeroRate(t *testing.T) {
	groupID := int64(30)
	svc := NewModelPricingPageService(
		modelPricingGroupsStub{
			groups: []Group{{ID: groupID, Name: "Free Group", Platform: PlatformOpenAI, RateMultiplier: 0}},
		},
		modelPricingAccountsStub{byGroup: map[int64][]Account{
			groupID: {
				{Credentials: map[string]any{"model_mapping": map[string]any{
					"gpt-free": "gpt-free-upstream",
				}}},
			},
		}},
		modelPricingResolverStub{byModel: map[string]*ResolvedPricing{
			"gpt-free": {
				BasePricing: &ModelPricing{
					InputPricePerToken:  1e-6,
					OutputPricePerToken: 2e-6,
				},
			},
		}},
	)

	result, err := svc.ListAvailablePricing(context.Background(), 123)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	require.Equal(t, 0.0, result.Groups[0].EffectiveRateMultiplier)
	require.Equal(t, 0.0, result.Groups[0].Models[0].InputPricePerMillion)
	require.Equal(t, 0.0, result.Groups[0].Models[0].OutputPricePerMillion)
}

type modelPricingGroupsErrorStub struct{}

func (modelPricingGroupsErrorStub) GetModelPricingGroups(context.Context, int64) ([]Group, error) {
	return nil, errors.New("groups failed")
}

func (modelPricingGroupsErrorStub) GetUserGroupRates(context.Context, int64) (map[int64]float64, error) {
	return nil, nil
}

func TestModelPricingPageServiceListAvailablePricingReturnsGroupErrors(t *testing.T) {
	svc := NewModelPricingPageService(
		modelPricingGroupsErrorStub{},
		modelPricingAccountsStub{},
		modelPricingResolverStub{},
	)

	_, err := svc.ListAvailablePricing(context.Background(), 123)
	require.ErrorContains(t, err, "model pricing groups")
}

func availableModelIDs(models []AvailableModelPricingModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func availableModelByID(models []AvailableModelPricingModel, id string) (AvailableModelPricingModel, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return AvailableModelPricingModel{}, false
}
