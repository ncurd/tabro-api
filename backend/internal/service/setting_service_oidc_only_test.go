//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type oidcOnlySettingRepo struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *oidcOnlySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *oidcOnlySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *oidcOnlySettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	values := make(map[string]string, len(r.values))
	for key, value := range r.values {
		values[key] = value
	}
	return values, nil
}

func (r *oidcOnlySettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	if r.err != nil {
		return r.err
	}
	if r.values == nil {
		r.values = make(map[string]string)
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func TestOIDCOnlySettingDefaultsOffForExistingInstallations(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{SettingKeyRegistrationEnabled: "true"}}
	svc := NewSettingService(repo, &config.Config{})

	require.False(t, svc.IsOIDCOnlyEnabled(ctx))
	all, err := svc.GetAllSettings(ctx)
	require.NoError(t, err)
	require.False(t, all.OIDCOnlyEnabled)
	public, err := svc.GetPublicSettings(ctx)
	require.NoError(t, err)
	require.False(t, public.OIDCOnlyEnabled)

	repo.err = errors.New("settings unavailable")
	require.True(t, svc.IsOIDCOnlyEnabled(ctx), "a read error must not re-open local login")
}

func TestOIDCOnlySettingPersistsAndAppearsInPublicSettings(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.UpdateSettings(ctx, &SystemSettings{
		RegistrationEnabled: true,
		OIDCOnlyEnabled:     true,
		OIDCConnectEnabled:  true,
	}))
	require.Equal(t, "true", repo.values[SettingKeyOIDCOnlyEnabled])
	require.Equal(t, "true", repo.values[SettingKeyRegistrationEnabled], "OIDC-only mode must not close OIDC self-registration")

	reloaded := NewSettingService(repo, &config.Config{})
	require.True(t, reloaded.IsOIDCOnlyEnabled(ctx))
	all, err := reloaded.GetAllSettings(ctx)
	require.NoError(t, err)
	require.True(t, all.OIDCOnlyEnabled)
	public, err := reloaded.GetPublicSettings(ctx)
	require.NoError(t, err)
	require.True(t, public.OIDCOnlyEnabled)

	injected, err := reloaded.GetPublicSettingsForInjection(ctx)
	require.NoError(t, err)
	jsonBody, err := json.Marshal(injected)
	require.NoError(t, err)
	require.Contains(t, string(jsonBody), `"oidc_only_enabled":true`)

	require.NoError(t, reloaded.UpdateSettings(ctx, &SystemSettings{RegistrationEnabled: true}))
	require.Equal(t, "false", repo.values[SettingKeyOIDCOnlyEnabled])
	require.False(t, reloaded.IsOIDCOnlyEnabled(ctx))
}
