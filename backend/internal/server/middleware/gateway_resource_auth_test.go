package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	gatewayMiddlewareAudience = "llm-gateway-api"
	gatewayMiddlewareClientID = "tabro-client"
	gatewayMiddlewareSubject  = "tabro-user-42"
	gatewayMiddlewareTenant   = "tenant-acme"
)

type gatewayMiddlewareIdentityRepo struct {
	service.APIKeyRepository

	boundKey          *service.APIKey
	identityLookupErr error
	getByKeyCalls     int
	identityLookups   int
	lastLookupIssuer  string
	lastLookupSubject string
}

func (r *gatewayMiddlewareIdentityRepo) GetByKey(_ context.Context, _ string) (*service.APIKey, error) {
	r.getByKeyCalls++
	return nil, service.ErrAPIKeyNotFound
}

func (r *gatewayMiddlewareIdentityRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKey(ctx, key)
}

func (r *gatewayMiddlewareIdentityRepo) GetByOIDCIdentity(_ context.Context, issuer, subject string) (*service.APIKey, error) {
	r.identityLookups++
	r.lastLookupIssuer = issuer
	r.lastLookupSubject = subject
	if r.identityLookupErr != nil {
		return nil, r.identityLookupErr
	}
	if r.boundKey == nil || r.boundKey.OIDCIssuer != issuer || r.boundKey.OIDCSubject != subject {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.boundKey
	return &clone, nil
}

func (r *gatewayMiddlewareIdentityRepo) BindOIDCIdentity(_ context.Context, _ int64, _, _ string) error {
	return errors.New("not implemented")
}

func (r *gatewayMiddlewareIdentityRepo) UpdateLastUsed(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

type gatewayMiddlewareOAuthFixture struct {
	issuer     string
	privateKey *rsa.PrivateKey
	kid        string
	cfg        *config.Config
	repo       *gatewayMiddlewareIdentityRepo
	apiKeys    *service.APIKeyService
}

func newGatewayMiddlewareOAuthFixture(t *testing.T) *gatewayMiddlewareOAuthFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	kid := "gateway-test-key"

	var issuer string
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}
		exponent := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty":     "RSA",
				"kid":     kid,
				"use":     "sig",
				"alg":     jwt.SigningMethodRS256.Alg(),
				"key_ops": []string{"verify"},
				"n":       base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
				"e":       base64.RawURLEncoding.EncodeToString(exponent),
			}},
		}); err != nil {
			http.Error(w, "failed to encode jwks", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(jwksServer.Close)
	issuer = jwksServer.URL

	boundKey := &service.APIKey{
		ID:          7331,
		UserID:      42,
		Key:         "internal-oidc-billing-key",
		OIDCManaged: true,
		OIDCIssuer:  issuer,
		OIDCSubject: gatewayMiddlewareSubject,
		Status:      service.StatusAPIKeyActive,
		User: &service.User{
			ID:          42,
			Role:        service.RoleUser,
			Status:      service.StatusActive,
			Concurrency: 3,
		},
	}
	repo := &gatewayMiddlewareIdentityRepo{boundKey: boundKey}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "local-tabro-jwt-secret"
	cfg.Gateway.ResourceServer = config.GatewayResourceServerConfig{
		Enabled:             true,
		IssuerURL:           issuer,
		JWKSURL:             jwksServer.URL + "/jwks",
		Audience:            gatewayMiddlewareAudience,
		RequiredScopes:      "llm.invoke",
		AllowedClientIDs:    gatewayMiddlewareClientID,
		AllowedSigningAlgs:  jwt.SigningMethodRS256.Alg(),
		ClockSkewSeconds:    0,
		JWKSCacheTTLSeconds: 300,
		TenantClaim:         "tenant_id",
		TokenExchange: config.GatewayTokenExchangeConfig{
			ActorClaim:         "act",
			MaxDelegationDepth: 4,
		},
	}
	apiKeys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)

	return &gatewayMiddlewareOAuthFixture{
		issuer:     issuer,
		privateKey: privateKey,
		kid:        kid,
		cfg:        cfg,
		repo:       repo,
		apiKeys:    apiKeys,
	}
}

func (f *gatewayMiddlewareOAuthFixture) claims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":       f.issuer,
		"sub":       gatewayMiddlewareSubject,
		"aud":       gatewayMiddlewareAudience,
		"exp":       now.Add(5 * time.Minute).Unix(),
		"nbf":       now.Add(-time.Minute).Unix(),
		"iat":       now.Add(-time.Minute).Unix(),
		"scope":     "openid llm.invoke",
		"azp":       gatewayMiddlewareClientID,
		"tenant_id": gatewayMiddlewareTenant,
	}
}

func (f *gatewayMiddlewareOAuthFixture) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.privateKey)
	require.NoError(t, err)
	return signed
}

func cloneGatewayMiddlewareClaims(source jwt.MapClaims) jwt.MapClaims {
	clone := make(jwt.MapClaims, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func TestGatewayOAuthResourceServerAcceptsDedicatedAudienceAndStoresSafeRequestIdentity(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	token := fixture.sign(t, fixture.claims())
	const (
		idempotencyKey = "run-17:node-3:logical-call-9"
		runID          = "run-17"
		projectID      = "project-29"
	)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
	router.GET("/v1/test", func(c *gin.Context) {
		resolved, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, fixture.repo.boundKey.ID, resolved.ID)

		ctx := c.Request.Context()
		require.Equal(t, fixture.issuer, ctx.Value(ctxkey.OIDCIssuer))
		require.Equal(t, gatewayMiddlewareSubject, ctx.Value(ctxkey.OIDCSubject))
		require.Equal(t, gatewayMiddlewareTenant, ctx.Value(ctxkey.OIDCTenant))
		expiresAt, ok := ctx.Value(ctxkey.OIDCExpiresAt).(time.Time)
		require.True(t, ok)
		require.True(t, expiresAt.After(time.Now()))
		require.Equal(t, runID, ctx.Value(ctxkey.TabroRunID))
		require.Equal(t, projectID, ctx.Value(ctxkey.TabroProjectID))

		sum := sha256.Sum256([]byte("gateway\x00" + strconv.FormatInt(resolved.ID, 10) + "\x00" + idempotencyKey))
		expectedHash := hex.EncodeToString(sum[:])
		storedID, _ := ctx.Value(ctxkey.GatewayBillingRequestID).(string)
		require.Equal(t, expectedHash, storedID)
		require.Len(t, storedID, sha256.Size*2)
		require.NotEqual(t, idempotencyKey, storedID)

		// The async usage snapshot must retain only verified identity,
		// correlation metadata and the hashed idempotency identifier.
		workerCtx := ctxkey.CaptureGatewayUsageContext(ctx).Apply(context.Background())
		require.Equal(t, expectedHash, workerCtx.Value(ctxkey.GatewayBillingRequestID))
		require.Equal(t, gatewayMiddlewareSubject, workerCtx.Value(ctxkey.OIDCSubject))
		require.Equal(t, runID, workerCtx.Value(ctxkey.TabroRunID))
		require.NotEqual(t, token, workerCtx.Value(ctxkey.GatewayBillingRequestID))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.Header.Set("X-Tabro-Run-Id", runID)
	req.Header.Set("X-Tabro-Project-Id", projectID)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, fixture.repo.identityLookups)
	require.Equal(t, fixture.issuer, fixture.repo.lastLookupIssuer)
	require.Equal(t, gatewayMiddlewareSubject, fixture.repo.lastLookupSubject)
	require.Zero(t, fixture.repo.getByKeyCalls, "a verified bearer JWT must not be looked up as an API key")
}

func TestGatewayOAuthResourceServerMapsAudienceAndScopeFailures(t *testing.T) {
	tests := []struct {
		name                string
		mutate              func(jwt.MapClaims)
		wantStatus          int
		wantCode            string
		wantAuthenticate    string
		wantIdentityLookups int
	}{
		{
			name: "wrong audience is unauthorized",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = "tabro-api"
			},
			wantStatus:       http.StatusUnauthorized,
			wantCode:         "INVALID_TOKEN",
			wantAuthenticate: `Bearer error="invalid_token"`,
		},
		{
			name: "gateway plus tabro audience is unauthorized",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = []string{gatewayMiddlewareAudience, "tabro-api"}
			},
			wantStatus:       http.StatusUnauthorized,
			wantCode:         "INVALID_TOKEN",
			wantAuthenticate: `Bearer error="invalid_token"`,
		},
		{
			name: "missing invoke scope is forbidden",
			mutate: func(claims jwt.MapClaims) {
				claims["scope"] = "openid profile"
			},
			wantStatus:       http.StatusForbidden,
			wantCode:         "INSUFFICIENT_SCOPE",
			wantAuthenticate: `Bearer error="insufficient_scope", scope="llm.invoke"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newGatewayMiddlewareOAuthFixture(t)
			claims := cloneGatewayMiddlewareClaims(fixture.claims())
			tt.mutate(claims)
			token := fixture.sign(t, claims)
			handlerCalled := false

			router := gin.New()
			router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
			router.GET("/v1/test", func(c *gin.Context) {
				handlerCalled = true
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
			var response ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tt.wantCode, response.Code)
			require.Equal(t, tt.wantAuthenticate, rec.Header().Get("WWW-Authenticate"))
			require.False(t, handlerCalled)
			require.Equal(t, tt.wantIdentityLookups, fixture.repo.identityLookups,
				"claims must be fully validated before billing identity lookup")
		})
	}
}

func TestGatewayOAuthResourceServerGoogleMapsAudienceAndScopeFailures(t *testing.T) {
	tests := []struct {
		name             string
		mutate           func(jwt.MapClaims)
		wantStatus       int
		wantGoogleStatus string
		wantAuthenticate string
	}{
		{
			name: "wrong audience is unauthenticated",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = "tabro-api"
			},
			wantStatus:       http.StatusUnauthorized,
			wantGoogleStatus: "UNAUTHENTICATED",
			wantAuthenticate: `Bearer error="invalid_token"`,
		},
		{
			name: "missing invoke scope is permission denied",
			mutate: func(claims jwt.MapClaims) {
				claims["scope"] = "openid profile"
			},
			wantStatus:       http.StatusForbidden,
			wantGoogleStatus: "PERMISSION_DENIED",
			wantAuthenticate: `Bearer error="insufficient_scope", scope="llm.invoke"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newGatewayMiddlewareOAuthFixture(t)
			claims := cloneGatewayMiddlewareClaims(fixture.claims())
			tt.mutate(claims)
			token := fixture.sign(t, claims)

			router := gin.New()
			router.Use(GatewayAuthWithSubscriptionGoogle(fixture.apiKeys, nil, nil, fixture.cfg))
			router.GET("/v1beta/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			req := httptest.NewRequest(http.MethodGet, "/v1beta/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
			var response googleErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tt.wantStatus, response.Error.Code)
			require.Equal(t, tt.wantGoogleStatus, response.Error.Status)
			require.Equal(t, tt.wantAuthenticate, rec.Header().Get("WWW-Authenticate"))
			require.Zero(t, fixture.repo.identityLookups)
		})
	}
}

func TestGatewayOAuthResourceServerRejectsLocalTabroJWT(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	now := time.Now()
	localToken := jwt.NewWithClaims(jwt.SigningMethodHS256, service.JWTClaims{
		UserID:          fixture.repo.boundKey.UserID,
		AuthMethod:      service.AuthMethodOIDC,
		BillingAPIKeyID: fixture.repo.boundKey.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		},
	})
	signed, err := localToken.SignedString([]byte(fixture.cfg.JWT.Secret))
	require.NoError(t, err)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
	router.GET("/v1/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var response ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, "INVALID_TOKEN", response.Code)
	require.Equal(t, `Bearer error="invalid_token"`, rec.Header().Get("WWW-Authenticate"))
	require.Zero(t, fixture.repo.identityLookups)
	require.Zero(t, fixture.repo.getByKeyCalls, "a JWT-shaped bearer value must never downgrade to API-key lookup")
}

func TestGatewayOAuthJWTIsAcceptedOnlyFromAuthorizationBearer(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		setToken    func(*http.Request, string)
		googleStyle bool
	}{
		{
			name: "x-api-key",
			path: "/v1/test",
			setToken: func(req *http.Request, token string) {
				req.Header.Set("x-api-key", token)
			},
		},
		{
			name: "x-goog-api-key",
			path: "/v1/test",
			setToken: func(req *http.Request, token string) {
				req.Header.Set("x-goog-api-key", token)
			},
		},
		{
			name:        "google query key",
			path:        "/v1beta/test",
			googleStyle: true,
			setToken: func(req *http.Request, token string) {
				query := req.URL.Query()
				query.Set("key", token)
				req.URL.RawQuery = query.Encode()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newGatewayMiddlewareOAuthFixture(t)
			token := fixture.sign(t, fixture.claims())
			handlerCalled := false
			router := gin.New()
			if tt.googleStyle {
				router.Use(GatewayAuthWithSubscriptionGoogle(fixture.apiKeys, nil, nil, fixture.cfg))
			} else {
				router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
			}
			router.GET(tt.path, func(c *gin.Context) {
				handlerCalled = true
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			tt.setToken(req, token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.False(t, handlerCalled)
			require.Zero(t, fixture.repo.identityLookups,
				"non-bearer JWTs must never enter OAuth identity resolution")
			require.Zero(t, fixture.repo.getByKeyCalls,
				"JWT-shaped credentials outside Authorization must be rejected before API-key lookup")
		})
	}
}

func TestGatewayOAuthTabroHeadersCannotAuthenticateOrSelectBillingIdentity(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
	router.GET("/v1/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Idempotency-Key", "run:node:call")
	req.Header.Set("X-Tabro-Run-Id", "pretend-user-subject")
	req.Header.Set("X-Tabro-Project-Id", "pretend-billing-key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, fixture.repo.identityLookups)
	require.Zero(t, fixture.repo.getByKeyCalls)
}

func TestGatewayMalformedCompactJWTNeverDowngradesToAPIKeyLookup(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	resourceServer := service.NewGatewayResourceServer(fixture.cfg, fixture.apiKeys)

	for _, credential := range []string{
		"not-json.not-base64.signature=",
		"header..signature",
		"header.payload.",
	} {
		before := fixture.repo.getByKeyCalls
		_, _, err := resolveGatewayCredential(context.Background(), credential, true, fixture.apiKeys, resourceServer)
		require.Error(t, err)
		require.Equal(t, before, fixture.repo.getByKeyCalls, "compact JWT-shaped input must not reach API-key lookup")
	}
}
