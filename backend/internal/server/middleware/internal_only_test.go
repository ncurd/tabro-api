package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalOnlyClosesUserMoneyButKeepsOperationsAndWebhooks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/auth/register", "{}", 410}, {"POST", "/api/v1/auth/forgot-password", "{}", 410},
		{"POST", "/api/v1/payment/orders", "{}", 410}, {"POST", "/api/v1/payment/orders/1/refund-request", "{}", 410},
		{"POST", "/api/v1/payment/public/orders/verify", "{}", 410}, {"POST", "/api/v1/redeem", "{}", 410}, {"GET", "/api/v1/keys", "", 410},
		{"POST", "/api/v1/admin/users/1/balance", "{}", 410}, {"POST", "/api/v1/admin/subscriptions/assign", "{}", 410},
		{"PUT", "/api/v1/admin/users/1", `{"Balance":123}`, 410}, {"PUT", "/api/v1/admin/users/1", `{"status":"disabled"}`, 204},
		{"POST", "/api/v1/admin/accounts/1/reset-quota", "{}", 204}, {"GET", "/api/v1/admin/accounts", "", 204},
		{"POST", "/api/v1/payment/webhook/stripe", "{}", 204}, {"GET", "/api/v1/payment/webhook/easypay", "", 204},
		{"POST", "/api/v1/auth/login", "{}", 204}, {"GET", "/api/v1/auth/oauth/oidc/callback", "", 204},
		{"GET", "/api/v1/user/totp/status", "", 204}, {"POST", "/v1/responses", "{}", 204}, {"GET", "/v1/responses", "", 204},
		{"POST", "/v1beta/models/gemini-3-pro:generateContent", "{}", 204}, {"GET", "/api/v1/settings/public", "", 204},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			r := gin.New()
			r.Use(InternalOnly(config.DeploymentConfig{InternalOnly: true, AccountCenterURL: "https://auth.example/account"}))
			r.Handle(tc.method, tc.path, func(c *gin.Context) { c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			require.Equal(t, tc.status, w.Code)
		})
	}
}
func TestInternalOnlyRedirectDoesNotForwardUserQueryOrTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(InternalOnly(config.DeploymentConfig{InternalOnly: true, AccountCenterURL: "https://auth.example/account"}))
	r.GET("/payment", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/payment?token=private&redirect=evil", nil))
	require.Equal(t, 303, w.Code)
	require.Equal(t, "https://auth.example/account", w.Header().Get("Location"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	r2 := gin.New()
	r2.Use(InternalOnly(config.DeploymentConfig{}))
	r2.POST("/api/v1/redeem", func(c *gin.Context) { c.Status(204) })
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/redeem", nil))
	require.Equal(t, 204, w.Code)
}
