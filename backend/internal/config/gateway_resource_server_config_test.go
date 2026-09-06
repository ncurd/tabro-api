package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadGatewayResourceServerDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()

	require.NoError(t, err)
	rs := cfg.Gateway.ResourceServer
	require.False(t, rs.Enabled)
	require.Equal(t, "llm-gateway-api", rs.Audience)
	require.Equal(t, "llm.invoke", rs.RequiredScopes)
	require.Equal(t, "RS256,ES256,PS256", rs.AllowedSigningAlgs)
	require.Equal(t, 120, rs.ClockSkewSeconds)
	require.Equal(t, 300, rs.JWKSCacheTTLSeconds)
	require.Equal(t, "tenant_id", rs.TenantClaim)
	require.False(t, rs.RequireTenant)
	require.False(t, rs.TokenExchange.RequireActor)
	require.Equal(t, "act", rs.TokenExchange.ActorClaim)
	require.Equal(t, 4, rs.TokenExchange.MaxDelegationDepth)
}

func TestLoadGatewayResourceServerFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_RESOURCE_SERVER_ENABLED", "true")
	t.Setenv("GATEWAY_RESOURCE_SERVER_ISSUER_URL", "https://identity.example.com/realms/production")
	t.Setenv("GATEWAY_RESOURCE_SERVER_DISCOVERY_URL", "https://identity.example.com/.well-known/openid-configuration")
	t.Setenv("GATEWAY_RESOURCE_SERVER_JWKS_URL", "https://identity.example.com/oauth2/jwks")
	t.Setenv("GATEWAY_RESOURCE_SERVER_AUDIENCE", "https://llm.example.com")
	t.Setenv("GATEWAY_RESOURCE_SERVER_REQUIRED_SCOPES", "llm.invoke,llm.stream")
	t.Setenv("GATEWAY_RESOURCE_SERVER_ALLOWED_CLIENT_IDS", "tabro-agent,tabro-worker")
	t.Setenv("GATEWAY_RESOURCE_SERVER_ALLOWED_SIGNING_ALGS", "RS256,PS256")
	t.Setenv("GATEWAY_RESOURCE_SERVER_CLOCK_SKEW_SECONDS", "45")
	t.Setenv("GATEWAY_RESOURCE_SERVER_JWKS_CACHE_TTL_SECONDS", "600")
	t.Setenv("GATEWAY_RESOURCE_SERVER_TENANT_CLAIM", "https://tabro.example/tenant_id")
	t.Setenv("GATEWAY_RESOURCE_SERVER_REQUIRE_TENANT", "true")
	t.Setenv("GATEWAY_RESOURCE_SERVER_TOKEN_EXCHANGE_REQUIRE_ACTOR", "true")
	t.Setenv("GATEWAY_RESOURCE_SERVER_TOKEN_EXCHANGE_ACTOR_CLAIM", "delegation.actor")
	t.Setenv("GATEWAY_RESOURCE_SERVER_TOKEN_EXCHANGE_ALLOWED_ACTOR_CLIENT_IDS", "token-broker")
	t.Setenv("GATEWAY_RESOURCE_SERVER_TOKEN_EXCHANGE_MAX_DELEGATION_DEPTH", "2")

	cfg, err := Load()
	require.NoError(t, err)

	rs := cfg.Gateway.ResourceServer
	require.True(t, rs.Enabled)
	require.Equal(t, "https://identity.example.com/realms/production", rs.IssuerURL)
	require.Equal(t, "https://identity.example.com/.well-known/openid-configuration", rs.DiscoveryURL)
	require.Equal(t, "https://identity.example.com/oauth2/jwks", rs.JWKSURL)
	require.Equal(t, "https://llm.example.com", rs.Audience)
	require.Equal(t, "llm.invoke,llm.stream", rs.RequiredScopes)
	require.Equal(t, "tabro-agent,tabro-worker", rs.AllowedClientIDs)
	require.Equal(t, "RS256,PS256", rs.AllowedSigningAlgs)
	require.Equal(t, 45, rs.ClockSkewSeconds)
	require.Equal(t, 600, rs.JWKSCacheTTLSeconds)
	require.Equal(t, "https://tabro.example/tenant_id", rs.TenantClaim)
	require.True(t, rs.RequireTenant)
	require.True(t, rs.TokenExchange.RequireActor)
	require.Equal(t, "delegation.actor", rs.TokenExchange.ActorClaim)
	require.Equal(t, "token-broker", rs.TokenExchange.AllowedActorClientIDs)
	require.Equal(t, 2, rs.TokenExchange.MaxDelegationDepth)
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
			Audience:            "llm-gateway-api",
			RequiredScopes:      "llm.invoke",
			AllowedClientIDs:    "tabro-web,tabro-agent",
			AllowedSigningAlgs:  "RS256,ES256,PS256",
			ClockSkewSeconds:    120,
			JWKSCacheTTLSeconds: 300,
			TenantClaim:         "tenant_id",
			TokenExchange: GatewayTokenExchangeConfig{
				ActorClaim:            "act",
				AllowedActorClientIDs: "tabro-token-exchange",
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
