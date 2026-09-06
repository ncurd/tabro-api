package middleware

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type gatewayInvocationFingerprint struct {
	BodySHA256  string `json:"body_sha256"`
	QuerySHA256 string `json:"query_sha256,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type gatewayInvocationOutcome struct {
	Status int `json:"status"`
}

// GatewayInvocationIdempotency reserves an Idempotency-Key before a model
// invocation reaches an upstream provider. Billing deduplication remains a
// second line of defence; it must never be the first place a reused key is
// noticed, otherwise a caller could execute multiple upstream requests while
// paying for only the first one.
//
// Responses may be streamed and arbitrarily large, so this guard deliberately
// does not persist/replay response bodies. A completed duplicate receives 409
// with X-Idempotency-Replayed=true and never reaches the provider.
func GatewayInvocationIdempotency(writeError GatewayErrorWriter) gin.HandlerFunc {
	if writeError == nil {
		writeError = AnthropicErrorWriter
	}
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey == "" {
			c.Next()
			return
		}

		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil {
			writeError(c, http.StatusInternalServerError, "Authenticated billing identity is unavailable")
			c.Abort()
			return
		}
		coordinator := service.DefaultIdempotencyCoordinator()
		if coordinator == nil {
			// Fail closed only for requests that opt into idempotency. Letting the
			// request through would make the later billing request-id dedup unsafe.
			writeError(c, http.StatusServiceUnavailable, "Idempotency store is unavailable")
			c.Abort()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			writeError(c, http.StatusBadRequest, "Failed to read request body")
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		bodyHash := sha256.Sum256(body)
		query := c.Request.URL.Query()
		// Authentication material is not part of the logical request and must
		// not be copied into persistence, even indirectly as a payload field.
		query.Del("key")
		query.Del("api_key")
		queryHash := ""
		if encoded := query.Encode(); encoded != "" {
			sum := sha256.Sum256([]byte(encoded))
			queryHash = hex.EncodeToString(sum[:])
		}
		path := c.Request.URL.EscapedPath()
		if path == "" {
			path = "/"
		}
		actorScope := "api-key:" + strconv.FormatInt(apiKey.ID, 10)
		result, execErr := coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
			Scope:          "gateway.invoke." + actorScope,
			ActorScope:     actorScope,
			Method:         c.Request.Method,
			Route:          path,
			IdempotencyKey: idempotencyKey,
			Payload: gatewayInvocationFingerprint{
				BodySHA256:  hex.EncodeToString(bodyHash[:]),
				QuerySHA256: queryHash,
				ContentType: strings.TrimSpace(c.GetHeader("Content-Type")),
			},
			TTL: service.DefaultWriteIdempotencyTTL(),
		}, func(execCtx context.Context) (any, error) {
			// The billing request ID must identify this reservation generation,
			// not the raw Idempotency-Key forever. When an invocation record
			// legitimately expires and is reclaimed, a new generation prevents
			// the permanent billing archive from suppressing the new charge.
			billingBase, _ := execCtx.Value(ctxkey.GatewayBillingRequestID).(string)
			if strings.TrimSpace(billingBase) == "" {
				return nil, service.ErrIdempotencyStoreUnavail
			}
			generation := make([]byte, 16)
			if _, err := rand.Read(generation); err != nil {
				return nil, service.ErrIdempotencyStoreUnavail
			}
			billingSum := sha256.Sum256([]byte("gateway-invocation\x00" + billingBase + "\x00" + hex.EncodeToString(generation)))
			execCtx = context.WithValue(execCtx, ctxkey.GatewayBillingRequestID, hex.EncodeToString(billingSum[:]))
			c.Request = c.Request.WithContext(execCtx)
			c.Next()
			return gatewayInvocationOutcome{Status: c.Writer.Status()}, nil
		})
		if execErr != nil {
			if retryAfter := service.RetryAfterSecondsFromError(execErr); retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			if c.Writer.Written() {
				c.Abort()
				return
			}
			status := http.StatusInternalServerError
			message := "Failed to enforce request idempotency"
			switch infraerrors.Reason(execErr) {
			case infraerrors.Reason(service.ErrIdempotencyKeyInvalid):
				status = http.StatusBadRequest
				message = "Idempotency-Key is invalid"
			case infraerrors.Reason(service.ErrIdempotencyKeyConflict):
				status = http.StatusConflict
				message = "Idempotency-Key was already used with a different request"
			case infraerrors.Reason(service.ErrIdempotencyInProgress):
				status = http.StatusConflict
				message = "Idempotent request is still processing"
			case infraerrors.Reason(service.ErrIdempotencyRetryBackoff):
				status = http.StatusConflict
				message = "Idempotent request is in retry backoff"
			case infraerrors.Reason(service.ErrIdempotencyStoreUnavail):
				status = http.StatusServiceUnavailable
				message = "Idempotency store is unavailable"
			}
			writeError(c, status, message)
			c.Abort()
			return
		}
		if result != nil && result.Replayed {
			c.Header("X-Idempotency-Replayed", "true")
			writeError(c, http.StatusConflict, "Idempotent request already completed; the original streaming response is not replayable")
			c.Abort()
			return
		}
	}
}
