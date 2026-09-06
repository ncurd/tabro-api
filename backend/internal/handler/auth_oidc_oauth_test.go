package handler

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type oidcLoginUserRepoStub struct {
	service.UserRepository
	usersByEmail map[string]*service.User
	createCalls  int
}

func (r *oidcLoginUserRepoStub) GetByEmail(_ context.Context, email string) (*service.User, error) {
	user, ok := r.usersByEmail[email]
	if !ok {
		return nil, service.ErrUserNotFound
	}
	return user, nil
}

func (r *oidcLoginUserRepoStub) GetByID(_ context.Context, id int64) (*service.User, error) {
	for _, user := range r.usersByEmail {
		if user.ID == id {
			return user, nil
		}
	}
	return nil, service.ErrUserNotFound
}

func (r *oidcLoginUserRepoStub) Create(_ context.Context, _ *service.User) error {
	r.createCalls++
	return nil
}

type oidcLoginAPIKeyRepoStub struct {
	service.APIKeyRepository
	keys      map[string]*service.APIKey
	created   []*service.APIKey
	nextKeyID int64
}

func (r *oidcLoginAPIKeyRepoStub) BindOIDCIdentity(_ context.Context, id int64, issuer, subject string) error {
	for _, apiKey := range r.keys {
		if apiKey.ID != id {
			continue
		}
		if (apiKey.OIDCIssuer != "" || apiKey.OIDCSubject != "") && (apiKey.OIDCIssuer != issuer || apiKey.OIDCSubject != subject) {
			return service.ErrOIDCGatewayIdentityConflict
		}
		apiKey.OIDCIssuer = issuer
		apiKey.OIDCSubject = subject
		return nil
	}
	return service.ErrAPIKeyNotFound
}

func (r *oidcLoginAPIKeyRepoStub) GetByOIDCIdentity(_ context.Context, issuer, subject string) (*service.APIKey, error) {
	for _, apiKey := range r.keys {
		if apiKey.OIDCIssuer == issuer && apiKey.OIDCSubject == subject {
			clone := *apiKey
			return &clone, nil
		}
	}
	return nil, service.ErrAPIKeyNotFound
}

func (r *oidcLoginAPIKeyRepoStub) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	apiKey, ok := r.keys[key]
	if !ok {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *apiKey
	return &clone, nil
}

func (r *oidcLoginAPIKeyRepoStub) ListByUserID(_ context.Context, _ int64, _ pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{}, nil
}

func (r *oidcLoginAPIKeyRepoStub) ListKeysByUserID(_ context.Context, userID int64) ([]string, error) {
	keys := make([]string, 0, len(r.keys))
	for key, apiKey := range r.keys {
		if apiKey.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (r *oidcLoginAPIKeyRepoStub) Create(_ context.Context, apiKey *service.APIKey) error {
	if r.nextKeyID == 0 {
		r.nextKeyID = 7001
	}
	apiKey.ID = r.nextKeyID
	r.nextKeyID++
	clone := *apiKey
	clone.User = &service.User{ID: apiKey.UserID, Status: service.StatusActive}
	r.created = append(r.created, &clone)
	if r.keys == nil {
		r.keys = make(map[string]*service.APIKey)
	}
	r.keys[apiKey.Key] = &clone
	return nil
}

type oidcLoginRefreshCacheStub struct {
	service.RefreshTokenCache
	stored []*service.RefreshTokenData
}

func (r *oidcLoginRefreshCacheStub) StoreRefreshToken(_ context.Context, _ string, data *service.RefreshTokenData, _ time.Duration) error {
	clone := *data
	r.stored = append(r.stored, &clone)
	return nil
}

func (r *oidcLoginRefreshCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (r *oidcLoginRefreshCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

type oidcLoginSettingRepoStub struct {
	service.SettingRepository
	values map[string]string
}

func (r *oidcLoginSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *oidcLoginSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	if r.values == nil {
		r.values = make(map[string]string, len(values))
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func newOIDCLoginIntegrationServices(
	user *service.User,
	settingService *service.SettingService,
) (*AuthHandler, *service.AuthService, *oidcLoginUserRepoStub, *oidcLoginAPIKeyRepoStub, *oidcLoginRefreshCacheStub) {
	cfg := &config.Config{}
	cfg.JWT.Secret = "oidc-handler-integration-test-secret"
	cfg.JWT.AccessTokenExpireMinutes = 60
	cfg.JWT.RefreshTokenExpireDays = 30

	userRepo := &oidcLoginUserRepoStub{
		usersByEmail: map[string]*service.User{user.Email: user},
	}
	refreshCache := &oidcLoginRefreshCacheStub{}
	authService := service.NewAuthService(
		nil,
		userRepo,
		nil,
		refreshCache,
		cfg,
		settingService,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	apiKeyRepo := &oidcLoginAPIKeyRepoStub{keys: make(map[string]*service.APIKey)}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	handler := NewAuthHandler(cfg, authService, apiKeyService, nil, settingService, nil, nil, nil)
	return handler, authService, userRepo, apiKeyRepo, refreshCache
}

func TestLoginOIDCWithTokenPairCreatesPersistentBillingKey(t *testing.T) {
	user := &service.User{
		ID:           91,
		Email:        "oidc-user@example.com",
		Username:     "oidc-user",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		TokenVersion: 4,
	}
	handler, authService, userRepo, apiKeyRepo, refreshCache := newOIDCLoginIntegrationServices(user, nil)

	pair, resolvedUser, err := handler.loginOIDCWithTokenPair(
		context.Background(),
		user.Email,
		user.Username,
		"",
		false,
		"https://issuer.example.com",
		"subject-91",
	)
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.Same(t, user, resolvedUser)
	require.Zero(t, userRepo.createCalls)

	require.Len(t, apiKeyRepo.created, 1)
	createdKey := apiKeyRepo.created[0]
	require.Equal(t, int64(7001), createdKey.ID)
	require.Equal(t, user.ID, createdKey.UserID)
	require.Equal(t, "OIDC Access Token", createdKey.Name)
	require.True(t, createdKey.OIDCManaged)
	require.Equal(t, "https://issuer.example.com", apiKeyRepo.keys[createdKey.Key].OIDCIssuer)
	require.Equal(t, "subject-91", apiKeyRepo.keys[createdKey.Key].OIDCSubject)
	persistedKey, ok := apiKeyRepo.keys[createdKey.Key]
	require.True(t, ok)
	require.Equal(t, createdKey.ID, persistedKey.ID)

	claims, err := authService.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, service.AuthMethodOIDC, claims.AuthMethod)
	require.Equal(t, createdKey.ID, claims.BillingAPIKeyID)
	require.Equal(t, user.ID, claims.UserID)

	require.Len(t, refreshCache.stored, 1)
	require.Equal(t, service.AuthMethodOIDC, refreshCache.stored[0].AuthMethod)
	require.Equal(t, createdKey.ID, refreshCache.stored[0].BillingAPIKeyID)
}

func TestLoginOIDCWithTokenPairPrefersImmutableIdentityBindingOverEmail(t *testing.T) {
	user := &service.User{
		ID:           93,
		Email:        "bound-user@example.com",
		Username:     "bound-user",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		TokenVersion: 2,
	}
	handler, authService, userRepo, apiKeyRepo, _ := newOIDCLoginIntegrationServices(user, nil)
	apiKeyRepo.keys["oidc-internal:existing"] = &service.APIKey{
		ID:          8101,
		UserID:      user.ID,
		Key:         "oidc-internal:existing",
		Name:        "OIDC Access Token",
		Status:      service.StatusActive,
		OIDCManaged: true,
		OIDCIssuer:  "https://issuer.example.com",
		OIDCSubject: "stable-subject-93",
	}

	pair, resolvedUser, err := handler.loginOIDCWithTokenPair(
		context.Background(),
		"changed-or-untrusted@example.net",
		"ignored-name",
		"",
		false,
		"https://issuer.example.com",
		"stable-subject-93",
	)
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.Same(t, user, resolvedUser)
	require.Zero(t, userRepo.createCalls)
	require.Empty(t, apiKeyRepo.created)

	claims, err := authService.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, int64(8101), claims.BillingAPIKeyID)
	require.Equal(t, user.ID, claims.UserID)
}

func TestLoginOIDCWithTokenPairBackendModeRejectsNonAdminWithoutSideEffects(t *testing.T) {
	settingRepo := &oidcLoginSettingRepoStub{values: make(map[string]string)}
	settingService := service.NewSettingService(settingRepo, &config.Config{})
	require.NoError(t, settingService.UpdateSettings(context.Background(), &service.SystemSettings{
		BackendModeEnabled: true,
	}))
	t.Cleanup(func() {
		require.NoError(t, settingService.UpdateSettings(context.Background(), &service.SystemSettings{
			BackendModeEnabled: false,
		}))
	})

	user := &service.User{
		ID:       92,
		Email:    "ordinary-user@example.com",
		Username: "ordinary-user",
		Role:     service.RoleUser,
		Status:   service.StatusActive,
	}
	handler, _, userRepo, apiKeyRepo, refreshCache := newOIDCLoginIntegrationServices(user, settingService)

	pair, resolvedUser, err := handler.loginOIDCWithTokenPair(
		context.Background(),
		user.Email,
		user.Username,
		"",
		true,
		"https://issuer.example.com",
		"subject-92",
	)
	require.ErrorIs(t, err, service.ErrInvalidCredentials)
	require.Nil(t, pair)
	require.Nil(t, resolvedUser)
	require.Zero(t, userRepo.createCalls)
	require.Empty(t, apiKeyRepo.created)
	require.Empty(t, refreshCache.stored)
}

func TestCompleteOIDCOAuthRegistrationRejectsBackendModeInHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingRepo := &oidcLoginSettingRepoStub{values: make(map[string]string)}
	settingService := service.NewSettingService(settingRepo, &config.Config{})
	require.NoError(t, settingService.UpdateSettings(context.Background(), &service.SystemSettings{
		BackendModeEnabled: true,
	}))
	handler := &AuthHandler{settingSvc: settingService}

	router := gin.New()
	router.POST("/complete", handler.CompleteOIDCOAuthRegistration)
	req := httptest.NewRequest(http.MethodPost, "/complete", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "OAuth registration is disabled")
}

func TestOIDCSyntheticEmailStableAndDistinct(t *testing.T) {
	k1 := oidcIdentityKey("https://issuer.example.com", "subject-a")
	k2 := oidcIdentityKey("https://issuer.example.com", "subject-b")

	e1 := oidcSyntheticEmailFromIdentityKey(k1)
	e1Again := oidcSyntheticEmailFromIdentityKey(k1)
	e2 := oidcSyntheticEmailFromIdentityKey(k2)

	require.Equal(t, e1, e1Again)
	require.NotEqual(t, e1, e2)
	require.Contains(t, e1, "@oidc-connect.invalid")
}

func TestOIDCSelectLoginEmailPrefersRealEmail(t *testing.T) {
	identityKey := oidcIdentityKey("https://issuer.example.com", "subject-a")

	email := oidcSelectLoginEmail("user@example.com", "idtoken@example.com", identityKey)
	require.Equal(t, "user@example.com", email)

	email = oidcSelectLoginEmail("", "idtoken@example.com", identityKey)
	require.Equal(t, "idtoken@example.com", email)

	email = oidcSelectLoginEmail("", "", identityKey)
	require.Contains(t, email, "@oidc-connect.invalid")
	require.Equal(t, oidcSyntheticEmailFromIdentityKey(identityKey), email)
}

func TestOIDCSelectSubjectRejectsUserInfoMismatch(t *testing.T) {
	subject, err := oidcSelectSubject("subject-from-id-token", "subject-from-userinfo")
	require.Error(t, err)
	require.Empty(t, subject)

	subject, err = oidcSelectSubject("subject-ok", "subject-ok")
	require.NoError(t, err)
	require.Equal(t, "subject-ok", subject)
}

func TestOIDCSelectVerifiedLoginEmailKeepsVerificationBoundToSource(t *testing.T) {
	verified := true
	unverified := false

	tests := []struct {
		name     string
		userInfo *oidcUserInfoClaims
		idToken  *oidcIDTokenClaims
		want     string
		wantOK   bool
	}{
		{
			name:     "verified userinfo email wins",
			userInfo: &oidcUserInfoClaims{Email: "userinfo@example.com", EmailVerified: &verified},
			idToken:  &oidcIDTokenClaims{Email: "id@example.com", EmailVerified: &verified},
			want:     "userinfo@example.com",
			wantOK:   true,
		},
		{
			name:     "unverified userinfo cannot borrow id token verification",
			userInfo: &oidcUserInfoClaims{Email: "other-admin@example.com", EmailVerified: &unverified},
			idToken:  &oidcIDTokenClaims{Email: "verified@example.com", EmailVerified: &verified},
			want:     "verified@example.com",
			wantOK:   true,
		},
		{
			name:     "userinfo without verification cannot borrow id token verification",
			userInfo: &oidcUserInfoClaims{Email: "other-admin@example.com"},
			idToken:  &oidcIDTokenClaims{Email: "verified@example.com", EmailVerified: &verified},
			want:     "verified@example.com",
			wantOK:   true,
		},
		{
			name:     "no verified source is rejected",
			userInfo: &oidcUserInfoClaims{Email: "userinfo@example.com", EmailVerified: &unverified},
			idToken:  &oidcIDTokenClaims{Email: "id@example.com"},
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := oidcSelectVerifiedLoginEmail(tc.userInfo, tc.idToken)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuildOIDCAuthorizeURLIncludesNonceAndPKCE(t *testing.T) {
	cfg := config.OIDCConnectConfig{
		AuthorizeURL: "https://issuer.example.com/auth",
		ClientID:     "cid",
		Scopes:       "openid email profile",
		UsePKCE:      true,
	}

	u, err := buildOIDCAuthorizeURL(cfg, "state123", "nonce123", "challenge123", "https://app.example.com/callback")
	require.NoError(t, err)
	require.Contains(t, u, "nonce=nonce123")
	require.Contains(t, u, "code_challenge=challenge123")
	require.Contains(t, u, "code_challenge_method=S256")
	require.Contains(t, u, "scope=openid+email+profile")
}

func TestOIDCParseAndValidateIDToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	kid := "kid-1"
	jwks := oidcJWKSet{Keys: []oidcJWK{buildRSAJWK(kid, &priv.PublicKey)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(jwks))
	}))
	defer srv.Close()

	now := time.Now()
	claims := oidcIDTokenClaims{
		Nonce: "nonce-ok",
		Azp:   "client-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.example.com",
			Subject:   "subject-1",
			Audience:  jwt.ClaimStrings{"client-1", "another-aud"},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)

	cfg := config.OIDCConnectConfig{
		ClientID:           "client-1",
		IssuerURL:          "https://issuer.example.com",
		JWKSURL:            srv.URL,
		AllowedSigningAlgs: "RS256",
		ClockSkewSeconds:   120,
	}

	parsed, err := oidcParseAndValidateIDToken(context.Background(), cfg, signed, "nonce-ok")
	require.NoError(t, err)
	require.Equal(t, "subject-1", parsed.Subject)
	require.Equal(t, "https://issuer.example.com", parsed.Issuer)

	_, err = oidcParseAndValidateIDToken(context.Background(), cfg, signed, "bad-nonce")
	require.Error(t, err)
}

func buildRSAJWK(kid string, pub *rsa.PublicKey) oidcJWK {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	return oidcJWK{
		Kty: "RSA",
		Kid: kid,
		Use: "sig",
		Alg: "RS256",
		N:   n,
		E:   e,
	}
}
