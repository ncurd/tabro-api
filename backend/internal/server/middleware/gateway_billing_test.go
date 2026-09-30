package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayBillingCentralKeepsPermanentOperationIdentity(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), ctxkey.GatewayBillingRequestID, "stable-central-operation")
		ctx = bc.WithExecution(ctx, &bc.Execution{Mode: "central"})
		c.Request = c.Request.WithContext(ctx)
	})
	router.Use(GatewayInvocationIdempotency(AnthropicErrorWriter))
	called := false
	router.POST("/v1/responses", func(c *gin.Context) {
		called = true
		require.Equal(t, "stable-central-operation", c.Request.Context().Value(ctxkey.GatewayBillingRequestID))
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Idempotency-Key", "stable-client-operation")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.True(t, called)
}

func TestAuthBilledGatewayRequestNeverFallsBackWithoutBillingCenter(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	keys := &service.APIKeyService{}
	key := &service.APIKey{ID: 1, UserID: 2}
	principal := &service.GatewayOIDCPrincipal{Issuer: "https://auth.example", Subject: "user-1", AuthBilled: true}

	_, err := prepareGatewayBilling(c, keys, key, principal, "signed-access-token")
	require.ErrorIs(t, err, bc.ErrState)
}

func TestGatewayBillingMissingAuthAccountIsNotReportedAsOutage(t *testing.T) {
	status, code, _ := gatewayBillingError(&bc.RemoteError{Status: http.StatusNotFound})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "BILLING_ACCOUNT_NOT_READY", code)
}

func TestGatewayBillingUnsupportedGPT61SolReasoningIsClientError(t *testing.T) {
	status, code, _ := gatewayBillingError(service.ErrUnsupportedGPT61SolReasoning)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "INVALID_MODEL_REASONING", code)
}
