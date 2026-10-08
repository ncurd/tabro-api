//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type oidcAccountSettingRepo struct {
	service.SettingRepository
	values map[string]string
	err    error
}

func (r *oidcAccountSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	value, exists := r.values[key]
	if !exists {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *oidcAccountSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	values := make(map[string]string)
	for _, key := range keys {
		if value, exists := r.values[key]; exists {
			values[key] = value
		}
	}
	return values, nil
}

type oidcAccountKeyRepo struct {
	service.APIKeyRepository
	key     *service.APIKey
	err     error
	lookups []int64
}

func (r *oidcAccountKeyRepo) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	r.lookups = append(r.lookups, id)
	if r.err != nil {
		return nil, r.err
	}
	if r.key == nil || r.key.ID != id {
		return nil, service.ErrAPIKeyNotFound
	}
	key := *r.key
	return &key, nil
}

type oidcAccountFixture struct {
	router   *gin.Engine
	settings *oidcAccountSettingRepo
	keys     *oidcAccountKeyRepo
	user     *service.User
	auth     *service.AuthService
	cfg      *config.Config
}

func newOIDCAccountFixture(t *testing.T) *oidcAccountFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &oidcAccountFixture{
		settings: &oidcAccountSettingRepo{values: map[string]string{
			service.SettingKeyOIDCBillingEnabled: "true",
			service.SettingKeyOIDCOnlyEnabled:    "true", service.SettingKeyOIDCConnectEnabled: "true",
			service.SettingKeyOIDCConnectIssuerURL: "https://auth.tabro.cn",
			service.SettingKeyOIDCConnectClientID:  "tabro-llm",
		}},
		user: &service.User{ID: 41, Email: "local@example.com", Role: service.RoleUser, Status: service.StatusActive, TokenVersion: 1},
		keys: &oidcAccountKeyRepo{key: &service.APIKey{
			ID: 501, UserID: 41, Key: "not-a-public-secret", Status: service.StatusActive,
			OIDCManaged: true, OIDCIssuer: "https://auth.tabro.cn", OIDCSubject: "auth-user-41",
		}},
		cfg: &config.Config{
			OIDC: config.OIDCConnectConfig{Enabled: true, IssuerURL: "https://auth.tabro.cn", ClientID: "obsolete-login-client"},
			BillingCenter: config.BillingCenterConfig{
				Enabled: true, BaseURL: "https://billing-separate.example", TokenURL: "https://auth.tabro.cn/connect/token",
				ProducerClientID: "tabro-api-gateway", ClientSecret: "private-producer-secret", TimeoutSeconds: 15,
			},
		},
	}
	f.cfg.JWT.Secret = "oidc-account-test-signing-secret-32bytes"
	f.cfg.JWT.AccessTokenExpireMinutes = 60
	f.cfg.Gateway.ResourceServer = config.GatewayResourceServerConfig{
		Enabled: true, IssuerURL: "https://auth.tabro.cn", Audience: "tabro-llm", RequiredScopes: "llm.invoke",
		AllowedClientIDs: "tabro-agent", AllowedSigningAlgs: "RS256", ClockSkewSeconds: 120, JWKSCacheTTLSeconds: 300,
		TenantClaim: "tenant_id", RequireTenant: true,
		TokenExchange: config.GatewayTokenExchangeConfig{RequireActor: true, ActorClaim: "act", AllowedActorClientIDs: "tabro-agent", MaxDelegationDepth: 1},
	}
	users := &oidcLoginUserRepoStub{usersByEmail: map[string]*service.User{f.user.Email: f.user}}
	settings := service.NewSettingService(f.settings, f.cfg)
	f.auth = service.NewAuthService(nil, users, nil, nil, f.cfg, settings, nil, nil, nil, nil, nil)
	keys := service.NewAPIKeyService(f.keys, nil, nil, nil, nil, nil, f.cfg)
	usersService := service.NewUserService(users, nil, nil, nil)
	handler := NewAuthHandler(f.cfg, f.auth, keys, usersService, settings, nil, nil, nil)
	f.router = gin.New()
	f.router.Use(gin.HandlerFunc(middleware.NewJWTAuthMiddleware(f.auth, usersService, settings)))
	f.router.Use(middleware.BackendModeUserGuard(settings))
	f.router.GET("/api/v1/auth/oidc-account", handler.GetOIDCAccount)
	return f
}

func (f *oidcAccountFixture) token(t *testing.T, method string, keyID int64) string {
	t.Helper()
	plain, err := f.auth.GenerateToken(f.user)
	require.NoError(t, err)
	claims, err := f.auth.ValidateToken(plain)
	require.NoError(t, err)
	claims.AuthMethod, claims.BillingAPIKeyID = method, keyID
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(f.cfg.JWT.Secret))
	require.NoError(t, err)
	return token
}

func (f *oidcAccountFixture) request(t *testing.T, method string, keyID int64, query string) (*httptest.ResponseRecorder, oidcAccountResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc-account"+query, nil)
	request.Header.Set("Authorization", "Bearer "+f.token(t, method, keyID))
	result := httptest.NewRecorder()
	f.router.ServeHTTP(result, request)
	var envelope struct {
		Data oidcAccountResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &envelope))
	return result, envelope.Data
}

func requireNoOIDCAccountIdentity(t *testing.T, result oidcAccountResponse) {
	t.Helper()
	require.Nil(t, result.Issuer)
	require.Nil(t, result.Subject)
	require.Nil(t, result.ClientID)
	require.Nil(t, result.SummaryURL)
	require.Empty(t, result.Links)
}

func TestOIDCAccountReturnsOnlyVerifiedSessionIdentityAndTrustedLinks(t *testing.T) {
	f := newOIDCAccountFixture(t)
	response, account := f.request(t, service.AuthMethodOIDC, 501,
		"?user_id=999&billing_api_key_id=999&subject=another-user&issuer=https://attacker.example&client_id=another-client")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, "linked", account.Status)
	require.Equal(t, "https://auth.tabro.cn", *account.Issuer)
	require.Equal(t, "auth-user-41", *account.Subject)
	require.Equal(t, "tabro-llm", *account.ClientID)
	summary, err := url.Parse(*account.SummaryURL)
	require.NoError(t, err)
	require.Equal(t, "https", summary.Scheme)
	require.Equal(t, "auth.tabro.cn", summary.Host)
	require.Equal(t, "/api/account/header-summary", summary.Path)
	require.Equal(t, url.Values{"client_id": {"tabro-llm"}}, summary.Query())
	require.Equal(t, "https://auth.tabro.cn/Identity/Account/Manage/CreditDetails", account.Links.CreditDetails)
	require.Equal(t, "https://auth.tabro.cn/Identity/Account/Manage/Index", account.Links.Profile)
	require.Equal(t, "https://auth.tabro.cn/Identity/Account/Manage/TwoFactorAuthentication", account.Links.SecuritySettings)
	require.Equal(t, []int64{501}, f.keys.lookups)
	for _, private := range []string{"private-producer-secret", "not-a-public-secret", f.user.Email, "billing-separate.example", "tabro-api-gateway"} {
		require.NotContains(t, response.Body.String(), private)
	}
}

func TestOIDCAccountFailsClosedForUnlinkedAndChangedIdentities(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*oidcAccountFixture)
	}{
		{"missing key", func(f *oidcAccountFixture) { f.keys.key = nil }},
		{"different owner", func(f *oidcAccountFixture) { f.keys.key.UserID = 999 }},
		{"ordinary key", func(f *oidcAccountFixture) { f.keys.key.OIDCManaged = false }},
		{"revoked key", func(f *oidcAccountFixture) { f.keys.key.Status = service.StatusAPIKeyDisabled }},
		{"expired key", func(f *oidcAccountFixture) { at := time.Now().Add(-time.Minute); f.keys.key.ExpiresAt = &at }},
		{"issuer changed", func(f *oidcAccountFixture) { f.keys.key.OIDCIssuer = "https://previous-auth.example" }},
		{"blank subject", func(f *oidcAccountFixture) { f.keys.key.OIDCSubject = " " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOIDCAccountFixture(t)
			test.change(f)
			response, account := f.request(t, service.AuthMethodOIDC, 501, "")
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "unlinked", account.Status)
			requireNoOIDCAccountIdentity(t, account)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		})
	}
}

func TestOIDCAccountDisablingBillingDoesNotReadOrExposeIdentity(t *testing.T) {
	f := newOIDCAccountFixture(t)
	f.settings.values[service.SettingKeyOIDCBillingEnabled] = "false"
	response, account := f.request(t, service.AuthMethodOIDC, 501, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "disabled", account.Status)
	requireNoOIDCAccountIdentity(t, account)
	require.Empty(t, f.keys.lookups)
}

func TestOIDCAccountLegacyOIDCSessionCannotSupplyItsMissingKeyInQuery(t *testing.T) {
	f := newOIDCAccountFixture(t)
	response, account := f.request(t, service.AuthMethodOIDC, 0, "?billing_api_key_id=501&subject=auth-user-41")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "unlinked", account.Status)
	requireNoOIDCAccountIdentity(t, account)
	require.Empty(t, f.keys.lookups)
}

func TestOIDCAccountUsesEffectiveLoginClientConfigFallback(t *testing.T) {
	f := newOIDCAccountFixture(t)
	delete(f.settings.values, service.SettingKeyOIDCConnectClientID)
	f.cfg.OIDC.ClientID = "deployment-login-client"
	response, account := f.request(t, service.AuthMethodOIDC, 501, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "deployment-login-client", *account.ClientID)
	summary, err := url.Parse(*account.SummaryURL)
	require.NoError(t, err)
	require.Equal(t, "deployment-login-client", summary.Query().Get("client_id"))
}

func TestOIDCAccountNeverUsesLocalAdminSessionAsAuthIdentity(t *testing.T) {
	f := newOIDCAccountFixture(t)
	f.user.Role = service.RoleAdmin
	response, account := f.request(t, "", 501, "?auth_method=oidc")
	require.Equal(t, http.StatusUnauthorized, response.Code)
	requireNoOIDCAccountIdentity(t, account)
	require.Empty(t, f.keys.lookups)
}

func TestOIDCAccountOIDCAdminRemainsAvailableInBackendMode(t *testing.T) {
	f := newOIDCAccountFixture(t)
	f.user.Role = service.RoleAdmin
	f.settings.values[service.SettingKeyBackendModeEnabled] = "true"
	response, account := f.request(t, service.AuthMethodOIDC, 501, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "linked", account.Status)
}

func TestOIDCAccountPrerequisiteAndStorageFailuresExposeNoIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*oidcAccountFixture)
	}{
		{"disabled connector", func(f *oidcAccountFixture) { f.cfg.BillingCenter.Enabled = false }},
		{"issuer mismatch", func(f *oidcAccountFixture) {
			f.settings.values[service.SettingKeyOIDCConnectIssuerURL] = "https://other-auth.example"
		}},
		{"invalid resource policy", func(f *oidcAccountFixture) { f.cfg.Gateway.ResourceServer.RequiredScopes = "" }},
		{"missing login client", func(f *oidcAccountFixture) {
			delete(f.settings.values, service.SettingKeyOIDCConnectClientID)
			f.cfg.OIDC.ClientID = ""
		}},
		{"HTTP issuer", func(f *oidcAccountFixture) {
			f.settings.values[service.SettingKeyOIDCConnectIssuerURL] = "http://auth.tabro.cn"
			f.cfg.Gateway.ResourceServer.IssuerURL = "http://auth.tabro.cn"
		}},
		{"empty issuer query", func(f *oidcAccountFixture) {
			f.settings.values[service.SettingKeyOIDCConnectIssuerURL] = "https://auth.tabro.cn?"
			f.cfg.Gateway.ResourceServer.IssuerURL = "https://auth.tabro.cn?"
		}},
		{"settings unavailable", func(f *oidcAccountFixture) { f.settings.err = errors.New("storage failure with sensitive detail") }},
		{"key storage unavailable", func(f *oidcAccountFixture) { f.keys.err = errors.New("storage failure with sensitive detail") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOIDCAccountFixture(t)
			test.change(f)
			response, account := f.request(t, service.AuthMethodOIDC, 501, "")
			require.Equal(t, http.StatusServiceUnavailable, response.Code)
			requireNoOIDCAccountIdentity(t, account)
			require.Contains(t, response.Body.String(), "OIDC_ACCOUNT_UNAVAILABLE")
			require.NotContains(t, response.Body.String(), "sensitive detail")
			require.NotContains(t, response.Body.String(), "auth-user-41")
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		})
	}
}
