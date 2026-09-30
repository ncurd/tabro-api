//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type settingPublicRepoStub struct {
	values map[string]string
}

func (s *settingPublicRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *settingPublicRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *settingPublicRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *settingPublicRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *settingPublicRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *settingPublicRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *settingPublicRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func TestSettingService_GetPublicSettings_ExposesRegistrationEmailSuffixWhitelist(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			SettingKeyRegistrationEnabled:              "true",
			SettingKeyEmailVerifyEnabled:               "true",
			SettingKeyRegistrationEmailSuffixWhitelist: `["@EXAMPLE.com"," @foo.bar ","@invalid_domain",""]`,
		},
	}
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"@example.com", "@foo.bar"}, settings.RegistrationEmailSuffixWhitelist)
}

func TestSettingService_GetPublicSettings_ExposesTablePreferences(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{
			SettingKeyTableDefaultPageSize: "50",
			SettingKeyTablePageSizeOptions: "[20,50,100]",
		},
	}
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 50, settings.TableDefaultPageSize)
	require.Equal(t, []int{20, 50, 100}, settings.TablePageSizeOptions)
}

func TestSettingServiceInternalOnlyCannotBeDisabledByDatabasePublicSettings(t *testing.T) {
	repo := &settingPublicRepoStub{values: map[string]string{SettingKeyRegistrationEnabled: "true", SettingKeyBackendModeEnabled: "false", SettingKeyPasswordResetEnabled: "true", SettingKeyPurchaseSubscriptionEnabled: "true"}}
	svc := NewSettingService(repo, &config.Config{Deployment: config.DeploymentConfig{InternalOnly: true, AccountCenterURL: "https://auth.example/account"}})
	require.True(t, svc.IsBackendModeEnabled(context.Background()))
	require.False(t, svc.IsRegistrationEnabled(context.Background()))
	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.InternalOnly)
	require.True(t, settings.BackendModeEnabled)
	require.False(t, settings.RegistrationEnabled)
	require.False(t, settings.PasswordResetEnabled)
	require.False(t, settings.PurchaseSubscriptionEnabled)
	require.Equal(t, "https://auth.example/account", settings.AccountCenterURL)
	injected, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	body, err := json.Marshal(injected)
	require.NoError(t, err)
	require.Contains(t, string(body), `"internal_only":true`)
	require.Contains(t, string(body), `"account_center_url":"https://auth.example/account"`)
}
