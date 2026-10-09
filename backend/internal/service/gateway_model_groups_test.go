package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestScopedModelCatalogueUnionsOnlyAuthorizedGroups(t *testing.T) {
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		1: {{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"claude-alias": "claude-opus-5"}}}},
		2: {{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"openai-alias": "gpt-6-sol"}}}},
		3: {{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"secret-model": "gpt-6-sol"}}}},
	}}
	s := &GatewayService{accountRepo: repo}
	groups := []*Group{{ID: 1, Status: StatusActive}, {ID: 2, Status: StatusActive}, {ID: 2, Status: StatusActive}}
	ctx := WithGatewayModelGroups(context.Background(), groups)
	groups[0].ID = 3
	require.Equal(t, []string{"claude-alias", "openai-alias"}, s.GetAvailableModels(ctx, nil, ""))
	require.Equal(t, []string{"openai-alias"}, s.GetAvailableModels(ctx, nil, PlatformOpenAI))
	require.Zero(t, repo.listAllCalls.Load(), "a scoped catalogue must not enumerate global accounts")
	for _, scope := range [][]*Group{nil, {}, {{ID: 99, Status: StatusActive}}, {{ID: 3, Status: StatusDisabled}}} {
		models := s.GetAvailableModels(WithGatewayModelGroups(context.Background(), scope), nil, "")
		require.NotNil(t, models, "empty scopes must not trigger handler defaults")
		require.Empty(t, models)
	}
}

func TestScopedGeminiCatalogueExcludesClaudeAndUnrelatedGroups(t *testing.T) {
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		1: {{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"native-alias": "gemini-3.1-pro-preview"}}}},
		2: {{ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"gemini-alias": "gemini-3-pro-high", "claude-alias": "claude-opus-4-8"}}}},
		3: {{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"secret-model": "gemini-3.1-pro-preview"}}}},
	}}
	s := &GatewayService{accountRepo: repo}
	ctx := WithGatewayModelGroups(context.Background(), []*Group{{ID: 1, Status: StatusActive}, {ID: 2, Status: StatusActive}})
	models := s.GetAvailableGeminiModels(ctx, nil, false)
	require.Contains(t, models, "gemini-alias")
	require.Contains(t, models, "native-alias")
	require.NotContains(t, models, "claude-alias")
	require.NotContains(t, models, "secret-model")
	models = s.GetAvailableGeminiModels(ctx, nil, true)
	require.Contains(t, models, "gemini-alias")
	require.NotContains(t, models, "native-alias")
	require.NotContains(t, models, "claude-alias")
	require.NotContains(t, models, "secret-model")
	require.Zero(t, repo.listAllCalls.Load())
}

func TestScopedAllGroupCatalogueAppliesEachProviderChannelRestrictions(t *testing.T) {
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		1: {
			{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"shared": "claude-opus-5", "hidden": "claude-opus-5"}}},
			{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"shared": "gpt-6-sol", "openai-only": "gpt-6-sol"}}},
		},
	}}
	price := .1
	channels := &ChannelService{}
	cache := populateChannelCache([]Channel{{ID: 1, Status: StatusActive, GroupIDs: []int64{1}, RestrictModels: true, BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"shared"}, InputPrice: &price}, {Platform: PlatformOpenAI, Models: []string{"openai-only"}, InputPrice: &price}},
	}}, map[int64]string{1: "all"})
	channels.cache.Store(cache)
	s := &GatewayService{accountRepo: repo, channelService: channels}
	group := &Group{ID: 1, Platform: "all", Status: StatusActive, Hydrated: true}
	ctx := WithGatewayModelGroups(context.Background(), []*Group{group})
	require.Equal(t, []string{"openai-only", "shared"}, s.GetAvailableModels(ctx, nil, ""))
	require.Equal(t, []string{"openai-only"}, s.GetAvailableModels(ctx, nil, PlatformOpenAI))
	// Direct provider queries use the same platform-specific channel entries.
	effective := *group
	effective.Platform = PlatformAnthropic
	providerCtx := context.WithValue(context.Background(), ctxkey.Group, &effective)
	require.Equal(t, []string{"shared"}, s.GetAvailableModels(providerCtx, &group.ID, PlatformAnthropic))
}

func TestScopedAllGroupCatalogueMatchesAccountAdmissionRequirements(t *testing.T) {
	account := func(alias, accountType string, extra map[string]any) Account {
		return Account{Platform: PlatformOpenAI, Type: accountType, Status: StatusActive, Schedulable: true, Extra: extra,
			Credentials: map[string]any{"model_mapping": map[string]any{alias: "gpt-6-sol"}}}
	}
	allowed := account("allowed", AccountTypeOAuth, map[string]any{"privacy_mode": PrivacyModeTrainingOff})
	apiKey := account("api-key", AccountTypeAPIKey, map[string]any{"privacy_mode": PrivacyModeTrainingOff})
	privateUnset := account("privacy-unset", AccountTypeOAuth, nil)
	exhausted := account("quota-exhausted", AccountTypeOAuth, map[string]any{"privacy_mode": PrivacyModeTrainingOff, "quota_limit": 1.0, "quota_used": 1.0})
	paused := account("paused", AccountTypeOAuth, map[string]any{"privacy_mode": PrivacyModeTrainingOff})
	paused.Schedulable = false
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{1: {apiKey, privateUnset, exhausted, paused, allowed}}}
	group := &Group{ID: 1, Platform: PlatformAll, Status: StatusActive, Hydrated: true, RequireOAuthOnly: true, RequirePrivacySet: true}
	s := &GatewayService{accountRepo: repo}
	models := s.GetAvailableModels(WithGatewayModelGroups(context.Background(), []*Group{group}), nil, "")
	require.Contains(t, models, "allowed")
	for _, blocked := range []string{"api-key", "privacy-unset", "quota-exhausted", "paused"} {
		require.NotContains(t, models, blocked)
	}
	require.Zero(t, repo.listAllCalls.Load())
}
