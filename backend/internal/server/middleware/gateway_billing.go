package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// The coordinator is attached once during dependency construction, before any
// requests. Both Anthropic/OpenAI and Google authentication use the same route.
func ProvideGatewayAuthMiddleware(keys *service.APIKeyService, subscriptions *service.SubscriptionService, auth *service.AuthService, cfg *config.Config, billing *service.GatewayBillingCoordinator) APIKeyAuthMiddleware {
	keys.GatewayBilling = billing
	return NewGatewayAuthMiddleware(keys, subscriptions, auth, cfg)
}

func gatewayBillingError(err error) (int, string, string) {
	var remote *bc.RemoteError
	if errors.As(err, &remote) {
		switch remote.Status {
		case 400:
			return 400, "BILLING_REQUEST_INVALID", "Billing request is invalid"
		case 404:
			return 403, "BILLING_ACCOUNT_NOT_READY", "No billable Auth account or product is available for this request"
		case 402:
			return 402, "BILLING_LIMIT_EXCEEDED", "Insufficient funds or billing budget exceeded"
		case 403:
			return 403, "BILLING_FORBIDDEN", "Billing authority denied this account or credential"
		case 409:
			return 409, "BILLING_CONFLICT", "Billing operation conflicts with its current state"
		}
	}
	if errors.Is(err, bc.ErrConflict) {
		return 409, "BILLING_CONFLICT", "Billing identity or operation conflicts with its frozen state"
	}
	return 503, "BILLING_CENTER_UNAVAILABLE", "Billing authorization is unavailable or this request is not supported"
}

func prepareGatewayBilling(c *gin.Context, keys *service.APIKeyService, key *service.APIKey, principal *service.GatewayOIDCPrincipal, credential string) (func(), error) {
	noop := func() {}
	if keys.GatewayBilling == nil {
		if principal != nil && principal.AuthBilled {
			return noop, bc.ErrState
		}
		return noop, nil
	}
	var binding *service.GatewayBillingCredential
	if principal == nil {
		var err error
		binding, err = keys.GatewayBilling.Credential(c.Request.Context(), key.ID, key.UserID)
		if err != nil {
			return noop, err
		}
		if binding != nil {
			principal = binding.Principal()
			credential = ""
		}
	}
	route, err := keys.GatewayBilling.RouteForPrincipal(c.Request.Context(), key.UserID, principal)
	if err != nil {
		return noop, err
	}
	if route == nil || route.Mode == "local" {
		if principal != nil && principal.AuthBilled {
			return noop, bc.ErrState
		}
		return noop, nil
	}
	if c.Request.Method == "GET" && strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		ctx := service.WithGatewayBillingSession(c.Request.Context(), keys.GatewayBilling.Session(*route, principal, credential, binding, key))
		if route.Mode == "central" {
			ctx = bc.WithExecution(ctx, &bc.Execution{Mode: "central"})
		}
		c.Request = c.Request.WithContext(ctx)
		return noop, nil
	}
	// Read-only catalogue/usage endpoints never reserve. WebSocket upgrades and
	// all writes still pass Prepare and must be explicitly supported.
	unbilledResourceDelete := c.Request.Method == "DELETE" && strings.HasPrefix(strings.TrimPrefix(c.Request.URL.Path, "/v1"), "/audio/voices/")
	unbilledTokenCount := c.Request.Method == "POST" && strings.HasSuffix(c.Request.URL.Path, "/messages/count_tokens")
	if (c.Request.Method == "GET" && !strings.EqualFold(c.GetHeader("Upgrade"), "websocket")) || unbilledResourceDelete || unbilledTokenCount {
		if route.Mode == "central" {
			c.Request = c.Request.WithContext(bc.WithExecution(c.Request.Context(), &bc.Execution{Mode: "central"}))
		}
		return noop, nil
	}
	if c.Request.Method != "POST" || c.Request.Body == nil {
		return noop, bc.ErrState
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 32*1024*1024+1))
	if err != nil {
		return noop, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > 32*1024*1024 {
		return noop, bc.ErrConflict
	}
	if route.Mode == "central" {
		body, err = keys.GatewayBilling.BoundGeminiRequest(c.Request.URL.Path, body)
		if err != nil {
			return noop, err
		}
		body, err = keys.GatewayBilling.BoundRequest(c.Request.URL.Path, body)
		if err != nil {
			return noop, err
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
	}
	operationID, _ := c.Request.Context().Value(ctxkey.GatewayBillingRequestID).(string)
	if route.Mode == "shadow" {
		operationID = uuid.NewString()
	} else if idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key")); idempotencyKey != "" {
		sum := sha256.Sum256([]byte("central\x00" + route.Issuer + "\x00" + route.ActorUserID + "\x00" + route.TenantID + "\x00" + route.OriginAppID + "\x00" + idempotencyKey))
		operationID = hex.EncodeToString(sum[:])
	} else {
		operationID = uuid.NewString()
	}
	var execution *bc.Execution
	if service.IsBillingImagesPath(c.Request.URL.Path) {
		execution, err = keys.GatewayBilling.PrepareImagesForKey(c.Request.Context(), *route, principal, credential, c.Request.URL.Path, operationID, body, c.GetHeader("Content-Type"), key, binding)
	} else {
		execution, err = keys.GatewayBilling.PrepareForKey(c.Request.Context(), *route, principal, credential, c.Request.URL.Path, operationID, body, key, binding)
	}
	if err != nil {
		if route.Mode == "shadow" {
			if auditErr := keys.GatewayBilling.ShadowFailure(c.Request.Context(), *route, operationID, service.HashUsageRequestPayload(body), "quote_failed"); auditErr != nil {
				logger.LegacyPrintf("billing_center", "shadow failure audit unavailable: %v", auditErr)
			}
			return noop, nil
		}
		return noop, err
	}
	ctx := bc.WithExecution(c.Request.Context(), execution)
	ctx = context.WithValue(ctx, ctxkey.GatewayBillingRequestID, operationID)
	c.Request = c.Request.WithContext(ctx)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := keys.GatewayBilling.Finish(ctx, execution); err != nil {
			logger.LegacyPrintf("billing_center", "request reconciliation failed: %v", err)
		}
	}, nil
}
