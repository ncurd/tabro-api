package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type oidcBillingGuardSettings struct {
	service.SettingRepository
	enabled string
	err     error
}

func (s *oidcBillingGuardSettings) GetValue(context.Context, string) (string, error) {
	return s.enabled, s.err
}

func TestOIDCBillingDisablesLocalMoneyAndKeepsOperationalRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/redeem", `{}`, 410},
		{"GET", "/api/v1/subscriptions/active", ``, 410},
		{"POST", "/api/v1/admin/subscriptions/assign", `{}`, 410},
		{"PUT", "/api/v1/admin/promo-codes/1", `{}`, 410},
		{"POST", "/api/v1/admin/redeem-codes", `{}`, 410},
		{"POST", "/api/v1/payment/orders", `{}`, 410},
		{"POST", "/api/v1/admin/users/1/balance", `{}`, 410},
		{"PUT", "/api/v1/admin/users/1", `{"Balance":0}`, 410},
		{"POST", "/api/v1/admin/users", `{"balance":10}`, 410},
		{"POST", "/api/v1/admin/users", `{"balance":0}`, 204},
		{"PUT", "/api/v1/admin/users/1", `{"status":"disabled"}`, 204},
		{"GET", "/api/v1/admin/users/1/balance-history", ``, 204},
		{"GET", "/api/v1/payment/orders/my", ``, 204},
		{"POST", "/api/v1/payment/webhook/stripe", `{}`, 204},
		{"POST", "/api/v1/payment/orders/1/refund-request", `{}`, 204},
		{"POST", "/v1/responses", `{}`, 204},
		{"POST", "/api/v1/admin/accounts/1/reset-quota", `{}`, 204},
		{"PUT", "/api/v1/admin/settings", `{}`, 204},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			r := gin.New()
			r.Use(OIDCBilling(service.NewSettingService(&oidcBillingGuardSettings{enabled: "true"}, nil)))
			r.Handle(tc.method, tc.path, func(c *gin.Context) { c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			require.Equal(t, tc.status, w.Code)
		})
	}
}

func TestOIDCBillingGuardDefaultsOffAndFailsClosedOnStorageError(t *testing.T) {
	for _, tc := range []struct {
		settings *oidcBillingGuardSettings
		status   int
	}{
		{&oidcBillingGuardSettings{}, 204},
		{&oidcBillingGuardSettings{err: service.ErrSettingNotFound}, 204},
		{&oidcBillingGuardSettings{err: errors.New("database unavailable")}, 410},
	} {
		r := gin.New()
		r.Use(OIDCBilling(service.NewSettingService(tc.settings, nil)))
		r.POST("/api/v1/redeem", func(c *gin.Context) { c.Status(204) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/redeem", strings.NewReader(`{}`)))
		require.Equal(t, tc.status, w.Code)
	}
}
