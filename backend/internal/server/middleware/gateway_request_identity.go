package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var (
	errInvalidGatewayAuthorization   = errors.New("invalid gateway authorization header")
	errConflictingGatewayCredentials = errors.New("conflicting gateway credentials")
)

type gatewayCredentialInput struct {
	value             string
	fromAuthorization bool
}

func extractGatewayCredentialInput(c *gin.Context, allowQueryKey bool) (gatewayCredentialInput, error) {
	var result gatewayCredentialInput
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if authorization != "" {
		parts := strings.SplitN(authorization, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			return result, errInvalidGatewayAuthorization
		}
		result.value = strings.TrimSpace(parts[1])
		result.fromAuthorization = true
	}

	otherValues := []string{
		strings.TrimSpace(c.GetHeader("x-goog-api-key")),
		strings.TrimSpace(c.GetHeader("x-api-key")),
	}
	if allowQueryKey {
		otherValues = append(otherValues, strings.TrimSpace(c.Query("key")))
	}
	for _, value := range otherValues {
		if value == "" {
			continue
		}
		if result.fromAuthorization && looksLikeGatewayJWT(result.value) {
			return gatewayCredentialInput{}, errConflictingGatewayCredentials
		}
		if result.value != "" && result.value != value {
			return gatewayCredentialInput{}, errConflictingGatewayCredentials
		}
		if result.value == "" {
			result.value = value
			result.fromAuthorization = false
		}
	}
	return result, nil
}

// attachGatewayRequestIdentity stores only verified claims and log-safe
// correlation values. The raw bearer token and raw idempotency key never enter
// request context or structured logs.
func attachGatewayRequestIdentity(c *gin.Context, apiKey *service.APIKey, principal *service.GatewayOIDCPrincipal) error {
	if c == nil || c.Request == nil || apiKey == nil {
		return nil
	}
	ctx := c.Request.Context()
	if principal != nil {
		ctx = context.WithValue(ctx, ctxkey.OIDCIssuer, principal.Issuer)
		ctx = context.WithValue(ctx, ctxkey.OIDCSubject, principal.Subject)
		ctx = context.WithValue(ctx, ctxkey.OIDCTenant, principal.Tenant)
		ctx = context.WithValue(ctx, ctxkey.OIDCExpiresAt, principal.ExpiresAt)
	}

	idempotencyKey, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		return err
	}
	if idempotencyKey != "" {
		sum := sha256.Sum256([]byte("gateway\x00" + strconv.FormatInt(apiKey.ID, 10) + "\x00" + idempotencyKey))
		ctx = context.WithValue(ctx, ctxkey.GatewayBillingRequestID, hex.EncodeToString(sum[:]))
	}
	if runID := safeGatewayCorrelationValue(c.GetHeader("X-Tabro-Run-Id")); runID != "" {
		ctx = context.WithValue(ctx, ctxkey.TabroRunID, runID)
	}
	if projectID := safeGatewayCorrelationValue(c.GetHeader("X-Tabro-Project-Id")); projectID != "" {
		ctx = context.WithValue(ctx, ctxkey.TabroProjectID, projectID)
	}
	c.Request = c.Request.WithContext(ctx)
	return nil
}

func safeGatewayCorrelationValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r < 32 || r > 126 {
			return ""
		}
	}
	return value
}
