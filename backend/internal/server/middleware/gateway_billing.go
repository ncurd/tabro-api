package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// The coordinator is attached once during dependency construction, before any
// requests. Both Anthropic/OpenAI and Google authentication use the same route.
func ProvideGatewayAuthMiddleware(keys *service.APIKeyService, subscriptions *service.SubscriptionService, auth *service.AuthService, cfg *config.Config, billing *service.GatewayBillingCoordinator, models *service.AdminModelPolicyService) APIKeyAuthMiddleware {
	keys.GatewayBilling = billing
	keys.ModelPolicies = models
	return NewGatewayAuthMiddleware(keys, subscriptions, auth, cfg)
}

func gatewayBillingError(err error) (int, string, string) {
	if message, tooLarge := gatewayRequestBodyLimitError(err); tooLarge {
		return http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", message
	}
	var local *service.BillingRequestError
	if errors.As(err, &local) {
		return local.HTTPStatus(), string(local.Code), local.Message
	}
	if errors.Is(err, service.ErrUnsupportedGPT61SolReasoning) {
		return 400, "INVALID_MODEL_REASONING", "GPT-6.1 Sol does not support this reasoning effort"
	}
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
	return 503, "BILLING_CENTER_UNAVAILABLE", "Billing authorization is unavailable"
}

// Only identifier-shaped model names belong in admission logs. Never log the
// request body, credentials, proof, or arbitrary error strings here.
func gatewayBillingLogModel(body []byte) string {
	value := gjson.GetBytes(body, "model")
	if value.Type != gjson.String || len(value.Str) == 0 || len(value.Str) > 200 {
		return ""
	}
	for _, r := range value.Str {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._/:@", r) {
			continue
		}
		return ""
	}
	return value.Str
}

func logGatewayBillingFailure(c *gin.Context, stage, model string, err error) {
	status, code, _ := gatewayBillingError(err)
	fields := []zap.Field{
		zap.String("stage", stage), zap.String("method", c.Request.Method),
		zap.String("path", c.Request.URL.Path), zap.String("model", model),
		zap.String("error_code", code), zap.Int("http_status", status),
		zap.String("error_type", fmt.Sprintf("%T", err)),
	}
	var local *service.BillingRequestError
	if errors.As(err, &local) {
		fields = append(fields, zap.String("reason", local.Message))
	}
	var remote *bc.RemoteError
	if errors.As(err, &remote) {
		fields = append(fields, zap.Int("authority_status", remote.Status),
			zap.Bool("retryable", remote.Retryable), zap.Bool("unknown_outcome", remote.UnknownOutcome))
	}
	logger.FromContext(c.Request.Context()).Warn("gateway.billing.prepare_failed", fields...)
}

func prepareGatewayBilling(c *gin.Context, keys *service.APIKeyService, key *service.APIKey, principal *service.GatewayOIDCPrincipal, credential string) (finish func(), err error) {
	noop := func() {}
	stage, model := "connector", ""
	defer func() {
		if err != nil {
			logGatewayBillingFailure(c, stage, model, err)
		}
	}()
	if principal != nil && principal.SubjectProof != "" {
		credential = principal.SubjectProof
	}
	if keys.GatewayBilling == nil {
		if principal != nil && principal.AuthBilled {
			return noop, bc.ErrState
		}
		return noop, nil
	}
	var binding *service.GatewayBillingCredential
	if principal == nil {
		stage = "credential_lookup"
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
	stage = "route_lookup"
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
	stage = "request_validation"
	if c.Request.Method != "POST" || c.Request.Body == nil {
		return noop, service.NewBillingRequestError(service.BillingRequestUnsupported, "Central billing requires a supported POST request with a body")
	}
	stage = "request_body"
	body, err := readGatewayRequestBody(c)
	if err != nil {
		return noop, err
	}
	model = gatewayBillingLogModel(body)
	if route.Mode == "central" {
		stage = "gemini_bounds"
		body, err = keys.GatewayBilling.BoundGeminiRequest(c.Request.URL.Path, body)
		if err != nil {
			return noop, err
		}
		stage = "text_bounds"
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
	stage = "admission"
	if service.IsBillingImagesPath(c.Request.URL.Path) {
		execution, err = keys.GatewayBilling.PrepareImagesForKey(c.Request.Context(), *route, principal, credential, c.Request.URL.Path, operationID, body, c.GetHeader("Content-Type"), key, binding)
	} else {
		execution, err = keys.GatewayBilling.PrepareForKey(c.Request.Context(), *route, principal, credential, c.Request.URL.Path, operationID, body, key, binding)
	}
	if err != nil {
		if route.Mode == "shadow" {
			if auditErr := keys.GatewayBilling.ShadowFailure(c.Request.Context(), *route, operationID, service.HashUsageRequestPayload(body), "quote_failed"); auditErr != nil {
				logGatewayBillingFailure(c, "shadow_audit", model, auditErr)
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
			logGatewayBillingFailure(c, "reconciliation", model, err)
		}
	}, nil
}
