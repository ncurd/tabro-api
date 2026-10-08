//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func oidcBillingTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.OIDC.Enabled = true
	cfg.OIDC.IssuerURL = "https://auth.example"
	cfg.BillingCenter = config.BillingCenterConfig{Enabled: true, BaseURL: "https://auth.example/billing", TokenURL: "https://auth.example/token", ProducerClientID: "tabro", ClientSecret: "secret"}
	cfg.Gateway.ResourceServer.Enabled = true
	cfg.Gateway.ResourceServer.IssuerURL = cfg.OIDC.IssuerURL
	return cfg
}

func oidcBillingTestSettings() *SystemSettings {
	return &SystemSettings{OIDCOnlyEnabled: true, OIDCConnectEnabled: true, OIDCConnectIssuerURL: "https://auth.example", OIDCBillingEnabled: true, OIDCBillingRateMultiplier: 2.5, OIDCBillingSettlementTime: "00:00", OIDCBillingSettlementTimezone: "Asia/Shanghai"}
}

func TestOIDCBillingSettingDefaultsAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, oidcBillingTestConfig())
	all, err := svc.GetAllSettings(ctx)
	require.NoError(t, err)
	require.False(t, all.OIDCBillingEnabled)
	require.True(t, all.OIDCBillingSupported)
	require.Equal(t, 1.0, all.OIDCBillingRateMultiplier)
	require.Equal(t, "00:00", all.OIDCBillingSettlementTime)
	require.Equal(t, "Asia/Shanghai", all.OIDCBillingSettlementTimezone)
	require.False(t, svc.IsOIDCBillingEnabled(ctx))

	settings := oidcBillingTestSettings()
	settings.OIDCBillingSettlementTime = "02:15"
	settings.PromoCodeEnabled = true
	settings.PurchaseSubscriptionEnabled = true
	settings.DefaultBalance = 100
	settings.DefaultSubscriptions = []DefaultSubscriptionSetting{{GroupID: 1, ValidityDays: 30}}
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	repo.values[SettingPaymentEnabled] = "true"
	require.Equal(t, "2.5", repo.values[SettingKeyOIDCBillingRateMultiplier])
	require.True(t, svc.IsOIDCBillingEnabled(ctx))
	require.Zero(t, svc.GetDefaultBalance(ctx))
	require.Empty(t, svc.GetDefaultSubscriptions(ctx))
	policy, err := svc.GetOIDCBillingPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, OIDCBillingPolicy{Enabled: true, RateMultiplier: 2.5, SettlementTime: "02:15", SettlementTimezone: "Asia/Shanghai"}, policy)

	public, err := svc.GetPublicSettings(ctx)
	require.NoError(t, err)
	require.True(t, public.OIDCBillingEnabled)
	require.True(t, public.OIDCBillingSupported)
	require.Equal(t, 2.5, public.OIDCBillingRateMultiplier)
	require.False(t, public.PromoCodeEnabled)
	require.False(t, svc.IsPromoCodeEnabled(ctx))
	require.False(t, public.PurchaseSubscriptionEnabled)
	require.False(t, public.PaymentEnabled)
	injected, err := svc.GetPublicSettingsForInjection(ctx)
	require.NoError(t, err)
	encoded, err := json.Marshal(injected)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"oidc_billing_settlement_time":"02:15"`)
	require.Contains(t, string(encoded), `"oidc_billing_enabled":true`)
}

func TestOIDCBillingRejectsInvalidPolicyWithoutWriting(t *testing.T) {
	tests := []struct {
		name, code string
		change     func(*SystemSettings, *config.Config)
	}{
		{"requires OIDC only", "OIDC_BILLING_REQUIRES_ONLY_MODE", func(s *SystemSettings, _ *config.Config) { s.OIDCOnlyEnabled = false }},
		{"requires OIDC login", "OIDC_BILLING_REQUIRES_ONLY_MODE", func(s *SystemSettings, _ *config.Config) { s.OIDCConnectEnabled = false }},
		{"requires connector", "OIDC_BILLING_NOT_SUPPORTED", func(_ *SystemSettings, c *config.Config) { c.BillingCenter.Enabled = false }},
		{"requires valid connector", "OIDC_BILLING_NOT_SUPPORTED", func(_ *SystemSettings, c *config.Config) { c.BillingCenter.ClientSecret = "" }},
		{"requires gateway", "OIDC_BILLING_NOT_SUPPORTED", func(_ *SystemSettings, c *config.Config) { c.Gateway.ResourceServer.Enabled = false }},
		{"requires matching issuer", "OIDC_BILLING_ISSUER_MISMATCH", func(s *SystemSettings, _ *config.Config) { s.OIDCConnectIssuerURL = "https://other.example" }},
		{"zero multiplier", "OIDC_BILLING_INVALID_MULTIPLIER", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingRateMultiplier = 0 }},
		{"negative multiplier", "OIDC_BILLING_INVALID_MULTIPLIER", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingRateMultiplier = -1 }},
		{"nan multiplier", "OIDC_BILLING_INVALID_MULTIPLIER", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingRateMultiplier = math.NaN() }},
		{"infinite multiplier", "OIDC_BILLING_INVALID_MULTIPLIER", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingRateMultiplier = math.Inf(1) }},
		{"invalid time", "OIDC_BILLING_INVALID_SETTLEMENT_TIME", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingSettlementTime = "24:00" }},
		{"time format", "OIDC_BILLING_INVALID_SETTLEMENT_TIME", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingSettlementTime = "1:00" }},
		{"invalid timezone", "OIDC_BILLING_INVALID_TIMEZONE", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingSettlementTimezone = "Mars/Olympus" }},
		{"host timezone", "OIDC_BILLING_INVALID_TIMEZONE", func(s *SystemSettings, _ *config.Config) { s.OIDCBillingSettlementTimezone = "Local" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, settings := oidcBillingTestConfig(), oidcBillingTestSettings()
			tt.change(settings, cfg)
			repo := &oidcOnlySettingRepo{values: map[string]string{}}
			err := NewSettingService(repo, cfg).UpdateSettings(context.Background(), settings)
			require.ErrorContains(t, err, tt.code)
			require.Empty(t, repo.values)
		})
	}
}

func TestOIDCBillingPolicyFailsClosedWhenConfigurationOrStorageChanges(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	cfg := oidcBillingTestConfig()
	svc := NewSettingService(repo, cfg)
	require.NoError(t, svc.UpdateSettings(ctx, oidcBillingTestSettings()))
	cfg.Gateway.ResourceServer.IssuerURL = "https://other.example"
	policy, err := svc.GetOIDCBillingPolicy(ctx)
	require.True(t, policy.Enabled)
	require.ErrorContains(t, err, "OIDC_BILLING_ISSUER_MISMATCH")
	require.True(t, svc.IsOIDCBillingEnabled(ctx))
	cfg.Gateway.ResourceServer.IssuerURL = "https://auth.example"
	repo.values[SettingKeyOIDCBillingRateMultiplier] = "NaN"
	_, err = svc.GetOIDCBillingPolicy(ctx)
	require.ErrorContains(t, err, "OIDC_BILLING_INVALID_MULTIPLIER")
	repo.err = errors.New("database unavailable")
	_, err = svc.GetOIDCBillingPolicy(ctx)
	require.Error(t, err)
	require.True(t, svc.IsOIDCBillingEnabled(ctx))
}

func TestOIDCBillingPolicyNextSettlementAt(t *testing.T) {
	policy := defaultOIDCBillingPolicy()
	tests := []struct{ input, want string }{
		{"2026-10-08T15:59:59Z", "2026-10-08T16:00:00Z"},
		{"2026-10-08T16:00:00Z", "2026-10-09T16:00:00Z"},
		{"2026-12-31T20:00:00Z", "2027-01-01T16:00:00Z"},
	}
	for _, tt := range tests {
		at, err := time.Parse(time.RFC3339, tt.input)
		require.NoError(t, err)
		due, err := policy.NextSettlementAt(at)
		require.NoError(t, err)
		require.Equal(t, tt.want, due.Format(time.RFC3339))
	}
	policy.SettlementTime = "04:30"
	policy.SettlementTimezone = "America/New_York"
	at, err := time.Parse(time.RFC3339, "2026-03-07T18:00:00Z")
	require.NoError(t, err)
	due, err := policy.NextSettlementAt(at)
	require.NoError(t, err)
	require.Equal(t, "2026-03-08T08:30:00Z", due.Format(time.RFC3339), "settlement uses the configured clock time across DST")
	policy.SettlementTime = "02:30"
	due, err = policy.NextSettlementAt(at)
	require.NoError(t, err)
	require.Equal(t, "2026-03-08T07:30:00Z", due.Format(time.RFC3339), "a nonexistent spring-forward time moves forward by the DST gap")
}
