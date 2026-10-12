package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type accessKeyAuthorityStub struct {
	GatewayBillingAuthority
	result     bc.AccessKeyValidation
	err        error
	calls      int
	permission string
}

func (a *accessKeyAuthorityStub) ValidateAccessKey(_ context.Context, _ string, permission string) (bc.AccessKeyValidation, error) {
	a.calls++
	a.permission = permission
	return a.result, a.err
}

func TestCentralAccessKeyLiveAuthorizationBindsVerifiedIdentityWithoutStoringSecret(t *testing.T) {
	repo := &gatewayAutoIdentityRepo{}
	server, _, signingKey := newAutoProvisionResourceServer(t, repo)
	claims := gatewayOIDCTestClaims(server.cfg.IssuerURL, time.Now())
	claims["azp"] = "tabro-api"
	claims["act"] = map[string]any{"sub": "tabro-api"}
	claims["scope"] = "models.read"
	claims["access_key_id"] = "central-key"
	claims["access_key_version"] = int64(9007199254740993)
	proof := gatewayOIDCTestToken(t, signingKey, "key-1", claims)
	authority := &accessKeyAuthorityStub{result: bc.AccessKeyValidation{KeyID: "central-key", Version: 9007199254740993, UserID: claims["sub"].(string), TenantID: claims["tenant_id"].(string), OriginAppID: "tabro-api", SubjectAccessToken: proof}}
	server.apiKeyService.GatewayBilling = &GatewayBillingCoordinator{enabled: true, authority: authority}
	server.cfg.AutoProvision = false // Auth validation, not a local auto-mode flag, grants this path.
	for range 2 {
		key, principal, err := server.AuthenticateAccessKey(context.Background(), "tbrk_opaque-not-persisted", "api.read")
		require.NoError(t, err)
		require.True(t, key.OIDCManaged)
		require.True(t, principal.AuthBilled)
		require.Equal(t, proof, principal.SubjectProof)
		require.Equal(t, int64(9007199254740993), principal.AccessKeyVersion)
		require.NotEqual(t, "tbrk_opaque-not-persisted", key.Key)
		serialized, err := json.Marshal(principal)
		require.NoError(t, err)
		require.NotContains(t, string(serialized), proof)
	}
	require.Equal(t, 2, authority.calls)
	require.Equal(t, "api.read", authority.permission)
	require.Equal(t, 1, repo.provisions)
	// The direct proof cannot be replayed as an ordinary external OAuth token,
	// even if the static allowlist is accidentally expanded to include its client.
	server.cfg.AllowedClientIDs += ",tabro-api"
	_, err := server.Verify(context.Background(), proof)
	require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	authority.err = &bc.RemoteError{Status: http.StatusForbidden}
	_, _, err = server.AuthenticateAccessKey(context.Background(), "tbrk_opaque-not-persisted", "api.read")
	require.ErrorIs(t, err, ErrGatewayOIDCScopeDenied)
	require.Equal(t, 3, authority.calls)
	require.Equal(t, 1, repo.provisions)
}

func TestCentralAccessKeyRejectsMismatchedProofBeforeIdentityLookup(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(jwt.MapClaims)
	}{
		{"tenant", func(c jwt.MapClaims) { c["tenant_id"] = "other-tenant" }},
		{"user", func(c jwt.MapClaims) { c["sub"] = "other-user" }},
		{"key", func(c jwt.MapClaims) { c["access_key_id"] = "other-key" }},
		{"version", func(c jwt.MapClaims) { c["access_key_version"] = 2 }},
		{"missing key reference", func(c jwt.MapClaims) { delete(c, "access_key_id") }},
		{"ordinary app", func(c jwt.MapClaims) { c["azp"] = gatewayOIDCTestClientID }},
		{"missing actor", func(c jwt.MapClaims) { delete(c, "act") }},
		{"nested actor", func(c jwt.MapClaims) {
			c["act"] = map[string]any{"sub": "tabro-api", "act": map[string]any{"sub": "tabro-api"}}
		}},
		{"read cannot invoke", func(c jwt.MapClaims) { c["scope"] = "models.read" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &gatewayAutoIdentityRepo{}
			server, _, signingKey := newAutoProvisionResourceServer(t, repo)
			claims := gatewayOIDCTestClaims(server.cfg.IssuerURL, time.Now())
			claims["azp"], claims["act"] = "tabro-api", map[string]any{"sub": "tabro-api"}
			claims["access_key_id"], claims["access_key_version"] = "central-key", 1
			authority := &accessKeyAuthorityStub{result: bc.AccessKeyValidation{KeyID: "central-key", Version: 1, UserID: claims["sub"].(string), TenantID: claims["tenant_id"].(string), OriginAppID: "tabro-api"}}
			test.edit(claims)
			authority.result.SubjectAccessToken = gatewayOIDCTestToken(t, signingKey, "key-1", claims)
			server.apiKeyService.GatewayBilling = &GatewayBillingCoordinator{enabled: true, authority: authority}
			_, _, err := server.AuthenticateAccessKey(context.Background(), "tbrk_opaque", "api.invoke")
			require.Error(t, err)
			require.Zero(t, repo.lookups)
			require.Zero(t, repo.provisions)
		})
	}
}

func TestOIDCResourceServerDisablesLocalKeyCreationAndMutationWithoutBillingFlag(t *testing.T) {
	for _, auto := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Gateway.ResourceServer.Enabled = true
		cfg.Gateway.ResourceServer.AutoProvision = auto
		keys := NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
		_, err := keys.Create(context.Background(), 1, CreateAPIKeyRequest{Name: "bypass", CustomKey: new("custom-secret")})
		require.ErrorContains(t, err, "Auth account center")
		_, err = keys.Update(context.Background(), 1, 1, UpdateAPIKeyRequest{})
		require.ErrorContains(t, err, "Auth account center")
	}
}

func TestAuthKeyModeUsesLiveConnectionInsteadOfStaticDefaults(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       bool
		autoProvision bool
		want          bool
	}{
		{name: "non OIDC", want: false},
		{name: "resource server", enabled: true, want: true},
		{name: "auto provisioning", autoProvision: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.ResourceServer.Enabled = true
			cfg.Gateway.ResourceServer.AutoProvision = true
			keys := NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			current := &GatewayBillingCoordinator{owner: &gatewayBillingRuntime{}}
			current.connection.ResourceServer.Enabled = test.enabled
			current.connection.ResourceServer.AutoProvision = test.autoProvision
			keys.GatewayBilling = current
			required, err := keys.RequiresAuthCredentials(context.Background())
			require.NoError(t, err)
			require.Equal(t, test.want, required)
		})
	}
}
