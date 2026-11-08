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
	"net/http/httptest"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayBodyBillingRepository struct {
	service.GatewayBillingRepository
	audits      int
	payloadHash string
}

func (*gatewayBodyBillingRepository) GetBillingRoute(context.Context, int64) (*service.GatewayBillingRoute, error) {
	return &service.GatewayBillingRoute{LocalUserID: 2, Mode: "shadow"}, nil
}

func (*gatewayBodyBillingRepository) GetBillingWorkspaceRoute(context.Context, string, string) (*service.GatewayBillingRoute, error) {
	return nil, nil
}

func (r *gatewayBodyBillingRepository) RecordBillingShadowFailure(_ context.Context, _ service.GatewayBillingRoute, _ bc.Key, payloadHash, _ string) error {
	r.audits++
	r.payloadHash = payloadHash
	return nil
}

func gatewayBodyBillingFixture(t *testing.T, keys *service.APIKeyService) (*gatewayBodyBillingRepository, *service.GatewayOIDCPrincipal) {
	t.Helper()
	repo := &gatewayBodyBillingRepository{}
	// A disabled shadow connector records the full payload hash without making
	// network calls or reserving money; this still exercises billing's body read.
	coordinator, err := service.ProvideGatewayBillingCoordinator(&config.Config{}, repo, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(coordinator.Stop)
	keys.GatewayBilling = coordinator
	return repo, &service.GatewayOIDCPrincipal{Issuer: "https://auth.example", Subject: "user", ClientID: "test"}
}

func TestGatewayRoutingAndBillingAcceptBodiesAbove32MiBWithinConfiguredLimit(t *testing.T) {
	body := bytes.Repeat([]byte(" "), 32*1024*1024+512)
	// The model at the end proves routing reads the entire body.
	copy(body[len(body)-len(`{"model":"gpt-6-sol"}`):], `{"model":"gpt-6-sol"}`)
	expectedHash := service.HashUsageRequestPayload(body)
	keys, key := requestRoutingFixture()
	repo, principal := gatewayBodyBillingFixture(t, keys)
	router := gin.New()
	router.Use(RequestBodyLimit(int64(len(body))))
	router.POST("/v1/responses", func(c *gin.Context) {
		resolved, err := resolveRequestGatewayGroup(c, keys, key)
		require.NoError(t, err)
		require.Equal(t, service.PlatformOpenAI, resolved.Group.Platform)
		finish, err := prepareGatewayBilling(c, keys, resolved, principal, "test-proof")
		require.NoError(t, err)
		defer finish()
		hash := sha256.New()
		size, err := io.Copy(hash, c.Request.Body)
		require.NoError(t, err)
		require.EqualValues(t, len(body), size)
		require.Equal(t, expectedHash, hex.EncodeToString(hash.Sum(nil)))
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, 1, repo.audits)
	require.Equal(t, expectedHash, repo.payloadHash, "billing must inspect the complete request")
}

func TestGatewayBodyLimitFailsClosedBeforeRoutingOrBilling(t *testing.T) {
	for _, stage := range []string{"routing", "billing"} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%t", stage, chunked), func(t *testing.T) {
				keys, key := requestRoutingFixture()
				repo, principal := gatewayBodyBillingFixture(t, keys)
				router := gin.New()
				router.Use(RequestBodyLimit(64))
				router.POST("/v1/responses", func(c *gin.Context) {
					original := c.Request.Body
					var err error
					var status int
					var code, message string
					if stage == "routing" {
						_, err = resolveRequestGatewayGroup(c, keys, key)
						status, code, message = gatewayGroupRoutingError(err)
					} else {
						_, err = prepareGatewayBilling(c, keys, key, principal, "test-proof")
						status, code, message = gatewayBillingError(err)
					}
					var maxErr *http.MaxBytesError
					require.ErrorAs(t, err, &maxErr)
					require.EqualValues(t, 64, maxErr.Limit)
					require.Equal(t, http.StatusRequestEntityTooLarge, status)
					require.Equal(t, "REQUEST_BODY_TOO_LARGE", code)
					require.Same(t, original, c.Request.Body, "never restore a truncated prefix")
					_, readErr := io.ReadAll(c.Request.Body)
					require.ErrorAs(t, readErr, &maxErr, "the configured limit must remain enforced")
					AbortWithError(c, status, code, message)
				}, func(c *gin.Context) {
					t.Error("oversized requests must not reach an upstream handler")
				})
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 65)))
				if chunked {
					request.ContentLength = -1
					request.TransferEncoding = []string{"chunked"}
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
				require.Contains(t, recorder.Body.String(), "maximum 64 bytes")
				require.Zero(t, repo.audits, "oversized requests must fail before quoting or shadow auditing")
			})
		}
	}
}

func TestGatewayRequestBodyReadFailurePreservesOriginalReader(t *testing.T) {
	readErr := errors.New("request transport failed")
	original := io.NopCloser(io.MultiReader(strings.NewReader("prefix"), gatewayBodyErrorReader{err: readErr}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Body = original
	body, err := readGatewayRequestBody(c)
	require.ErrorIs(t, err, readErr)
	require.Nil(t, body)
	require.Equal(t, original, c.Request.Body)
}

type gatewayBodyErrorReader struct{ err error }

func (r gatewayBodyErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestGatewayInvocationIdempotencyBodyLimitDoesNotReserve(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	repo := newGatewayIdempotencyRepoStub()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	for _, chunked := range []bool{false, true} {
		for _, protocol := range []string{"anthropic", "google"} {
			t.Run(fmt.Sprintf("%s/chunked=%t", protocol, chunked), func(t *testing.T) {
				writer := GatewayErrorWriter(AnthropicErrorWriter)
				if protocol == "google" {
					writer = GoogleErrorWriter
				}
				router := gin.New()
				router.Use(RequestBodyLimit(16))
				router.Use(func(c *gin.Context) {
					c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 77})
					c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.GatewayBillingRequestID, "body-limit-test"))
					c.Next()
				})
				router.Use(GatewayInvocationIdempotency(writer))
				router.POST("/v1/responses", func(c *gin.Context) {
					t.Error("oversized idempotent requests must not reach an upstream handler")
				})
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 17)))
				request.Header.Set("Idempotency-Key", "body-limit-test")
				if chunked {
					request.ContentLength = -1
					request.TransferEncoding = []string{"chunked"}
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
				require.Contains(t, recorder.Body.String(), "maximum 16 bytes")
				require.Empty(t, repo.records, "reading must succeed before idempotency reserves a key")
			})
		}
	}
}
