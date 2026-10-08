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

type billingConnectionAdminRepo struct {
	values map[string]string
	writes int
}

func (r *billingConnectionAdminRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	value, exists := r.values[key]
	if !exists {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *billingConnectionAdminRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *billingConnectionAdminRepo) Set(_ context.Context, key, value string) error {
	r.writes++
	r.values[key] = value
	return nil
}

func (r *billingConnectionAdminRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, exists := r.values[key]; exists {
			values[key] = value
		}
	}
	return values, nil
}

func (r *billingConnectionAdminRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.writes++
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *billingConnectionAdminRepo) GetAll(ctx context.Context) (map[string]string, error) {
	keys := make([]string, 0, len(r.values))
	for key := range r.values {
		keys = append(keys, key)
	}
	return r.GetMultiple(ctx, keys)
}

func (r *billingConnectionAdminRepo) Delete(_ context.Context, key string) error {
	r.writes++
	delete(r.values, key)
	return nil
}

func validAdminBillingConnection() service.OIDCBillingConnectionConfig {
	return service.OIDCBillingConnectionConfig{
		BillingCenter: config.BillingCenterConfig{
			Enabled:          true,
			BaseURL:          "https://issuer.example/billing",
			TokenURL:         "https://issuer.example/token",
			ProducerClientID: "tabro-billing",
			ClientSecret:     "billing-client-secret",
			TimeoutSeconds:   10,
		},
		ResourceServer: config.GatewayResourceServerConfig{
			Enabled:             true,
			IssuerURL:           "https://issuer.example/oidc",
			Audience:            "tabro-api",
			RequiredScopes:      "llm.invoke",
			AllowedClientIDs:    "tabro-client",
			AllowedSigningAlgs:  "RS256",
			ClockSkewSeconds:    120,
			JWKSCacheTTLSeconds: 300,
			TokenExchange: config.GatewayTokenExchangeConfig{
				ActorClaim:         "act",
				MaxDelegationDepth: 1,
			},
		},
	}
}

func newBillingConnectionAdminRouter(cfg *config.Config) (*gin.Engine, *billingConnectionAdminRepo) {
	repo := &billingConnectionAdminRepo{values: map[string]string{
		service.SettingKeyOIDCBillingEnabled:   "false",
		service.SettingKeyOIDCConnectEnabled:   "true",
		service.SettingKeyOIDCConnectIssuerURL: "https://issuer.example/oidc",
		service.SettingKeyOIDCOnlyEnabled:      "true",
		service.SettingKeyRegistrationEnabled:  "false",
	}}
	return billingConnectionAdminRouter(cfg, repo), repo
}

func billingConnectionAdminRouter(cfg *config.Config, repo *billingConnectionAdminRepo) *gin.Engine {
	h := NewSettingHandler(service.NewSettingService(repo, cfg), nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/admin/settings/oidc-billing-connection", h.GetOIDCBillingConnectionSettings)
	router.PUT("/admin/settings/oidc-billing-connection", h.UpdateOIDCBillingConnectionSettings)
	router.POST("/admin/settings/oidc-billing-connection/setup", h.SetupOIDCBillingConnectionSettings)
	return router
}

func billingConnectionAdminRequest(t *testing.T, router *gin.Engine, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/admin/settings/oidc-billing-connection", strings.NewReader(body))
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func decodeBillingConnectionAdminSettings(t *testing.T, recorder *httptest.ResponseRecorder) service.OIDCBillingConnectionSettings {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var envelope struct {
		Code int                                   `json:"code"`
		Data service.OIDCBillingConnectionSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	return envelope.Data
}

func TestGetOIDCBillingConnectionSettingsRedactsCredentials(t *testing.T) {
	connection := validAdminBillingConnection()
	connection.BillingCenter.Payments = config.BillingCenterPaymentsConfig{
		Enabled:              true,
		ProducerClientID:     "separate-payment-client",
		ClientSecret:         "payment-client-secret",
		CallbackBaseURL:      "https://app.example/payments",
		AccountCenterBaseURL: "https://issuer.example/account",
	}
	cfg := &config.Config{BillingCenter: connection.BillingCenter}
	cfg.Gateway.ResourceServer = connection.ResourceServer
	router, repo := newBillingConnectionAdminRouter(cfg)

	recorder := billingConnectionAdminRequest(t, router, http.MethodGet, "")
	settings := decodeBillingConnectionAdminSettings(t, recorder)
	require.Equal(t, connection.BillingCenter.BaseURL, settings.BillingCenter.BaseURL)
	require.Empty(t, settings.BillingCenter.ClientSecret)
	require.Zero(t, settings.BillingCenter.Payments)
	require.True(t, settings.ClientSecretConfigured)
	require.True(t, settings.OIDCBillingSupported)
	require.NotContains(t, recorder.Body.String(), "billing-client-secret")
	require.NotContains(t, recorder.Body.String(), "payment-client-secret")
	require.NotContains(t, recorder.Body.String(), "separate-payment-client")
	require.Zero(t, repo.writes)
}

func TestUpdateOIDCBillingConnectionSettingsPersistsAndRetainsSecret(t *testing.T) {
	router, repo := newBillingConnectionAdminRouter(&config.Config{})
	connection := validAdminBillingConnection()
	body, err := json.Marshal(connection)
	require.NoError(t, err)

	recorder := billingConnectionAdminRequest(t, router, http.MethodPut, string(body))
	settings := decodeBillingConnectionAdminSettings(t, recorder)
	require.True(t, settings.OIDCBillingSupported)
	require.True(t, settings.ClientSecretConfigured)
	require.Empty(t, settings.BillingCenter.ClientSecret)
	require.NotContains(t, recorder.Body.String(), "billing-client-secret")
	require.Positive(t, repo.writes)

	connection.BillingCenter.ClientSecret = ""
	connection.BillingCenter.TimeoutSeconds = 20
	body, err = json.Marshal(connection)
	require.NoError(t, err)
	recorder = billingConnectionAdminRequest(t, router, http.MethodPut, string(body))
	settings = decodeBillingConnectionAdminSettings(t, recorder)
	require.Equal(t, 20, settings.BillingCenter.TimeoutSeconds)
	require.True(t, settings.ClientSecretConfigured)
	require.True(t, settings.OIDCBillingSupported)
	require.Empty(t, settings.BillingCenter.ClientSecret)

	// A fresh service must load the retained secret from persistent settings,
	// instead of relying on the first service's in-memory configuration.
	router = billingConnectionAdminRouter(&config.Config{}, repo)
	recorder = billingConnectionAdminRequest(t, router, http.MethodGet, "")
	settings = decodeBillingConnectionAdminSettings(t, recorder)
	require.True(t, settings.ClientSecretConfigured)
	require.True(t, settings.OIDCBillingSupported)
	require.Equal(t, 20, settings.BillingCenter.TimeoutSeconds)
	require.NotContains(t, recorder.Body.String(), "billing-client-secret")
}

func TestUpdateOIDCBillingConnectionSettingsRejectsInvalidGateway(t *testing.T) {
	router, repo := newBillingConnectionAdminRouter(&config.Config{})
	connection := validAdminBillingConnection()
	connection.ResourceServer.Audience = ""
	body, err := json.Marshal(connection)
	require.NoError(t, err)

	recorder := billingConnectionAdminRequest(t, router, http.MethodPut, string(body))
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
	require.NotContains(t, recorder.Body.String(), "billing-client-secret")
}

func TestUpdateOIDCBillingConnectionSettingsRejectsMalformedJSON(t *testing.T) {
	router, repo := newBillingConnectionAdminRouter(&config.Config{})
	recorder := billingConnectionAdminRequest(t, router, http.MethodPut, `{"billing_center":"invalid"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
}

func billingConnectionAdminSetupRequest(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/oidc-billing-connection/setup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestSetupOIDCBillingConnectionSettingsRejectsMalformedJSON(t *testing.T) {
	router, repo := newBillingConnectionAdminRouter(&config.Config{})
	recorder := billingConnectionAdminSetupRequest(router, `{"client_secret":42}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
}

func TestSetupOIDCBillingConnectionSettingsRequiresSavedOIDC(t *testing.T) {
	router, repo := newBillingConnectionAdminRouter(&config.Config{})
	repo.values[service.SettingKeyOIDCConnectEnabled] = "false"
	recorder := billingConnectionAdminSetupRequest(router, `{"client_secret":"do-not-leak-secret"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
	require.NotContains(t, recorder.Body.String(), "do-not-leak-secret")
}
