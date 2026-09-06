package middleware

import (
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// APIKeyAuthGoogle is a Google-style error wrapper for API key auth.
func APIKeyAuthGoogle(apiKeyService *service.APIKeyService, cfg *config.Config) gin.HandlerFunc {
	return gatewayAuthWithSubscriptionGoogle(apiKeyService, nil, nil, cfg)
}

// APIKeyAuthWithSubscriptionGoogle behaves like ApiKeyAuthWithSubscription but returns Google-style errors:
// {"error":{"code":401,"message":"...","status":"UNAUTHENTICATED"}}
//
// It is intended for Gemini native endpoints (/v1beta) to match Gemini SDK expectations.
func APIKeyAuthWithSubscriptionGoogle(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, cfg *config.Config) gin.HandlerFunc {
	return gatewayAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, nil, cfg)
}

// GatewayAuthWithSubscriptionGoogle is the production Gemini middleware. It
// accepts gateway-specific external OAuth access tokens while retaining the
// legacy constructor above for callers that only need API-key authentication.
func GatewayAuthWithSubscriptionGoogle(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, _ *service.AuthService, cfg *config.Config) gin.HandlerFunc {
	resourceServer := service.NewGatewayResourceServer(cfg, apiKeyService)
	return gatewayAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, resourceServer, cfg)
}

func gatewayAuthWithSubscriptionGoogle(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, resourceServer *service.GatewayResourceServer, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if v := strings.TrimSpace(c.Query("api_key")); v != "" {
			abortWithGoogleError(c, 400, "Query parameter api_key is deprecated. Use Authorization header or key instead.")
			return
		}
		credential, extractErr := extractGatewayCredentialInput(c, allowGoogleQueryKey(c.Request.URL.Path))
		if extractErr != nil {
			c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
			abortWithGoogleError(c, 401, "Invalid or conflicting authentication credentials")
			return
		}
		apiKeyString := credential.value
		if apiKeyString == "" {
			abortWithGoogleError(c, 401, "API key is required")
			return
		}

		apiKey, oidcPrincipal, err := resolveGatewayCredential(c.Request.Context(), apiKeyString, credential.fromAuthorization, apiKeyService, resourceServer)
		if err != nil {
			if errors.Is(err, service.ErrGatewayOIDCScopeDenied) {
				requiredScopes := gatewayRequiredScopeChallenge(cfg)
				c.Header("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+requiredScopes+`"`)
				abortWithGoogleError(c, 403, "Token does not grant the required gateway scope")
				return
			}
			if errors.Is(err, service.ErrGatewayOIDCIdentityNotBound) {
				abortWithGoogleError(c, 403, "OIDC identity is not linked to a gateway billing account")
				return
			}
			if errors.Is(err, service.ErrGatewayOIDCUnavailable) {
				abortWithGoogleError(c, 503, "OIDC token validation is temporarily unavailable")
				return
			}
			if errors.Is(err, service.ErrAPIKeyNotFound) || errors.Is(err, errInvalidGatewayCredential) || errors.Is(err, service.ErrTokenRevoked) {
				c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
				abortWithGoogleError(c, 401, "Invalid API key")
				return
			}
			if errors.Is(err, service.ErrGatewayOIDCTokenInvalid) {
				c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
				abortWithGoogleError(c, 401, "Invalid OAuth access token")
				return
			}
			abortWithGoogleError(c, 500, "Failed to validate API key")
			return
		}

		// Match the standard gateway's split between authentication policy
		// (always enforced) and billing policy (skipped only in simple mode).
		// Expired/quota-exhausted states are handled below with their proper
		// 403/429 semantics instead of being flattened into "disabled".
		if !apiKey.IsActive() &&
			apiKey.Status != service.StatusAPIKeyExpired &&
			apiKey.Status != service.StatusAPIKeyQuotaExhausted {
			abortWithGoogleError(c, 401, "API key is disabled")
			return
		}
		if len(apiKey.IPWhitelist) > 0 || len(apiKey.IPBlacklist) > 0 {
			clientIP := ip.GetTrustedClientIP(c)
			allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, apiKey.CompiledIPWhitelist, apiKey.CompiledIPBlacklist)
			if !allowed {
				abortWithGoogleError(c, 403, "Access denied")
				return
			}
		}
		if apiKey.User == nil {
			abortWithGoogleError(c, 401, "User associated with API key not found")
			return
		}
		if !apiKey.User.IsActive() {
			abortWithGoogleError(c, 401, "User account is not active")
			return
		}
		if err := attachGatewayRequestIdentity(c, apiKey, oidcPrincipal); err != nil {
			abortWithGoogleError(c, 400, "Idempotency-Key must be at most 128 visible ASCII characters without spaces")
			return
		}

		// 简易模式：跳过余额和订阅检查
		if cfg.RunMode == config.RunModeSimple {
			c.Set(string(ContextKeyAPIKey), apiKey)
			c.Set(string(ContextKeyUser), AuthSubject{
				UserID:      apiKey.User.ID,
				Concurrency: apiKey.User.Concurrency,
			})
			c.Set(string(ContextKeyUserRole), apiKey.User.Role)
			setGroupContext(c, apiKey.Group)
			_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
			c.Next()
			return
		}

		switch apiKey.Status {
		case service.StatusAPIKeyQuotaExhausted:
			abortWithGoogleError(c, 429, "API key quota exhausted")
			return
		case service.StatusAPIKeyExpired:
			abortWithGoogleError(c, 403, "API key expired")
			return
		}
		if apiKey.IsExpired() {
			abortWithGoogleError(c, 403, "API key expired")
			return
		}
		if apiKey.IsQuotaExhausted() {
			abortWithGoogleError(c, 429, "API key quota exhausted")
			return
		}

		isSubscriptionType := apiKey.Group != nil && apiKey.Group.IsSubscriptionType()
		if isSubscriptionType && subscriptionService != nil {
			subscription, err := subscriptionService.GetActiveSubscription(
				c.Request.Context(),
				apiKey.User.ID,
				apiKey.Group.ID,
			)
			if err != nil {
				abortWithGoogleError(c, 403, "No active subscription found for this group")
				return
			}

			needsMaintenance, err := subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
			if err != nil {
				status := 403
				if errors.Is(err, service.ErrDailyLimitExceeded) ||
					errors.Is(err, service.ErrWeeklyLimitExceeded) ||
					errors.Is(err, service.ErrMonthlyLimitExceeded) {
					status = 429
				}
				abortWithGoogleError(c, status, err.Error())
				return
			}

			c.Set(string(ContextKeySubscription), subscription)

			if needsMaintenance {
				maintenanceCopy := *subscription
				subscriptionService.DoWindowMaintenance(&maintenanceCopy)
			}
		} else {
			if apiKey.User.Balance <= 0 {
				abortWithGoogleError(c, 403, "Insufficient account balance")
				return
			}
		}

		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
		setGroupContext(c, apiKey.Group)
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
		c.Next()
	}
}

func googleCredentialCameFromHeader(c *gin.Context) bool {
	if strings.TrimSpace(c.GetHeader("x-goog-api-key")) != "" || strings.TrimSpace(c.GetHeader("x-api-key")) != "" {
		return true
	}
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	parts := strings.SplitN(auth, " ", 2)
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && strings.TrimSpace(parts[1]) != ""
}

// extractAPIKeyForGoogle extracts API key for Google/Gemini endpoints.
// Priority: x-goog-api-key > Authorization: Bearer > x-api-key > query key
// This allows OpenClaw and other clients using Bearer auth to work with Gemini endpoints.
func extractAPIKeyForGoogle(c *gin.Context) string {
	// 1) preferred: Gemini native header
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k
	}

	// 2) fallback: Authorization: Bearer <key>
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}

	// 3) x-api-key header (backward compatibility)
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}

	// 4) query parameter key (for specific paths)
	if allowGoogleQueryKey(c.Request.URL.Path) {
		if v := strings.TrimSpace(c.Query("key")); v != "" {
			return v
		}
	}

	return ""
}

func allowGoogleQueryKey(path string) bool {
	return strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta")
}

func abortWithGoogleError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  googleapi.HTTPStatusToGoogleStatus(status),
		},
	})
	c.Abort()
}
