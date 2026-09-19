//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type fallbackSettingsRepoStub struct {
	settingRepoStub
	initialized map[string]string
}

func (s *fallbackSettingsRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	s.initialized = settings
	return nil
}

func TestSettingService_InitializeDefaultSettings_UsesAvailableAnthropicFallback(t *testing.T) {
	repo := &fallbackSettingsRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	require.Equal(t, "claude-sonnet-4-6", repo.initialized[SettingKeyFallbackModelAnthropic])
}

func TestSettingService_AnthropicFallback_DefaultAndStoredValues(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "missing", want: "claude-sonnet-4-6"},
		{name: "empty", values: map[string]string{SettingKeyFallbackModelAnthropic: ""}, want: "claude-sonnet-4-6"},
		{name: "custom", values: map[string]string{SettingKeyFallbackModelAnthropic: "custom-claude"}, want: "custom-claude"},
		{name: "existing", values: map[string]string{SettingKeyFallbackModelAnthropic: "claude-3-5-sonnet-20241022"}, want: "claude-3-5-sonnet-20241022"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &settingRepoStub{values: tt.values}
			svc := NewSettingService(repo, &config.Config{})

			require.Equal(t, tt.want, svc.GetFallbackModel(context.Background(), PlatformAnthropic))
			require.Equal(t, tt.want, svc.parseSettings(tt.values).FallbackModelAnthropic)
		})
	}
}

func TestSettingService_InitializeDefaultSettings_PreservesExistingFallback(t *testing.T) {
	repo := &fallbackSettingsRepoStub{settingRepoStub: settingRepoStub{values: map[string]string{
		SettingKeyRegistrationEnabled:    "true",
		SettingKeyFallbackModelAnthropic: "custom-claude",
	}}}
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	require.Nil(t, repo.initialized)
	require.Equal(t, "custom-claude", svc.GetFallbackModel(context.Background(), PlatformAnthropic))
}
