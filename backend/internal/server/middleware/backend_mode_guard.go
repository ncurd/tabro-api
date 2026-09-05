package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// BackendModeUserGuard blocks non-admin users from accessing user routes when backend mode is enabled.
// Must be placed AFTER JWT auth middleware so that the user role is available in context.
func BackendModeUserGuard(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil || !settingService.IsBackendModeEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		role, _ := GetUserRoleFromContext(c)
		if role == "admin" {
			c.Next()
			return
		}
		response.Forbidden(c, "Backend mode is active. User self-service is disabled.")
		c.Abort()
	}
}

// BackendModeAuthGuard selectively blocks auth endpoints when backend mode is enabled.
// Allows: login, login/2fa, logout, refresh, and the OIDC GET start/callback endpoints.
// Blocks: register, forgot-password, reset-password, OAuth registration, other OAuth providers, etc.
func BackendModeAuthGuard(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil || !settingService.IsBackendModeEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		// OIDC is the only OAuth flow available in backend mode. Keep this
		// allowlist method- and path-exact so registration and other OAuth
		// endpoints remain unavailable.
		if c.Request.Method == http.MethodGet &&
			(path == "/api/v1/auth/oauth/oidc/start" || path == "/api/v1/auth/oauth/oidc/callback") {
			c.Next()
			return
		}
		// Allow login, 2FA, logout, refresh, public settings
		allowedSuffixes := []string{"/auth/login", "/auth/login/2fa", "/auth/logout", "/auth/refresh"}
		for _, suffix := range allowedSuffixes {
			if strings.HasSuffix(path, suffix) {
				c.Next()
				return
			}
		}
		response.Forbidden(c, "Backend mode is active. Registration and self-service auth flows are disabled.")
		c.Abort()
	}
}
