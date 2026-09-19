package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

func accountCatalogModelIDs(models []openai.Model) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestAccountAvailableOpenAIModels_RespectsAuthenticationLifecycle(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: accountType}
			ids := accountCatalogModelIDs(account.AvailableOpenAIModels())
			for _, model := range []string{
				"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5",
				"gpt-image-2.5-sunburst", "gpt-image-2.5-flare",
			} {
				require.Contains(t, ids, model)
			}
			for _, model := range []string{"gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini", "gpt-5.2-codex"} {
				require.NotContains(t, ids, model)
			}
			for _, model := range []string{"gpt-5", "gpt-5.1", "gpt-5.2", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini"} {
				if accountType == AccountTypeOAuth {
					require.NotContains(t, ids, model)
				} else {
					require.Contains(t, ids, model)
				}
			}
			if accountType == AccountTypeOAuth {
				require.NotContains(t, ids, "gpt-5.3-codex-spark")
			}
		})
	}
}

func TestAccountAvailableOpenAIModels_FiltersTargetsAndPreservesMappedAliases(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"unavailable": "gpt-5.4-mini",
			"gpt-5.4":     "gpt-5.6-terra",
			"image":       "gpt-image-2.5-sunburst",
		}},
	}
	require.Equal(t, []string{"gpt-5.4", "image"}, accountCatalogModelIDs(account.AvailableOpenAIModels()))

	account.Extra = map[string]any{"openai_passthrough": true}
	ids := accountCatalogModelIDs(account.AvailableOpenAIModels())
	require.Contains(t, ids, "gpt-5.6-terra")
	require.NotContains(t, ids, "gpt-5.4")
	require.NotContains(t, ids, "unavailable")
}

func TestAccountIsRetiredModel_RespectsProviderChannels(t *testing.T) {
	for _, tt := range []struct {
		name    string
		account Account
		model   string
		retired bool
	}{
		{name: "claude_direct_api", account: Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, model: "claude-3-5-sonnet-20241022", retired: true},
		{name: "claude_direct_oauth", account: Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, model: "claude-opus-4-1-20250805", retired: true},
		{name: "claude_bedrock", account: Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, model: "claude-3-5-sonnet-20241022"},
		{name: "claude_antigravity", account: Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}, model: "claude-3-5-sonnet-20241022"},
		{name: "claude_active", account: Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, model: "claude-sonnet-4-6"},
		{name: "gemini_retired", account: Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}, model: "gemini-2.0-flash", retired: true},
		{name: "gemini_retired_snapshot", account: Account{Platform: PlatformGemini, Type: AccountTypeOAuth}, model: "gemini-2.0-flash-001", retired: true},
		{name: "gemini_active", account: Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}, model: "gemini-2.5-flash"},
		{name: "gemini_supported_alias", account: Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}, model: "gemini-3-pro-preview"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.retired, tt.account.IsRetiredModel(tt.model))
		})
	}
}

func TestGetAvailableModels_OpenAIDefaultsFollowAccountTypes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		accounts []Account
		apiKey   bool
	}{
		{name: "oauth_only", accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}},
		{name: "mixed", accounts: []Account{
			{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		}, apiKey: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			groupID := int64(91)
			repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: tt.accounts}}
			svc := &GatewayService{accountRepo: repo}
			ids := svc.GetAvailableModels(context.Background(), &groupID, PlatformOpenAI)
			require.NotNil(t, ids)
			require.Contains(t, ids, "gpt-5.6-luna")
			require.Contains(t, ids, "gpt-image-2.5-sunburst")
			for _, model := range []string{"gpt-5.4", "gpt-5.4-mini"} {
				if tt.apiKey {
					require.Contains(t, ids, model)
				} else {
					require.NotContains(t, ids, model)
				}
			}
			require.NotContains(t, ids, "gpt-5.1-codex")
		})
	}
}

func TestGetAvailableModels_FiltersUpstreamTargetsAndPreservesAliases(t *testing.T) {
	for _, tt := range []struct {
		platform string
		retired  string
		current  string
	}{
		{platform: PlatformOpenAI, retired: "gpt-5.4-mini", current: "gpt-5.6-luna"},
		{platform: PlatformAnthropic, retired: "claude-3-5-sonnet-20241022", current: "claude-sonnet-4-6"},
		{platform: PlatformGemini, retired: "gemini-2.0-flash", current: "gemini-2.5-flash"},
	} {
		t.Run(tt.platform, func(t *testing.T) {
			repo := &modelsListAccountRepoStub{all: []Account{{
				ID: 1, Platform: tt.platform, Type: AccountTypeOAuth,
				Credentials: map[string]any{"model_mapping": map[string]any{
					"broken-alias": tt.retired,
					tt.retired:     tt.current,
				}},
			}}}
			svc := &GatewayService{accountRepo: repo}
			require.Equal(t, []string{tt.retired}, svc.GetAvailableModels(context.Background(), nil, tt.platform))
		})
	}
}

func TestGetAvailableModels_RetiredOnlyWhitelistCachesEmptyCatalog(t *testing.T) {
	for _, tt := range []struct {
		platform string
		retired  string
	}{
		{platform: PlatformOpenAI, retired: "gpt-5.4-mini"},
		{platform: PlatformAnthropic, retired: "claude-opus-4-1-20250805"},
		{platform: PlatformGemini, retired: "gemini-2.0-flash"},
	} {
		t.Run(tt.platform, func(t *testing.T) {
			repo := &modelsListAccountRepoStub{all: []Account{{
				ID: 1, Platform: tt.platform, Type: AccountTypeOAuth,
				Credentials: map[string]any{"model_mapping": map[string]any{tt.retired: tt.retired}},
			}}}
			svc := &GatewayService{
				accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute,
			}
			for i := 0; i < 2; i++ {
				models := svc.GetAvailableModels(context.Background(), nil, tt.platform)
				require.NotNil(t, models, "an explicitly empty catalog must not trigger handler defaults")
				require.Empty(t, models)
			}
			require.Equal(t, int64(1), repo.listAllCalls.Load(), "the second empty result should use the cache")
		})
	}
}

func TestGetAvailableModels_MixedUnmappedPlatformsKeepTheirDefaults(t *testing.T) {
	groupID := int64(92)
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey},
	}}}
	svc := &GatewayService{accountRepo: repo}
	models := svc.GetAvailableModels(context.Background(), &groupID, "")
	for _, model := range []string{"gpt-5.6-luna", "gpt-image-2.5-sunburst", "claude-fable-5-1", "claude-sonnet-4-6", "gemini-2.5-flash", "gemini-3.1-flash-image"} {
		require.Contains(t, models, model)
	}
	for _, model := range []string{"gpt-5.4", "claude-3-5-sonnet-20241022", "gemini-2.0-flash"} {
		require.NotContains(t, models, model)
	}

	openAIModels := svc.GetAvailableModels(context.Background(), &groupID, PlatformOpenAI)
	require.Contains(t, openAIModels, "gpt-5.6-luna")
	require.NotContains(t, openAIModels, "claude-fable-5-1")
	require.NotContains(t, openAIModels, "gemini-2.5-flash")
}

func TestAccountIsRetiredModel_BedrockEOLPreservesOtherChannelModels(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	for _, model := range []string{
		"claude-3-5-haiku-20241022",
		"anthropic.claude-3-5-haiku-20241022-v1:0",
		"us.anthropic.claude-3-5-haiku-20241022-v1:0",
	} {
		require.True(t, account.IsRetiredModel(model), model)
	}
	for _, model := range []string{
		"claude-haiku-4-5-20251001",
		"us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"claude-sonnet-4-20250514",
		"us.anthropic.claude-sonnet-4-20250514-v1:0",
		"us.anthropic.claude-opus-4-1-20250805-v1:0",
		"claude-3-5-sonnet-20241022",
	} {
		require.False(t, account.IsRetiredModel(model), "future and region-specific retirements must remain configurable: %s", model)
	}
}
