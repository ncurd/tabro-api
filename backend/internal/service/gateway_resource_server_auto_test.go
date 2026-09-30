package service

import (
	"context"
	"crypto/rsa"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type gatewayAutoIdentityRepo struct {
	APIKeyRepository
	mu         sync.Mutex
	key        *APIKey
	lookups    int
	provisions int
	createErr  error
}

func (r *gatewayAutoIdentityRepo) GetByOIDCIdentity(_ context.Context, issuer, subject string) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	if r.key == nil || r.key.OIDCIssuer != issuer || r.key.OIDCSubject != subject {
		return nil, ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}

func (r *gatewayAutoIdentityRepo) BindOIDCIdentity(context.Context, int64, string, string) error {
	return errors.New("unexpected binding")
}

func (r *gatewayAutoIdentityRepo) AutoProvisionOIDCGatewayIdentity(_ context.Context, issuer, subject, keyValue string) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisions++
	if r.createErr != nil {
		return nil, r.createErr
	}
	if r.key == nil {
		r.key = &APIKey{ID: 42, UserID: 7, Key: keyValue, OIDCIssuer: issuer, OIDCSubject: subject, OIDCManaged: true, AuthBillingOnly: true, Status: StatusAPIKeyAuthBillingOnly, User: &User{ID: 7, Status: StatusActive, APIOnly: true}}
	}
	clone := *r.key
	return &clone, nil
}

func newAutoProvisionResourceServer(t *testing.T, repo *gatewayAutoIdentityRepo) (*GatewayResourceServer, string, *rsa.PrivateKey) {
	t.Helper()
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	cfg.AutoProvision = true
	cfg.RequireTenant = true
	cfg.TokenExchange.RequireActor = true
	cfg.TokenExchange.MaxDelegationDepth = 1
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	server := newGatewayResourceServerWithClient(cfg, svc, jwks.server.Client())
	server.billingCenterEnabled = true
	claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now().Truncate(time.Second))
	claims["act"] = map[string]any{"sub": gatewayOIDCTestActorID}
	return server, gatewayOIDCTestToken(t, key, "key-1", claims), key
}

func TestGatewayResourceServerAutomaticallyProvisionsOnlyAfterVerification(t *testing.T) {
	repo := &gatewayAutoIdentityRepo{}
	server, validToken, _ := newAutoProvisionResourceServer(t, repo)
	key, principal, err := server.Authenticate(context.Background(), validToken)
	require.NoError(t, err)
	require.Equal(t, int64(42), key.ID)
	require.True(t, key.AuthBillingOnly)
	require.True(t, principal.AuthBilled)
	_, _, err = server.Authenticate(context.Background(), validToken)
	require.NoError(t, err)
	require.Equal(t, 1, repo.provisions)

	// No identity lookup or write is permitted for a JWT that failed signature,
	// audience, scope, client, or delegation checks.
	invalidRepo := &gatewayAutoIdentityRepo{}
	invalidServer, _, signingKey := newAutoProvisionResourceServer(t, invalidRepo)
	baseClaims := gatewayOIDCTestClaims(invalidServer.cfg.IssuerURL, time.Now().Truncate(time.Second))
	baseClaims["act"] = map[string]any{"sub": gatewayOIDCTestActorID}
	for _, test := range []struct {
		name string
		edit func(jwt.MapClaims)
	}{
		{"wrong audience", func(c jwt.MapClaims) { c["aud"] = "some-other-api" }},
		{"missing scope", func(c jwt.MapClaims) { c["scope"] = "openid" }},
		{"unauthorized client", func(c jwt.MapClaims) { c["azp"] = "attacker" }},
		{"missing delegation", func(c jwt.MapClaims) { delete(c, "act") }},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := cloneGatewayOIDCTestClaims(baseClaims)
			test.edit(claims)
			_, _, err := invalidServer.Authenticate(context.Background(), gatewayOIDCTestToken(t, signingKey, "key-1", claims))
			require.Error(t, err)
		})
	}
	require.Zero(t, invalidRepo.lookups)
	require.Zero(t, invalidRepo.provisions)
}

func TestGatewayResourceServerAutoProvisionRequiresBillingAndPreservesRevocation(t *testing.T) {
	repo := &gatewayAutoIdentityRepo{}
	server, token, _ := newAutoProvisionResourceServer(t, repo)
	server.billingCenterEnabled = false
	_, _, err := server.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable)
	require.Zero(t, repo.lookups)
	require.Zero(t, repo.provisions)

	server.billingCenterEnabled = true
	repo.createErr = ErrGatewayOIDCIdentityNotBound // historical soft-deleted key
	_, _, err = server.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrGatewayOIDCIdentityNotBound)
	require.Equal(t, 1, repo.provisions)

	repo.createErr = nil
	repo.key = &APIKey{ID: 91, UserID: 10, OIDCIssuer: server.cfg.IssuerURL, OIDCSubject: "tabro-user-42", OIDCManaged: true, AuthBillingOnly: true, Status: StatusAPIKeyAuthBillingOnly, User: &User{ID: 10, Status: StatusActive, APIOnly: true}}
	server.cfg.AutoProvision = false
	_, _, err = server.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable, "an Auth-only binding must never fall back to local billing")
}

func TestGatewayResourceServerExistingManualBindingUsesAuthWhenAutoModeEnabled(t *testing.T) {
	repo := &gatewayAutoIdentityRepo{}
	server, token, _ := newAutoProvisionResourceServer(t, repo)
	repo.key = &APIKey{ID: 91, UserID: 10, OIDCIssuer: server.cfg.IssuerURL, OIDCSubject: "tabro-user-42", OIDCManaged: true, Status: StatusAPIKeyActive, User: &User{ID: 10, Status: StatusActive}}
	key, principal, err := server.Authenticate(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, int64(91), key.ID)
	require.True(t, principal.AuthBilled)
	require.Zero(t, repo.provisions)
}
