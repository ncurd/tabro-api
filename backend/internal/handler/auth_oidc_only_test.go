package handler

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

type oidcOnlyHandlerSettingRepo struct {
	service.SettingRepository
	values map[string]string
}

func (r *oidcOnlyHandlerSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *oidcOnlyHandlerSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func newOIDCOnlyAuthTestRouter(enabled bool) *gin.Engine {
	cfg := &config.Config{}
	settingRepo := &oidcOnlyHandlerSettingRepo{values: map[string]string{
		service.SettingKeyOIDCOnlyEnabled: "false",
	}}
	if enabled {
		settingRepo.values[service.SettingKeyOIDCOnlyEnabled] = "true"
	}
	settingSvc := service.NewSettingService(settingRepo, cfg)
	h := NewAuthHandler(cfg, nil, nil, nil, settingSvc, nil, nil, nil)

	router := gin.New()
	router.POST("/register", h.Register)
	router.POST("/login", h.Login)
	router.POST("/login/2fa", h.Login2FA)
	router.POST("/send-verify-code", h.SendVerifyCode)
	router.POST("/forgot-password", h.ForgotPassword)
	router.POST("/reset-password", h.ResetPassword)
	router.GET("/oauth/linuxdo/start", h.LinuxDoOAuthStart)
	router.GET("/oauth/linuxdo/callback", h.LinuxDoOAuthCallback)
	router.POST("/oauth/linuxdo/complete-registration", h.CompleteLinuxDoOAuthRegistration)
	router.GET("/oauth/oidc/start", h.OIDCOAuthStart)
	return router
}

func TestOIDCOnlyModeRejectsNonOIDCAuthBeforeRequestProcessing(t *testing.T) {
	router := newOIDCOnlyAuthTestRouter(true)
	requests := []struct {
		name   string
		method string
		path   string
	}{
		{"local register", http.MethodPost, "/register"},
		{"local login", http.MethodPost, "/login"},
		{"local 2fa completion", http.MethodPost, "/login/2fa"},
		{"registration code", http.MethodPost, "/send-verify-code"},
		{"forgot password", http.MethodPost, "/forgot-password"},
		{"reset password", http.MethodPost, "/reset-password"},
		{"LinuxDo start", http.MethodGet, "/oauth/linuxdo/start"},
		{"LinuxDo callback", http.MethodGet, "/oauth/linuxdo/callback"},
		{"LinuxDo complete registration", http.MethodPost, "/oauth/linuxdo/complete-registration"},
	}

	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusForbidden, response.Code)
			var body struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			require.Equal(t, "OIDC_ONLY_LOGIN_REQUIRED", body.Reason)
		})
	}

	// The OIDC route must remain reachable; with no provider configured it
	// returns its own configuration error instead of the non-OIDC guard error.
	request := httptest.NewRequest(http.MethodGet, "/oauth/oidc/start", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.NotContains(t, response.Body.String(), "OIDC_ONLY_LOGIN_REQUIRED")
}

func TestOIDCOnlyModeOffDoesNotPreemptLocalAuthRequestValidation(t *testing.T) {
	router := newOIDCOnlyAuthTestRouter(false)
	for _, path := range []string{
		"/register", "/login", "/login/2fa", "/send-verify-code", "/forgot-password", "/reset-password", "/oauth/linuxdo/complete-registration",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.NotContains(t, response.Body.String(), "OIDC_ONLY_LOGIN_REQUIRED")
		})
	}

	for _, path := range []string{"/oauth/linuxdo/start", "/oauth/linuxdo/callback"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusNotFound, response.Code)
			require.NotContains(t, response.Body.String(), "OIDC_ONLY_LOGIN_REQUIRED")
		})
	}
}
