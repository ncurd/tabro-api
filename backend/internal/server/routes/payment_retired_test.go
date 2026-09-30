package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPaymentOrderCreationRouteIsRetiredWithoutTouchingHistoricalRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterPaymentRoutes(router.Group("/api/v1"), &handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{}, &admin.PaymentHandler{},
		middleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() }),
		middleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() }), nil)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/payment/orders",
		strings.NewReader(`{"amount":10,"payment_type":"alipay"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusGone, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), `"reason":"ACCOUNT_CENTER_REQUIRED"`)
	require.Contains(t, response.Body.String(), "Auth account center")

	routes := router.Routes()
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/payment/orders/my"},
		{http.MethodPost, "/api/v1/payment/orders/:id/refund-request"},
		{http.MethodPost, "/api/v1/payment/webhook/alipay"},
		{http.MethodPost, "/api/v1/payment/webhook/wxpay"},
	} {
		found := false
		for _, registered := range routes {
			if registered.Method == route.method && registered.Path == route.path {
				found = true
				break
			}
		}
		require.True(t, found, "%s %s should remain registered", route.method, route.path)
	}
}

func TestNewGatewayPaymentAdminConfigurationIsRetired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterPaymentRoutes(router.Group("/api/v1"), &handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{}, &admin.PaymentHandler{},
		middleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() }),
		middleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() }), nil)

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/payment/plans"},
		{http.MethodPut, "/api/v1/admin/payment/plans/1"},
		{http.MethodDelete, "/api/v1/admin/payment/plans/1"},
		{http.MethodPost, "/api/v1/admin/payment/providers"},
		{http.MethodDelete, "/api/v1/admin/payment/providers/1"},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusGone, response.Code, "%s %s", route.method, route.path)
		require.Contains(t, response.Body.String(), `"reason":"ACCOUNT_CENTER_REQUIRED"`)
	}

	for _, historical := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/payment/dashboard"},
		{http.MethodGet, "/api/v1/admin/payment/orders"},
		{http.MethodGet, "/api/v1/admin/payment/orders/:id"},
		{http.MethodPost, "/api/v1/admin/payment/orders/:id/cancel"},
		{http.MethodPost, "/api/v1/admin/payment/orders/:id/retry"},
		{http.MethodPost, "/api/v1/admin/payment/orders/:id/refund"},
		{http.MethodPut, "/api/v1/admin/payment/providers/:id"},
	} {
		found := false
		for _, registered := range router.Routes() {
			if registered.Method == historical.method && registered.Path == historical.path {
				found = true
				break
			}
		}
		require.True(t, found, "%s %s should remain registered", historical.method, historical.path)
	}
}
