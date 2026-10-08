//go:build unit

package admin

import (
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

func TestUpdateSettingsOIDCBillingPolicy(t *testing.T) {
	tests := []struct {
		name                 string
		enabled, unsupported bool
		changes              map[string]any
		status               int
		reason               string
	}{
		{"enable", false, false, map[string]any{"oidc_billing_enabled": true, "oidc_billing_rate_multiplier": 3.25, "oidc_billing_settlement_time": "01:30", "oidc_billing_settlement_timezone": "UTC"}, http.StatusOK, ""},
		{"omitted fields retain policy", true, false, nil, http.StatusOK, ""},
		{"disable", true, false, map[string]any{"oidc_billing_enabled": false}, http.StatusOK, ""},
		{"zero multiplier", false, false, map[string]any{"oidc_billing_rate_multiplier": 0}, http.StatusBadRequest, "OIDC_BILLING_INVALID_MULTIPLIER"},
		{"invalid schedule", false, false, map[string]any{"oidc_billing_settlement_time": "24:00"}, http.StatusBadRequest, "OIDC_BILLING_INVALID_SETTLEMENT_TIME"},
		{"invalid timezone", false, false, map[string]any{"oidc_billing_settlement_timezone": "Local"}, http.StatusBadRequest, "OIDC_BILLING_INVALID_TIMEZONE"},
		{"unsupported cannot spoof capability", false, true, map[string]any{"oidc_billing_enabled": true, "oidc_billing_supported": true}, http.StatusBadRequest, "OIDC_BILLING_NOT_SUPPORTED"},
		{"cannot disable OIDC only while billing enabled", true, false, map[string]any{"oidc_only_enabled": false}, http.StatusBadRequest, "OIDC_BILLING_REQUIRES_ONLY_MODE"},
		{"cannot change issuer while billing enabled", true, false, map[string]any{"oidc_connect_issuer_url": "https://other.example"}, http.StatusBadRequest, "OIDC_CONFIG_CHANGE_REQUIRES_MODE_OFF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, repo := newOIDCOnlyAdminSettingsFixture(service.AuthMethodOIDC)
			repo.values[service.SettingKeyOIDCOnlyEnabled] = "true"
			repo.values[service.SettingKeyOIDCBillingEnabled] = "false"
			if tt.enabled {
				repo.values[service.SettingKeyOIDCBillingEnabled] = "true"
			}
			repo.values[service.SettingKeyOIDCBillingRateMultiplier] = "2.5"
			repo.values[service.SettingKeyOIDCBillingSettlementTime] = "00:00"
			repo.values[service.SettingKeyOIDCBillingSettlementTimezone] = "Asia/Shanghai"
			cfg := &config.Config{}
			cfg.BillingCenter = config.BillingCenterConfig{Enabled: !tt.unsupported, BaseURL: "https://issuer.example/billing", TokenURL: "https://issuer.example/token", ProducerClientID: "tabro", ClientSecret: "secret"}
			cfg.Gateway.ResourceServer.Enabled = true
			cfg.Gateway.ResourceServer.IssuerURL = "https://issuer.example/oidc"
			handler := NewSettingHandler(service.NewSettingService(repo, cfg), nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("auth_method", service.AuthMethodOIDC); c.Next() })
			router.PUT("/admin/settings", handler.UpdateSettings)
			body := map[string]any{
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
			for key, value := range tt.changes {
				body[key] = value
			}
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(string(encoded)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tt.status, response.Code, response.Body.String())
			if tt.status != http.StatusOK {
				require.Contains(t, response.Body.String(), tt.reason)
				require.Zero(t, repo.setCalls)
				return
			}
			require.Equal(t, 1, repo.setCalls)
			if tt.changes == nil {
				require.Equal(t, "true", repo.values[service.SettingKeyOIDCBillingEnabled])
				require.Equal(t, "2.5", repo.values[service.SettingKeyOIDCBillingRateMultiplier])
				require.Equal(t, "00:00", repo.values[service.SettingKeyOIDCBillingSettlementTime])
				require.Equal(t, "Asia/Shanghai", repo.values[service.SettingKeyOIDCBillingSettlementTimezone])
			}
			require.Contains(t, response.Body.String(), `"oidc_billing_supported":true`)
			if tt.name == "enable" {
				require.Equal(t, "3.25", repo.values[service.SettingKeyOIDCBillingRateMultiplier])
				require.Equal(t, "01:30", repo.values[service.SettingKeyOIDCBillingSettlementTime])
				require.Equal(t, "UTC", repo.values[service.SettingKeyOIDCBillingSettlementTimezone])
			}
		})
	}
}
