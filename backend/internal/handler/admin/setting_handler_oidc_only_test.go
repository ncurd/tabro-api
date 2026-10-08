//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOIDCLoginConfigChanged(t *testing.T) {
	previous := &service.SystemSettings{
		OIDCConnectEnabled:             true,
		OIDCConnectClientID:            "client",
		OIDCConnectClientSecret:        "secret",
		OIDCConnectIssuerURL:           "https://issuer.example/oidc",
		OIDCConnectAuthorizeURL:        "https://issuer.example/authorize",
		OIDCConnectTokenURL:            "https://issuer.example/token",
		OIDCConnectJWKSURL:             "https://issuer.example/jwks",
		OIDCConnectScopes:              "openid email",
		OIDCConnectRedirectURL:         "https://app.example/api/v1/auth/oauth/oidc/callback",
		OIDCConnectFrontendRedirectURL: "/auth/oidc/callback",
		OIDCConnectTokenAuthMethod:     "client_secret_post",
		OIDCConnectValidateIDToken:     true,
		OIDCConnectAllowedSigningAlgs:  "RS256",
		OIDCConnectClockSkewSeconds:    120,
	}
	request := UpdateSettingsRequest{
		OIDCConnectEnabled:             previous.OIDCConnectEnabled,
		OIDCConnectClientID:            previous.OIDCConnectClientID,
		OIDCConnectClientSecret:        previous.OIDCConnectClientSecret,
		OIDCConnectIssuerURL:           previous.OIDCConnectIssuerURL,
		OIDCConnectAuthorizeURL:        previous.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:            previous.OIDCConnectTokenURL,
		OIDCConnectJWKSURL:             previous.OIDCConnectJWKSURL,
		OIDCConnectScopes:              previous.OIDCConnectScopes,
		OIDCConnectRedirectURL:         previous.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL: previous.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:     previous.OIDCConnectTokenAuthMethod,
		OIDCConnectValidateIDToken:     previous.OIDCConnectValidateIDToken,
		OIDCConnectAllowedSigningAlgs:  previous.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:    previous.OIDCConnectClockSkewSeconds,
	}
	require.False(t, oidcLoginConfigChanged(request, previous))
	require.True(t, oidcLoginConfigChanged(request, nil))
	// A stale saved secret is ignored by a public PKCE client.
	publicClient := request
	publicClient.OIDCConnectTokenAuthMethod = "none"
	publicClient.OIDCConnectUsePKCE = true
	publicClient.OIDCConnectClientSecret = ""
	previousPublicClient := *previous
	previousPublicClient.OIDCConnectTokenAuthMethod = "none"
	previousPublicClient.OIDCConnectUsePKCE = true
	require.False(t, oidcLoginConfigChanged(publicClient, &previousPublicClient))

	tests := []struct {
		name   string
		change func(*UpdateSettingsRequest)
	}{
		{"provider disabled", func(r *UpdateSettingsRequest) { r.OIDCConnectEnabled = false }},
		{"client changed", func(r *UpdateSettingsRequest) { r.OIDCConnectClientID = "other-client" }},
		{"secret changed", func(r *UpdateSettingsRequest) { r.OIDCConnectClientSecret = "other-secret" }},
		{"issuer changed", func(r *UpdateSettingsRequest) { r.OIDCConnectIssuerURL = "https://other.example/oidc" }},
		{"authorization endpoint changed", func(r *UpdateSettingsRequest) { r.OIDCConnectAuthorizeURL = "https://other.example/authorize" }},
		{"scopes changed", func(r *UpdateSettingsRequest) { r.OIDCConnectScopes = "openid profile" }},
		{"redirect changed", func(r *UpdateSettingsRequest) { r.OIDCConnectRedirectURL = "https://other.example/callback" }},
		{"token auth changed", func(r *UpdateSettingsRequest) { r.OIDCConnectTokenAuthMethod = "client_secret_basic" }},
		{"ID token validation changed", func(r *UpdateSettingsRequest) { r.OIDCConnectValidateIDToken = false }},
		{"email claim path changed", func(r *UpdateSettingsRequest) { r.OIDCConnectUserInfoEmailPath = "user.email" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := request
			tt.change(&changed)
			require.True(t, oidcLoginConfigChanged(changed, previous))
		})
	}
}

type oidcOnlyAdminSettingRepo struct {
	service.SettingRepository
	values   map[string]string
	setCalls int
}

func (r *oidcOnlyAdminSettingRepo) GetAll(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.values))
	for key, value := range r.values {
		values[key] = value
	}
	return values, nil
}

func (r *oidcOnlyAdminSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, exists := r.values[key]; exists {
			values[key] = value
		}
	}
	return values, nil
}

func (r *oidcOnlyAdminSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.setCalls++
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func newOIDCOnlyAdminSettingsFixture(authMethod string) (*gin.Engine, *oidcOnlyAdminSettingRepo) {
	repo := &oidcOnlyAdminSettingRepo{values: map[string]string{
		service.SettingKeyRegistrationEnabled:            "true",
		service.SettingKeyOIDCOnlyEnabled:                "false",
		service.SettingKeyOIDCConnectEnabled:             "true",
		service.SettingKeyOIDCConnectClientID:            "client",
		service.SettingKeyOIDCConnectClientSecret:        "secret",
		service.SettingKeyOIDCConnectIssuerURL:           "https://issuer.example/oidc",
		service.SettingKeyOIDCConnectAuthorizeURL:        "https://issuer.example/authorize",
		service.SettingKeyOIDCConnectTokenURL:            "https://issuer.example/token",
		service.SettingKeyOIDCConnectJWKSURL:             "https://issuer.example/jwks",
		service.SettingKeyOIDCConnectScopes:              "openid email",
		service.SettingKeyOIDCConnectRedirectURL:         "https://app.example/api/v1/auth/oauth/oidc/callback",
		service.SettingKeyOIDCConnectFrontendRedirectURL: "/auth/oidc/callback",
		service.SettingKeyOIDCConnectTokenAuthMethod:     "client_secret_post",
		service.SettingKeyOIDCConnectValidateIDToken:     "true",
		service.SettingKeyOIDCConnectAllowedSigningAlgs:  "RS256",
		service.SettingKeyOIDCConnectClockSkewSeconds:    "120",
	}}
	settings := service.NewSettingService(repo, &config.Config{})
	handler := NewSettingHandler(settings, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("auth_method", authMethod)
		c.Next()
	})
	router.PUT("/admin/settings", handler.UpdateSettings)
	return router, repo
}

func TestUpdateSettingsRequiresOIDCAdministratorToEnableOIDCOnlyMode(t *testing.T) {
	requestBody := map[string]any{
		"registration_enabled":               true,
		"oidc_only_enabled":                  true,
		"oidc_connect_enabled":               true,
		"oidc_connect_client_id":             "client",
		"oidc_connect_issuer_url":            "https://issuer.example/oidc",
		"oidc_connect_authorize_url":         "https://issuer.example/authorize",
		"oidc_connect_token_url":             "https://issuer.example/token",
		"oidc_connect_jwks_url":              "https://issuer.example/jwks",
		"oidc_connect_scopes":                "openid email",
		"oidc_connect_redirect_url":          "https://app.example/api/v1/auth/oauth/oidc/callback",
		"oidc_connect_frontend_redirect_url": "/auth/oidc/callback",
		"oidc_connect_token_auth_method":     "client_secret_post",
		"oidc_connect_validate_id_token":     true,
		"oidc_connect_allowed_signing_algs":  "RS256",
		"oidc_connect_clock_skew_seconds":    120,
	}
	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	for _, authMethod := range []string{"", "jwt", "admin_api_key"} {
		t.Run(authMethod, func(t *testing.T) {
			router, repo := newOIDCOnlyAdminSettingsFixture(authMethod)
			request := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Contains(t, response.Body.String(), "Sign in as an administrator through OIDC")
			require.Zero(t, repo.setCalls)
			require.Equal(t, "false", repo.values[service.SettingKeyOIDCOnlyEnabled])
		})
	}

	t.Run("OIDC administrator may enable", func(t *testing.T) {
		router, repo := newOIDCOnlyAdminSettingsFixture(service.AuthMethodOIDC)
		request := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, 1, repo.setCalls)
		require.Equal(t, "true", repo.values[service.SettingKeyOIDCOnlyEnabled])
	})

	t.Run("provider change must be saved and tested separately", func(t *testing.T) {
		changed := make(map[string]any, len(requestBody))
		for key, value := range requestBody {
			changed[key] = value
		}
		changed["oidc_connect_client_id"] = "other-client"
		changedBody, err := json.Marshal(changed)
		require.NoError(t, err)

		router, repo := newOIDCOnlyAdminSettingsFixture(service.AuthMethodOIDC)
		request := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(string(changedBody)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Contains(t, response.Body.String(), "Save and test OIDC configuration")
		require.Zero(t, repo.setCalls)
		require.Equal(t, "false", repo.values[service.SettingKeyOIDCOnlyEnabled])
	})
}
