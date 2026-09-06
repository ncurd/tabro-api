package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayIdempotencyRepoStub struct {
	mu      sync.Mutex
	nextID  int64
	records map[string]*service.IdempotencyRecord
}

func newGatewayIdempotencyRepoStub() *gatewayIdempotencyRepoStub {
	return &gatewayIdempotencyRepoStub{records: make(map[string]*service.IdempotencyRecord)}
}

func (r *gatewayIdempotencyRepoStub) recordKey(scope, hash string) string {
	return scope + "\x00" + hash
}

func cloneGatewayIdempotencyRecord(record *service.IdempotencyRecord) *service.IdempotencyRecord {
	if record == nil {
		return nil
	}
	clone := *record
	return &clone
}

func (r *gatewayIdempotencyRepoStub) CreateProcessing(_ context.Context, record *service.IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.recordKey(record.Scope, record.IdempotencyKeyHash)
	if _, exists := r.records[key]; exists {
		return false, nil
	}
	r.nextID++
	record.ID = r.nextID
	r.records[key] = cloneGatewayIdempotencyRecord(record)
	return true, nil
}

func (r *gatewayIdempotencyRepoStub) GetByScopeAndKeyHash(_ context.Context, scope, hash string) (*service.IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneGatewayIdempotencyRecord(r.records[r.recordKey(scope, hash)]), nil
}

func (r *gatewayIdempotencyRepoStub) TryReclaim(_ context.Context, id int64, fromStatus string, now, lockedUntil, expiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id && record.Status == fromStatus {
			record.Status = service.IdempotencyStatusProcessing
			record.LockedUntil = &lockedUntil
			record.ExpiresAt = expiresAt
			return true, nil
		}
	}
	return false, nil
}

func (r *gatewayIdempotencyRepoStub) ExtendProcessingLock(_ context.Context, id int64, fingerprint string, lockedUntil, expiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id && record.Status == service.IdempotencyStatusProcessing && record.RequestFingerprint == fingerprint {
			record.LockedUntil = &lockedUntil
			record.ExpiresAt = expiresAt
			return true, nil
		}
	}
	return false, nil
}

func (r *gatewayIdempotencyRepoStub) MarkSucceeded(_ context.Context, id int64, status int, body string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id {
			record.Status = service.IdempotencyStatusSucceeded
			record.ResponseStatus = &status
			record.ResponseBody = &body
			record.ExpiresAt = expiresAt
		}
	}
	return nil
}

func (r *gatewayIdempotencyRepoStub) MarkFailedRetryable(_ context.Context, id int64, reason string, lockedUntil, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.ID == id {
			record.Status = service.IdempotencyStatusFailedRetryable
			record.ErrorReason = &reason
			record.LockedUntil = &lockedUntil
			record.ExpiresAt = expiresAt
		}
	}
	return nil
}

func (r *gatewayIdempotencyRepoStub) DeleteExpired(_ context.Context, _ time.Time, _ int) (int64, error) {
	return 0, nil
}

func (r *gatewayIdempotencyRepoStub) expireAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		record.ExpiresAt = time.Now().Add(-time.Minute)
	}
}

func TestGatewayInvocationIdempotencyBlocksReplayAndPayloadReuseBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := service.DefaultIdempotencyCoordinator()
	repo := newGatewayIdempotencyRepoStub()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	var calls atomic.Int32
	var billingIDs []string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 77})
		ctx := context.WithValue(c.Request.Context(), ctxkey.GatewayBillingRequestID, "stable-key-derived-base")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(GatewayInvocationIdempotency(AnthropicErrorWriter))
	router.POST("/v1/messages", func(c *gin.Context) {
		calls.Add(1)
		billingID, _ := c.Request.Context().Value(ctxkey.GatewayBillingRequestID).(string)
		billingIDs = append(billingIDs, billingID)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	invoke := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "run-1:node-2:call-3")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := invoke(`{"model":"claude-fable-5-1","messages":[]}`)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, int32(1), calls.Load())

	replay := invoke(`{"model":"claude-fable-5-1","messages":[]}`)
	require.Equal(t, http.StatusConflict, replay.Code)
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), calls.Load(), "a completed replay must not reach the upstream handler")

	conflict := invoke(`{"model":"claude-opus-5","messages":[]}`)
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Empty(t, conflict.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), calls.Load(), "same key with a different payload must fail before the upstream handler")

	repo.expireAll()
	reclaimed := invoke(`{"model":"claude-fable-5-1","messages":[]}`)
	require.Equal(t, http.StatusOK, reclaimed.Code)
	require.Equal(t, int32(2), calls.Load())
	require.Len(t, billingIDs, 2)
	require.NotEmpty(t, billingIDs[0])
	require.NotEqual(t, billingIDs[0], billingIDs[1], "a reclaimed invocation must receive a new billing generation")
}

func TestGatewayInvocationIdempotencyFailsClosedWhenStoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	var called bool
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 88})
		ctx := context.WithValue(c.Request.Context(), ctxkey.GatewayBillingRequestID, "stable-key-derived-base")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(GatewayInvocationIdempotency(GoogleErrorWriter))
	router.POST("/v1beta/models/*action", func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", strings.NewReader(`{"contents":[]}`))
	req.Header.Set("Idempotency-Key", "run-2:node-4:call-6")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.False(t, called)
}
