package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLoadGatewayResourceServerDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()

	require.NoError(t, err)
	rs := cfg.Gateway.ResourceServer
	require.False(t, rs.Enabled)
	require.Equal(t, "tabro-llm", rs.Audience)
	require.Equal(t, "llm.invoke", rs.RequiredScopes)
	require.Empty(t, rs.AllowedClientIDs)
	require.Equal(t, "RS256,ES256,PS256", rs.AllowedSigningAlgs)
	require.Equal(t, 120, rs.ClockSkewSeconds)
	require.Equal(t, 300, rs.JWKSCacheTTLSeconds)
	require.Equal(t, "tenant_id", rs.TenantClaim)
	require.False(t, rs.RequireTenant)
	require.True(t, rs.TokenExchange.RequireActor)
	require.Equal(t, "act", rs.TokenExchange.ActorClaim)
	require.Empty(t, rs.TokenExchange.AllowedActorClientIDs)
	require.Equal(t, 4, rs.TokenExchange.MaxDelegationDepth)
}

func TestLoadGatewayResourceServerFromYAML(t *testing.T) {
	resetViperWithJWTSecret(t)
	tempDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(`
gateway:
  resource_server:
    enabled: true
    issuer_url: "https://identity.example.com/realms/production"
    discovery_url: "https://identity.example.com/.well-known/openid-configuration"
    jwks_url: "https://identity.example.com/oauth2/jwks"
    audience: "https://llm.example.com"
    required_scopes: "llm.invoke,llm.stream"
    allowed_client_ids: "tabro-agent,tabro-drama"
    allowed_signing_algs: "RS256,PS256"
    clock_skew_seconds: 45
    jwks_cache_ttl_seconds: 600
    tenant_claim: "https://tabro.example/tenant_id"
    require_tenant: true
    token_exchange:
      require_actor: true
      actor_claim: "delegation.actor"
      allowed_actor_client_ids: "tabro-agent"
      max_delegation_depth: 2
`), 0o600))
	t.Setenv("DATA_DIR", tempDir)

	cfg, err := Load()
	require.NoError(t, err)

	rs := cfg.Gateway.ResourceServer
	require.True(t, rs.Enabled)
	require.Equal(t, "https://identity.example.com/realms/production", rs.IssuerURL)
	require.Equal(t, "https://identity.example.com/.well-known/openid-configuration", rs.DiscoveryURL)
	require.Equal(t, "https://identity.example.com/oauth2/jwks", rs.JWKSURL)
	require.Equal(t, "https://llm.example.com", rs.Audience)
	require.Equal(t, "llm.invoke,llm.stream", rs.RequiredScopes)
	require.Equal(t, "tabro-agent,tabro-drama", rs.AllowedClientIDs)
	require.Equal(t, "RS256,PS256", rs.AllowedSigningAlgs)
	require.Equal(t, 45, rs.ClockSkewSeconds)
	require.Equal(t, 600, rs.JWKSCacheTTLSeconds)
	require.Equal(t, "https://tabro.example/tenant_id", rs.TenantClaim)
	require.True(t, rs.RequireTenant)
	require.True(t, rs.TokenExchange.RequireActor)
	require.Equal(t, "delegation.actor", rs.TokenExchange.ActorClaim)
	require.Equal(t, "tabro-agent", rs.TokenExchange.AllowedActorClientIDs)
	require.Equal(t, 2, rs.TokenExchange.MaxDelegationDepth)
}

func TestLoadGatewayResourceServerRejectsEnvironmentOverride(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_RESOURCE_SERVER_ENABLED", "true")

	_, err := Load()

	require.Error(t, err)
	require.Contains(t, err.Error(), "gateway.resource_server must be configured in config.yaml")
	require.Contains(t, err.Error(), "GATEWAY_RESOURCE_SERVER_ENABLED")
}

func TestDeployConfigExampleContainsCompleteFailClosedResourceServerPolicy(t *testing.T) {
	example := viper.New()
	example.SetConfigFile(filepath.Join("..", "..", "..", "deploy", "config.example.yaml"))
	require.NoError(t, example.ReadInConfig())

	keys := []string{
		"enabled",
		"issuer_url",
		"discovery_url",
		"jwks_url",
		"audience",
		"required_scopes",
		"allowed_client_ids",
		"allowed_signing_algs",
		"clock_skew_seconds",
		"jwks_cache_ttl_seconds",
		"tenant_claim",
		"require_tenant",
		"token_exchange.require_actor",
		"token_exchange.actor_claim",
		"token_exchange.allowed_actor_client_ids",
		"token_exchange.max_delegation_depth",
	}
	for _, key := range keys {
		require.Truef(t, example.IsSet("gateway.resource_server."+key), "missing gateway.resource_server.%s", key)
	}

	var rs GatewayResourceServerConfig
	require.NoError(t, example.UnmarshalKey("gateway.resource_server", &rs))
	require.False(t, rs.Enabled)
	require.Empty(t, rs.IssuerURL)
	require.Empty(t, rs.AllowedClientIDs)
	require.True(t, rs.TokenExchange.RequireActor)
	require.Empty(t, rs.TokenExchange.AllowedActorClientIDs)
}

func TestValidateGatewayResourceServerConfig(t *testing.T) {
	newValidConfig := func(t *testing.T) *Config {
		t.Helper()
		resetViperWithJWTSecret(t)
		cfg, err := Load()
		require.NoError(t, err)
		cfg.Gateway.ResourceServer = GatewayResourceServerConfig{
			Enabled:             true,
			IssuerURL:           "https://identity.example.com/realms/tabro",
			DiscoveryURL:        "https://identity.example.com/realms/tabro/.well-known/openid-configuration",
			JWKSURL:             "https://identity.example.com/realms/tabro/protocol/openid-connect/certs",
			Audience:            "tabro-llm",
			RequiredScopes:      "llm.invoke",
			AllowedClientIDs:    "tabro-agent,tabro-drama",
			AllowedSigningAlgs:  "RS256,ES256,PS256",
			ClockSkewSeconds:    120,
			JWKSCacheTTLSeconds: 300,
			TenantClaim:         "tenant_id",
			TokenExchange: GatewayTokenExchangeConfig{
				ActorClaim:            "act",
				AllowedActorClientIDs: "tabro-agent",
				MaxDelegationDepth:    4,
			},
		}
		return cfg
	}

	t.Run("valid", func(t *testing.T) {
		require.NoError(t, newValidConfig(t).Validate())
	})

	tests := []struct {
		name    string
		mutate  func(*GatewayResourceServerConfig)
		wantErr string
	}{
		{
			name:    "issuer required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.IssuerURL = "" },
			wantErr: "issuer_url is required",
		},
		{
			name:    "audience required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.Audience = "" },
			wantErr: "audience is required",
		},
		{
			name:    "scope required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.RequiredScopes = "" },
			wantErr: "required_scopes is required",
		},
		{
			name:    "scope list must contain an item",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.RequiredScopes = ", ; ," },
			wantErr: "required_scopes is required",
		},
		{
			name:    "scope token must be safe",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.RequiredScopes = `llm.invoke"bad` },
			wantErr: "contains invalid scope",
		},
		{
			name:    "client allowlist required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.AllowedClientIDs = "" },
			wantErr: "allowed_client_ids is required",
		},
		{
			name:    "client allowlist must contain an item",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.AllowedClientIDs = ", ; ," },
			wantErr: "allowed_client_ids is required",
		},
		{
			name:    "signing algorithms required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.AllowedSigningAlgs = "" },
			wantErr: "allowed_signing_algs is required",
		},
		{
			name:    "signing algorithm list must contain an item",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.AllowedSigningAlgs = ", ; ," },
			wantErr: "allowed_signing_algs is required",
		},
		{
			name:    "issuer must be absolute http url",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.IssuerURL = "identity.example.com/realm" },
			wantErr: "issuer_url invalid",
		},
		{
			name:    "issuer must not contain userinfo",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.IssuerURL = "https://user@identity.example.com/realm" },
			wantErr: "issuer_url invalid",
		},
		{
			name:    "issuer must not contain query",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.IssuerURL = "https://identity.example.com/realm?tenant=one" },
			wantErr: "issuer_url invalid",
		},
		{
			name:    "discovery url must be absolute http url",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.DiscoveryURL = "file:///tmp/discovery.json" },
			wantErr: "discovery_url invalid",
		},
		{
			name: "https issuer cannot downgrade discovery",
			mutate: func(rs *GatewayResourceServerConfig) {
				rs.DiscoveryURL = "http://identity.example.com/.well-known/openid-configuration"
			},
			wantErr: "discovery_url invalid",
		},
		{
			name: "discovery url must not contain userinfo",
			mutate: func(rs *GatewayResourceServerConfig) {
				rs.DiscoveryURL = "https://user@identity.example.com/.well-known/openid-configuration"
			},
			wantErr: "discovery_url invalid",
		},
		{
			name:    "jwks url must be absolute http url",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.JWKSURL = "//identity.example.com/jwks" },
			wantErr: "jwks_url invalid",
		},
		{
			name:    "https issuer cannot downgrade jwks",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.JWKSURL = "http://identity.example.com/jwks" },
			wantErr: "jwks_url invalid",
		},
		{
			name:    "jwks url must not contain userinfo",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.JWKSURL = "https://user@identity.example.com/jwks" },
			wantErr: "jwks_url invalid",
		},
		{
			name:    "negative clock skew rejected",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.ClockSkewSeconds = -1 },
			wantErr: "clock_skew_seconds must be between 0 and 600",
		},
		{
			name:    "excessive clock skew rejected",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.ClockSkewSeconds = 601 },
			wantErr: "clock_skew_seconds must be between 0 and 600",
		},
		{
			name:    "positive jwks ttl required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.JWKSCacheTTLSeconds = 0 },
			wantErr: "jwks_cache_ttl_seconds must be positive",
		},
		{
			name:    "symmetric signing algorithm rejected",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.AllowedSigningAlgs = "RS256,HS256" },
			wantErr: "contains unsupported algorithm",
		},
		{
			name: "tenant claim required when tenant is mandatory",
			mutate: func(rs *GatewayResourceServerConfig) {
				rs.RequireTenant = true
				rs.TenantClaim = ""
			},
			wantErr: "tenant_claim is required",
		},
		{
			name:    "actor claim required",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.TokenExchange.ActorClaim = "" },
			wantErr: "actor_claim is required",
		},
		{
			name:    "delegation depth must be positive",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.TokenExchange.MaxDelegationDepth = 0 },
			wantErr: "max_delegation_depth must be between 1 and 8",
		},
		{
			name:    "delegation depth is bounded",
			mutate:  func(rs *GatewayResourceServerConfig) { rs.TokenExchange.MaxDelegationDepth = 9 },
			wantErr: "max_delegation_depth must be between 1 and 8",
		},
		{
			name: "required actor must have allowlist",
			mutate: func(rs *GatewayResourceServerConfig) {
				rs.TokenExchange.RequireActor = true
				rs.TokenExchange.AllowedActorClientIDs = ""
			},
			wantErr: "allowed_actor_client_ids is required",
		},
		{
			name: "required actor allowlist must contain an item",
			mutate: func(rs *GatewayResourceServerConfig) {
				rs.TokenExchange.RequireActor = true
				rs.TokenExchange.AllowedActorClientIDs = ", ; ,"
			},
			wantErr: "allowed_actor_client_ids is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newValidConfig(t)
			tt.mutate(&cfg.Gateway.ResourceServer)

			err := cfg.Validate()

			require.Error(t, err)
			require.Truef(t, strings.Contains(err.Error(), tt.wantErr), "error %q does not contain %q", err, tt.wantErr)
		})
	}
}
