package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	gatewayOIDCTestAudience = "llm-gateway-api"
	gatewayOIDCTestClientID = "tabro-agent"
	gatewayOIDCTestActorID  = "tabro-token-exchange"
)

type gatewayOIDCTestJWKS struct {
	server *httptest.Server
	hits   atomic.Int64

	mu   sync.RWMutex
	keys []gatewayOIDCJWK
}

func newGatewayOIDCTestJWKS(t *testing.T, keys ...gatewayOIDCJWK) *gatewayOIDCTestJWKS {
	t.Helper()
	fixture := &gatewayOIDCTestJWKS{keys: append([]gatewayOIDCJWK(nil), keys...)}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}
		fixture.hits.Add(1)
		fixture.mu.RLock()
		set := gatewayOIDCJWKSet{Keys: append([]gatewayOIDCJWK(nil), fixture.keys...)}
		fixture.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(set); err != nil {
			http.Error(w, "failed to encode jwks", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *gatewayOIDCTestJWKS) replace(keys ...gatewayOIDCJWK) {
	f.mu.Lock()
	f.keys = append([]gatewayOIDCJWK(nil), keys...)
	f.mu.Unlock()
}

func gatewayOIDCTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func gatewayOIDCTestRSAJWK(kid string, key *rsa.PrivateKey) gatewayOIDCJWK {
	exponent := big.NewInt(int64(key.PublicKey.E)).Bytes()
	return gatewayOIDCJWK{
		Kty:    "RSA",
		Kid:    kid,
		Use:    "sig",
		Alg:    jwt.SigningMethodRS256.Alg(),
		KeyOps: []string{"verify"},
		N:      base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		E:      base64.RawURLEncoding.EncodeToString(exponent),
	}
}

func gatewayOIDCTestConfig(issuer, jwksURL string) config.GatewayResourceServerConfig {
	return config.GatewayResourceServerConfig{
		Enabled:             true,
		IssuerURL:           issuer,
		JWKSURL:             jwksURL,
		Audience:            gatewayOIDCTestAudience,
		RequiredScopes:      "llm.invoke",
		AllowedClientIDs:    gatewayOIDCTestClientID,
		AllowedSigningAlgs:  jwt.SigningMethodRS256.Alg(),
		ClockSkewSeconds:    0,
		JWKSCacheTTLSeconds: 300,
		TenantClaim:         "tenant_id",
		TokenExchange: config.GatewayTokenExchangeConfig{
			ActorClaim:            "act",
			AllowedActorClientIDs: gatewayOIDCTestActorID,
			MaxDelegationDepth:    4,
		},
	}
}

func gatewayOIDCTestClaims(issuer string, now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       issuer,
		"sub":       "tabro-user-42",
		"aud":       gatewayOIDCTestAudience,
		"exp":       now.Add(5 * time.Minute).Unix(),
		"nbf":       now.Add(-time.Minute).Unix(),
		"iat":       now.Add(-time.Minute).Unix(),
		"scope":     "openid llm.invoke",
		"azp":       gatewayOIDCTestClientID,
		"tenant_id": "tenant-acme",
	}
}

func gatewayOIDCTestToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

func cloneGatewayOIDCTestClaims(source jwt.MapClaims) jwt.MapClaims {
	cloned := make(jwt.MapClaims, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func TestGatewayResourceServerVerifyValidToken(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())

	now := time.Now().Truncate(time.Second)
	token := gatewayOIDCTestToken(t, key, "key-1", gatewayOIDCTestClaims(cfg.IssuerURL, now))
	principal, err := server.Verify(context.Background(), token)

	require.NoError(t, err)
	require.Equal(t, cfg.IssuerURL, principal.Issuer)
	require.Equal(t, "tabro-user-42", principal.Subject)
	require.Equal(t, "tenant-acme", principal.Tenant)
	require.Equal(t, gatewayOIDCTestClientID, principal.ClientID)
	require.Equal(t, now.Add(5*time.Minute), principal.ExpiresAt)
	require.ElementsMatch(t, []string{"openid", "llm.invoke"}, principal.Scopes)
	require.Empty(t, principal.ActorChain)
	require.EqualValues(t, 1, jwks.hits.Load())
}

func TestGatewayOIDCValidateEndpointForIssuerRejectsHTTPSDowngrade(t *testing.T) {
	require.NoError(t, gatewayOIDCValidateEndpointForIssuer("https://issuer.example.com", "https://issuer.example.com/jwks"))
	require.NoError(t, gatewayOIDCValidateEndpointForIssuer("http://issuer.test", "http://issuer.test/jwks"))
	require.Error(t, gatewayOIDCValidateEndpointForIssuer("https://issuer.example.com", "http://issuer.example.com/jwks"))
	require.Error(t, gatewayOIDCValidateEndpointForIssuer("https://issuer.example.com", "https://user@issuer.example.com/jwks"))
}

func TestGatewayResourceServerRejectsRedirectFromHTTPSToHTTPJWKS(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	plainJWKS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(gatewayOIDCJWKSet{
			Keys: []gatewayOIDCJWK{gatewayOIDCTestRSAJWK("key-1", key)},
		}); err != nil {
			http.Error(w, "failed to encode jwks", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(plainJWKS.Close)

	var secureRedirect *httptest.Server
	secureRedirect = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plainJWKS.URL+"/jwks", http.StatusFound)
	}))
	t.Cleanup(secureRedirect.Close)

	cfg := gatewayOIDCTestConfig(secureRedirect.URL, secureRedirect.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, secureRedirect.Client())
	token := gatewayOIDCTestToken(t, key, "key-1", gatewayOIDCTestClaims(cfg.IssuerURL, time.Now()))

	principal, err := server.Verify(context.Background(), token)
	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable)
}

func TestGatewayResourceServerRejectsIntermediateHTTPSDowngrade(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	var secureServer *httptest.Server
	var plainHits atomic.Int64
	plainBounce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, secureServer.URL+"/jwks", http.StatusFound)
	}))
	t.Cleanup(plainBounce.Close)

	secureServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, plainBounce.URL+"/bounce", http.StatusFound)
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(gatewayOIDCJWKSet{
				Keys: []gatewayOIDCJWK{gatewayOIDCTestRSAJWK("key-1", key)},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(secureServer.Close)

	cfg := gatewayOIDCTestConfig(secureServer.URL, secureServer.URL+"/start")
	server := newGatewayResourceServerWithClient(cfg, nil, secureServer.Client())
	token := gatewayOIDCTestToken(t, key, "key-1", gatewayOIDCTestClaims(cfg.IssuerURL, time.Now()))

	principal, err := server.Verify(context.Background(), token)
	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable)
	require.Zero(t, plainHits.Load(), "the client must reject the downgrade before issuing the HTTP request")
}

func TestGatewayOIDCParseJWKRejectsWeakRSAKey(t *testing.T) {
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	_, err = gatewayOIDCParseJWK(gatewayOIDCTestRSAJWK("weak", weakKey))
	require.ErrorContains(t, err, "at least 2048 bits")
}

func TestGatewayResourceServerVerifyRejectsInvalidRegisteredClaims(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())
	now := time.Now()

	tests := []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{
			name: "wrong issuer",
			mutate: func(claims jwt.MapClaims) {
				claims["iss"] = "https://other-issuer.example"
			},
		},
		{
			name: "wrong audience",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = "tabro-api"
			},
		},
		{
			name: "gateway plus tabro api audience is rejected",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = []string{gatewayOIDCTestAudience, "tabro-api"}
			},
		},
		{
			name: "expired",
			mutate: func(claims jwt.MapClaims) {
				claims["exp"] = now.Add(-time.Minute).Unix()
			},
		},
		{
			name: "not active yet",
			mutate: func(claims jwt.MapClaims) {
				claims["nbf"] = now.Add(time.Minute).Unix()
			},
		},
		{
			name: "missing subject",
			mutate: func(claims jwt.MapClaims) {
				delete(claims, "sub")
			},
		},
		{
			name: "missing expiration",
			mutate: func(claims jwt.MapClaims) {
				delete(claims, "exp")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := cloneGatewayOIDCTestClaims(gatewayOIDCTestClaims(cfg.IssuerURL, now))
			tt.mutate(claims)
			token := gatewayOIDCTestToken(t, key, "key-1", claims)

			principal, err := server.Verify(context.Background(), token)

			require.Nil(t, principal)
			require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
			require.False(t, errors.Is(err, ErrGatewayOIDCScopeDenied))
		})
	}
}

func TestGatewayResourceServerVerifyReturnsScopeDeniedSentinel(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())
	claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
	claims["scope"] = "openid profile"

	principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCScopeDenied)
	require.False(t, errors.Is(err, ErrGatewayOIDCTokenInvalid))
}

func TestGatewayResourceServerVerifyRejectsInvalidSignature(t *testing.T) {
	trustedKey := gatewayOIDCTestRSAKey(t)
	attackerKey := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", trustedKey))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())

	token := gatewayOIDCTestToken(t, attackerKey, "key-1", gatewayOIDCTestClaims(cfg.IssuerURL, time.Now()))
	principal, err := server.Verify(context.Background(), token)

	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
}

func TestGatewayResourceServerVerifyRejectsUnauthorizedClient(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())

	tests := []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{
			name: "client not allowlisted",
			mutate: func(claims jwt.MapClaims) {
				claims["azp"] = "untrusted-client"
			},
		},
		{
			name: "invalid client is not downgraded to insufficient scope",
			mutate: func(claims jwt.MapClaims) {
				claims["azp"] = "untrusted-client"
				claims["scope"] = "openid"
			},
		},
		{
			name: "azp and client id disagree",
			mutate: func(claims jwt.MapClaims) {
				claims["client_id"] = "another-client"
			},
		},
		{
			name: "client id missing",
			mutate: func(claims jwt.MapClaims) {
				delete(claims, "azp")
			},
		},
		{
			name: "present client id has invalid type",
			mutate: func(claims jwt.MapClaims) {
				claims["client_id"] = 42
			},
		},
		{
			name: "present client id is empty",
			mutate: func(claims jwt.MapClaims) {
				claims["client_id"] = "  "
			},
		},
		{
			name: "present azp has invalid type",
			mutate: func(claims jwt.MapClaims) {
				claims["azp"] = []string{gatewayOIDCTestClientID}
				claims["client_id"] = gatewayOIDCTestClientID
			},
		},
		{
			name: "present azp is empty",
			mutate: func(claims jwt.MapClaims) {
				claims["azp"] = ""
				claims["client_id"] = gatewayOIDCTestClientID
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
			tt.mutate(claims)
			principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

			require.Nil(t, principal)
			require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
		})
	}
}

func TestGatewayResourceServerVerifyTokenExchangeDelegation(t *testing.T) {
	key := gatewayOIDCTestRSAKey(t)
	jwks := newGatewayOIDCTestJWKS(t, gatewayOIDCTestRSAJWK("key-1", key))
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())

	t.Run("valid actor", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "urn:ietf:params:oauth:grant-type:token-exchange"
		claims["act"] = map[string]any{"client_id": gatewayOIDCTestActorID}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.NoError(t, err)
		require.Equal(t, []string{gatewayOIDCTestActorID}, principal.ActorChain)
	})

	t.Run("matching actor identity aliases are accepted", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token-exchange"
		claims["act"] = map[string]any{
			"client_id": gatewayOIDCTestActorID,
			"azp":       gatewayOIDCTestActorID,
			"sub":       gatewayOIDCTestActorID,
		}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.NoError(t, err)
		require.Equal(t, []string{gatewayOIDCTestActorID}, principal.ActorChain)
	})

	t.Run("conflicting actor identity aliases are rejected", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token-exchange"
		claims["act"] = map[string]any{
			"client_id": gatewayOIDCTestActorID,
			"azp":       "different-actor",
		}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})

	t.Run("malformed actor identity alias is rejected", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token-exchange"
		claims["act"] = map[string]any{
			"client_id": gatewayOIDCTestActorID,
			"sub":       42,
		}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})

	t.Run("nested actor identity conflict is rejected", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token-exchange"
		claims["act"] = map[string]any{
			"client_id": gatewayOIDCTestActorID,
			"act": map[string]any{
				"client_id": gatewayOIDCTestActorID,
				"sub":       "different-actor",
			},
		}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})

	t.Run("exchange grant requires actor", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["grant_type"] = "token-exchange"

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})

	t.Run("actor must be allowlisted", func(t *testing.T) {
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token_exchange"
		claims["act"] = map[string]any{"sub": "untrusted-actor"}

		principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})

	t.Run("delegation depth is bounded", func(t *testing.T) {
		depthCfg := cfg
		depthCfg.TokenExchange.MaxDelegationDepth = 1
		depthServer := newGatewayResourceServerWithClient(depthCfg, nil, jwks.server.Client())
		claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())
		claims["gty"] = "token-exchange"
		claims["act"] = map[string]any{
			"client_id": gatewayOIDCTestActorID,
			"act":       map[string]any{"client_id": gatewayOIDCTestActorID},
		}

		principal, err := depthServer.Verify(context.Background(), gatewayOIDCTestToken(t, key, "key-1", claims))

		require.Nil(t, principal)
		require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	})
}

func TestGatewayResourceServerJWKSCacheRotationAndUnknownKID(t *testing.T) {
	key1 := gatewayOIDCTestRSAKey(t)
	key2 := gatewayOIDCTestRSAKey(t)
	unknownKey := gatewayOIDCTestRSAKey(t)
	jwk1 := gatewayOIDCTestRSAJWK("key-1", key1)
	jwk2 := gatewayOIDCTestRSAJWK("key-2", key2)
	jwks := newGatewayOIDCTestJWKS(t, jwk1)
	cfg := gatewayOIDCTestConfig(jwks.server.URL, jwks.server.URL+"/jwks")
	server := newGatewayResourceServerWithClient(cfg, nil, jwks.server.Client())
	claims := gatewayOIDCTestClaims(cfg.IssuerURL, time.Now())

	_, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, key1, "key-1", claims))
	require.NoError(t, err)
	require.EqualValues(t, 1, jwks.hits.Load())

	_, err = server.Verify(context.Background(), gatewayOIDCTestToken(t, key1, "key-1", claims))
	require.NoError(t, err)
	require.EqualValues(t, 1, jwks.hits.Load(), "a cached kid must not refetch JWKS")

	jwks.replace(jwk1, jwk2)
	_, err = server.Verify(context.Background(), gatewayOIDCTestToken(t, key2, "key-2", claims))
	require.NoError(t, err)
	require.EqualValues(t, 2, jwks.hits.Load(), "a new kid must trigger one JWKS refresh")

	principal, err := server.Verify(context.Background(), gatewayOIDCTestToken(t, unknownKey, "unknown-key", claims))
	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	require.EqualValues(t, 3, jwks.hits.Load(), "an unknown kid must be checked against a fresh JWKS")

	secondUnknownKey := gatewayOIDCTestRSAKey(t)
	principal, err = server.Verify(context.Background(), gatewayOIDCTestToken(t, secondUnknownKey, "another-unknown-key", claims))
	require.Nil(t, principal)
	require.ErrorIs(t, err, ErrGatewayOIDCTokenInvalid)
	require.EqualValues(t, 3, jwks.hits.Load(), "random unknown kids must not force repeated JWKS refreshes")
}
