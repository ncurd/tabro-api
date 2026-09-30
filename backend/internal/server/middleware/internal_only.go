package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// InternalOnly is a server policy, not a menu visibility setting. It applies
// before the embedded SPA and remains effective with independently served UI.
func InternalOnly(cfg config.DeploymentConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cfg.InternalOnly {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		blocked := internalOnlyBlockedAPI(c.Request.Method, path)
		if c.Request.Method == http.MethodPut && strings.HasPrefix(path, "/api/v1/admin/users/") && !strings.Contains(strings.TrimPrefix(path, "/api/v1/admin/users/"), "/") {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1024*1024+1))
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			var fields map[string]json.RawMessage
			if err != nil || len(body) > 1024*1024 || json.Unmarshal(body, &fields) != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": "INVALID_REQUEST", "message": "Invalid user update"})
				return
			}
			for field := range fields {
				if strings.EqualFold(field, "balance") {
					blocked = true
				}
			}
		}
		if blocked {
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(http.StatusGone, gin.H{"code": "ACCOUNT_CENTER_REQUIRED", "message": "Account and billing self-service is available in the account center", "account_center_url": cfg.AccountCenterURL})
			return
		}
		if (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) && internalOnlyUserPage(path) {
			c.Header("Cache-Control", "no-store")
			c.Redirect(http.StatusSeeOther, cfg.AccountCenterURL)
			c.Abort()
			return
		}
		c.Next()
	}
}
func internalOnlyBlockedAPI(method, path string) bool {
	if !strings.HasPrefix(path, "/api/v1/") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/auth/") {
		switch path {
		case "/api/v1/auth/login", "/api/v1/auth/login/2fa", "/api/v1/auth/refresh", "/api/v1/auth/logout", "/api/v1/auth/me", "/api/v1/auth/revoke-all-sessions", "/api/v1/auth/oauth/oidc/start", "/api/v1/auth/oauth/oidc/callback":
			return false
		default:
			return true
		}
	}
	// /user remains behind JWT + forced BackendModeUserGuard: only operators
	// may maintain their password, profile and TOTP while self-service is closed.
	for _, prefix := range []string{"/api/v1/keys", "/api/v1/groups", "/api/v1/model-pricing", "/api/v1/usage", "/api/v1/announcements", "/api/v1/redeem", "/api/v1/subscriptions"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if path == "/api/v1/payment" || strings.HasPrefix(path, "/api/v1/payment/") {
		return !strings.HasPrefix(path, "/api/v1/payment/webhook/")
	}
	if method != http.MethodGet && method != http.MethodHead {
		for _, prefix := range []string{"/api/v1/admin/redeem-codes", "/api/v1/admin/promo-codes", "/api/v1/admin/subscriptions", "/api/v1/admin/payment/orders", "/api/v1/admin/payment/plans"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		if path == "/api/v1/admin/users" || strings.HasPrefix(path, "/api/v1/admin/users/") && strings.HasSuffix(path, "/balance") {
			return true
		}
	}
	return false
}
func internalOnlyUserPage(path string) bool {
	if path == "/" {
		return true
	}
	for _, prefix := range []string{"/home", "/dashboard", "/keys", "/usage", "/model-pricing", "/subscriptions", "/redeem", "/payment", "/purchase", "/orders", "/register", "/email-verify", "/forgot-password", "/reset-password", "/custom", "/key-usage", "/auth/linuxdo/callback", "/auth/callback"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
