//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func validAdminBillingConnection() OIDCBillingConnectionConfig {
	return OIDCBillingConnectionConfig{
		BillingCenter: config.BillingCenterConfig{Enabled: true, BaseURL: "https://auth.example", TokenURL: "https://auth.example/connect/token", ProducerClientID: "tabro-api", ClientSecret: "producer-secret", TimeoutSeconds: 15},
		ResourceServer: config.GatewayResourceServerConfig{
			Enabled: true, IssuerURL: "https://auth.example", Audience: "tabro-llm", RequiredScopes: "llm.invoke",
			AllowedClientIDs: "tabro-agent", AllowedSigningAlgs: "RS256", ClockSkewSeconds: 120, JWKSCacheTTLSeconds: 300,
			TenantClaim: "tenant_id", TokenExchange: config.GatewayTokenExchangeConfig{RequireActor: true, ActorClaim: "act", AllowedActorClientIDs: "tabro-agent", MaxDelegationDepth: 4},
		},
	}
}

func TestOIDCBillingConnectionAdminSaveOverridesYAMLAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.BillingCenter.Payments = config.BillingCenterPaymentsConfig{ProducerClientID: "payment-client", ClientSecret: "payment-secret"}
	before := *cfg
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, cfg)
	require.False(t, svc.IsOIDCBillingSupported())
	result, err := svc.UpdateOIDCBillingConnectionConfig(ctx, validAdminBillingConnection())
	require.NoError(t, err)
	require.Equal(t, "database", result.Source)
	require.True(t, result.ClientSecretConfigured)
	require.True(t, result.OIDCBillingSupported)
	require.Empty(t, result.BillingCenter.ClientSecret)
	require.Equal(t, before, *cfg, "startup configuration remains immutable")
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "producer-secret")
	require.NotContains(t, string(encoded), "payment-secret")
	require.NotContains(t, string(encoded), "payments")
	require.NotContains(t, repo.values[SettingKeyOIDCBillingConnection], "payment-secret")

	restarted := NewSettingService(repo, cfg)
	runtime, err := restarted.GetOIDCBillingConnectionConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "producer-secret", runtime.BillingCenter.ClientSecret)
	require.Equal(t, "payment-secret", runtime.BillingCenter.Payments.ClientSecret)
	require.True(t, restarted.IsOIDCBillingSupported())

	connection := validAdminBillingConnection()
	connection.BillingCenter.ClientSecret = ""
	connection.BillingCenter.TimeoutSeconds = 20
	_, err = restarted.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	runtime, err = restarted.GetOIDCBillingConnectionConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "producer-secret", runtime.BillingCenter.ClientSecret)
	require.Equal(t, 20, runtime.BillingCenter.TimeoutSeconds)
}

func TestOIDCBillingConnectionRejectsIncompleteTrustAndCredentialsWithoutSaving(t *testing.T) {
	changes := []struct {
		name   string
		change func(*OIDCBillingConnectionConfig)
	}{
		{"missing secret", func(c *OIDCBillingConnectionConfig) { c.BillingCenter.ClientSecret = "" }},
		{"HTTP authority", func(c *OIDCBillingConnectionConfig) { c.BillingCenter.BaseURL = "http://auth.example" }},
		{"missing audience", func(c *OIDCBillingConnectionConfig) { c.ResourceServer.Audience = "" }},
		{"missing client allowlist", func(c *OIDCBillingConnectionConfig) { c.ResourceServer.AllowedClientIDs = "" }},
		{"missing actor allowlist", func(c *OIDCBillingConnectionConfig) { c.ResourceServer.TokenExchange.AllowedActorClientIDs = "" }},
		{"unsigned algorithm", func(c *OIDCBillingConnectionConfig) { c.ResourceServer.AllowedSigningAlgs = "none" }},
		{"metadata downgrade", func(c *OIDCBillingConnectionConfig) { c.ResourceServer.JWKSURL = "http://auth.example/jwks" }},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			repo := &oidcOnlySettingRepo{values: map[string]string{}}
			connection := validAdminBillingConnection()
			test.change(&connection)
			_, err := NewSettingService(repo, &config.Config{}).UpdateOIDCBillingConnectionConfig(context.Background(), connection)
			require.ErrorContains(t, err, "OIDC_BILLING_CONNECTION_INVALID")
			require.Empty(t, repo.values)
		})
	}
}

func TestOIDCBillingConnectionCannotBreakEnabledBilling(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{SettingKeyOIDCBillingEnabled: "true", SettingKeyOIDCConnectIssuerURL: "https://auth.example"}}
	svc := NewSettingService(repo, &config.Config{})
	connection := validAdminBillingConnection()
	_, err := svc.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	saved := repo.values[SettingKeyOIDCBillingConnection]
	connection.BillingCenter.Enabled = false
	_, err = svc.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.ErrorContains(t, err, "OIDC_BILLING_CONNECTION_IN_USE")
	require.Equal(t, saved, repo.values[SettingKeyOIDCBillingConnection])
	connection = validAdminBillingConnection()
	connection.ResourceServer.IssuerURL = "https://other.example"
	_, err = svc.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.ErrorContains(t, err, "OIDC_BILLING_ISSUER_MISMATCH")
	require.Equal(t, saved, repo.values[SettingKeyOIDCBillingConnection])
}

func TestOIDCBillingConnectionUsesSavedIssuerForBillingPolicy(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Gateway.ResourceServer.IssuerURL = "https://obsolete.example"
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, cfg)
	_, err := svc.UpdateOIDCBillingConnectionConfig(ctx, validAdminBillingConnection())
	require.NoError(t, err)
	repo.values[SettingKeyOIDCBillingEnabled] = "true"
	repo.values[SettingKeyOIDCOnlyEnabled] = "true"
	repo.values[SettingKeyOIDCConnectEnabled] = "true"
	repo.values[SettingKeyOIDCConnectIssuerURL] = "https://auth.example"
	policy, err := svc.GetOIDCBillingPolicy(ctx)
	require.NoError(t, err)
	require.True(t, policy.Enabled)
}

func TestOIDCBillingConnectionFailsClosedOnInvalidStorageAndReadErrors(t *testing.T) {
	repo := &oidcOnlySettingRepo{values: map[string]string{SettingKeyOIDCBillingConnection: "not valid JSON"}}
	svc := NewSettingService(repo, oidcBillingTestConfig())
	_, err := svc.GetOIDCBillingConnectionConfig(context.Background())
	require.Error(t, err)
	require.False(t, svc.IsOIDCBillingSupported())
	repo.values[SettingKeyOIDCBillingConnection] = `{"billing_center":{"enabled":true},"resource_server":{"enabled":true}}`
	_, err = svc.GetOIDCBillingConnectionConfig(context.Background())
	require.Error(t, err, "partial saved configuration cannot inherit old YAML credentials")
	repo.err = errors.New("database unavailable")
	_, err = svc.GetOIDCBillingConnectionConfig(context.Background())
	require.Error(t, err)
	require.False(t, svc.IsOIDCBillingSupported())
}

func TestOIDCBillingConnectionValidatorRunsUnderGuardAndRejectsBeforePersistence(t *testing.T) {
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	var guard sync.Mutex
	svc.SetOIDCBillingConnectionUpdateGuard(func() func() { guard.Lock(); return guard.Unlock })
	svc.SetOIDCBillingConnectionValidator(func(context.Context, OIDCBillingConnectionConfig, OIDCBillingConnectionConfig) error {
		require.False(t, guard.TryLock(), "pending-operation validation runs inside the update guard")
		return errors.New("unsettled operations")
	})
	_, err := svc.UpdateOIDCBillingConnectionConfig(context.Background(), validAdminBillingConnection())
	require.ErrorContains(t, err, "unsettled operations")
	require.Empty(t, repo.values)
	require.True(t, guard.TryLock(), "a rejected save releases the guard")
	guard.Unlock()
}

type failingBillingConnectionWriteRepo struct {
	*oidcOnlySettingRepo
}

func (*failingBillingConnectionWriteRepo) SetMultiple(context.Context, map[string]string) error {
	return errors.New("write unavailable")
}

func TestOIDCBillingConnectionWriteFailureKeepsCommittedConfiguration(t *testing.T) {
	ctx := context.Background()
	repo := &oidcOnlySettingRepo{values: map[string]string{}}
	_, err := NewSettingService(repo, &config.Config{}).UpdateOIDCBillingConnectionConfig(ctx, validAdminBillingConnection())
	require.NoError(t, err)
	svc := NewSettingService(&failingBillingConnectionWriteRepo{repo}, &config.Config{})
	invalidated := false
	svc.SetOnUpdateCallback(func() { invalidated = true })
	connection := validAdminBillingConnection()
	connection.BillingCenter.ClientSecret = "rotated-secret"
	_, err = svc.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.ErrorContains(t, err, "write unavailable")
	require.False(t, invalidated)
	committed, err := svc.GetOIDCBillingConnectionConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "producer-secret", committed.BillingCenter.ClientSecret)
}
