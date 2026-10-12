package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accessKeyMiddlewareBillingRepo struct {
	service.GatewayBillingRepository
}

func (*accessKeyMiddlewareBillingRepo) Claim(context.Context, time.Duration, int) ([]bc.Event, error) {
	return nil, nil
}
func (*accessKeyMiddlewareBillingRepo) PendingBillingShadows(context.Context, int) ([]service.GatewayBillingShadow, error) {
	return nil, nil
}
func (*accessKeyMiddlewareBillingRepo) GetBillingRoute(context.Context, int64) (*service.GatewayBillingRoute, error) {
	return nil, nil
}
func (*accessKeyMiddlewareBillingRepo) GetBillingWorkspaceRoute(context.Context, string, string) (*service.GatewayBillingRoute, error) {
	return nil, nil
}

func TestCentralAccessKeyRevalidatesEveryNonBillableRequestAndRejectsLocalKeys(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	claims := fixture.claims()
	claims["azp"], claims["act"] = "tabro-api", map[string]any{"sub": "tabro-api"}
	claims["scope"] = "llm.invoke models.read"
	claims["access_key_id"], claims["access_key_version"] = "central-key", 2
	proof := fixture.sign(t, claims)
	var validations atomic.Int32
	var revoked atomic.Bool
	var requiredPermission atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Contains(t, r.Form.Get("scope"), "billing.read")
			_, _ = w.Write([]byte(`{"access_token":"service-token","token_type":"Bearer","expires_in":300}`))
		case "/internal/access-keys/v1/validate":
			validations.Add(1)
			require.Equal(t, "Bearer service-token", r.Header.Get("Authorization"))
			var request map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Equal(t, "tbrk_central-user-key", request["key"])
			require.Equal(t, "direct_api", request["mode"])
			require.Equal(t, "tabro-api", request["target_app_id"])
			requiredPermission.Store(request["required_permission"])
			if revoked.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"forbidden"}`))
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(bc.AccessKeyValidation{KeyID: "central-key", Version: 2, UserID: gatewayMiddlewareSubject, TenantID: gatewayMiddlewareTenant, OriginAppID: "tabro-api", Mode: "direct_api", TargetAppID: "tabro-api", Permissions: []string{"api.invoke", "api.read"}, SubjectAccessToken: proof}))
		default:
			t.Errorf("non-billable request unexpectedly called %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	fixture.cfg.BillingCenter = config.BillingCenterConfig{Enabled: true, BaseURL: server.URL, TokenURL: server.URL + "/token", ProducerClientID: "tabro-api-gateway", ClientSecret: "synthetic-test-secret", InsecureLocal: true}
	billing, err := service.ProvideGatewayBillingCoordinator(fixture.cfg, &accessKeyMiddlewareBillingRepo{}, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(billing.Stop)
	fixture.apiKeys.GatewayBilling = billing
	for _, test := range []struct {
		method, path, permission string
		websocket                bool
	}{
		{http.MethodGet, "/v1/models", "api.read", false},
		{http.MethodGet, "/v1/usage", "api.read", false},
		{http.MethodGet, "/v1/audio/voices/voice-owned", "api.read", false},
		{http.MethodDelete, "/v1/audio/voices/voice-owned", "api.invoke", false},
		{http.MethodPost, "/v1/messages/count_tokens", "api.invoke", false},
		{http.MethodGet, "/v1/realtime", "api.invoke", true},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			router := gin.New()
			router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
			router.Handle(test.method, test.path, func(c *gin.Context) {
				key, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.True(t, key.OIDCManaged)
				c.Status(http.StatusNoContent)
			})
			before := validations.Load()
			revoked.Store(false)
			for range 2 {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"model":"test"}`))
				req.Header.Set("Authorization", "Bearer tbrk_central-user-key")
				if test.websocket {
					req.Header.Set("Upgrade", "websocket")
				}
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
				require.Equal(t, test.permission, requiredPermission.Load())
			}
			revoked.Store(true)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("Authorization", "Bearer tbrk_central-user-key")
			if test.websocket {
				req.Header.Set("Upgrade", "websocket")
			}
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusForbidden, w.Code)
			require.Equal(t, before+3, validations.Load())
		})
	}
	// A self-asserted business-system header cannot turn a legacy secret into
	// an Auth-validated credential, even with OIDC billing's UI flag disabled.
	_, _, err = resolveGatewayCredential(context.Background(), "legacy-local-key", true, fixture.apiKeys, service.NewGatewayResourceServer(fixture.cfg, fixture.apiKeys))
	require.ErrorIs(t, err, errInvalidGatewayCredential)
	require.Zero(t, fixture.repo.getByKeyCalls)
}
