package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OIDCBilling disables local financial entry points independently of the UI.
// Historical orders and signed callbacks remain available for existing orders.
func OIDCBilling(settings *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		method, path := c.Request.Method, strings.TrimSuffix(c.Request.URL.Path, "/")
		blocked := oidcBillingLocalFinancialAPI(method, path)
		userCreate := method == http.MethodPost && path == "/api/v1/admin/users"
		userUpdate := method == http.MethodPut && strings.HasPrefix(path, "/api/v1/admin/users/") && !strings.Contains(strings.TrimPrefix(path, "/api/v1/admin/users/"), "/")
		if settings == nil || (!blocked && !userCreate && !userUpdate) || !settings.IsOIDCBillingEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		if !blocked && (userCreate || userUpdate) {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1024*1024+1))
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			var fields map[string]json.RawMessage
			if err != nil || len(body) > 1024*1024 || json.Unmarshal(body, &fields) != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": "INVALID_REQUEST", "message": "Invalid user request"})
				return
			}
			for field, value := range fields {
				if !strings.EqualFold(field, "balance") {
					continue
				}
				var balance float64
				blocked = userUpdate || json.Unmarshal(value, &balance) != nil || balance != 0
				if blocked {
					break
				}
			}
		}
		if blocked {
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(http.StatusGone, gin.H{"code": "OIDC_BILLING_REQUIRED", "message": "OIDC billing is enabled; local balances, subscriptions and coupons are unavailable"})
			return
		}
		c.Next()
	}
}

func oidcBillingLocalFinancialAPI(method, path string) bool {
	for _, prefix := range []string{"/api/v1/redeem", "/api/v1/subscriptions"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if path == "/api/v1/auth/validate-promo-code" || path == "/api/v1/payment/plans" ||
		path == "/api/v1/payment/checkout-info" || path == "/api/v1/payment/channels" || path == "/api/v1/payment/limits" {
		return true
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return false
	}
	if path == "/api/v1/payment/orders" {
		return true
	}
	for _, prefix := range []string{"/api/v1/admin/redeem-codes", "/api/v1/admin/promo-codes", "/api/v1/admin/subscriptions", "/api/v1/admin/payment/plans"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "/api/v1/admin/users/") && strings.HasSuffix(path, "/balance")
}
