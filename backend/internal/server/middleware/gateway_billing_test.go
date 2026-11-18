package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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
	core, logs := observer.New(zap.WarnLevel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
	keys := &service.APIKeyService{}
	key := &service.APIKey{ID: 1, UserID: 2}
	principal := &service.GatewayOIDCPrincipal{Issuer: "https://auth.example", Subject: "user-1", AuthBilled: true}

	_, err := prepareGatewayBilling(c, keys, key, principal, "signed-access-token")
	require.ErrorIs(t, err, bc.ErrState)
	require.Len(t, logs.All(), 1, "the admission failure must be diagnosable before an upstream handler runs")
	require.Equal(t, "connector", logs.All()[0].ContextMap()["stage"])
	require.Equal(t, "BILLING_CENTER_UNAVAILABLE", logs.All()[0].ContextMap()["error_code"])
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

func TestGatewayBillingLocalErrorsRetainActionableClientStatusAndSafeMessage(t *testing.T) {
	cause := errors.New("private-upstream-body secret-proof secret-api-key")
	for _, tc := range []struct {
		code   service.BillingRequestErrorCode
		status int
	}{
		{service.BillingRequestInvalid, http.StatusBadRequest},
		{service.BillingRequestUnsupported, http.StatusBadRequest},
		{service.BillingModelCapabilitiesUnavailable, http.StatusUnprocessableEntity},
		{service.BillingModelPricingNotConfigured, http.StatusUnprocessableEntity},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			local := service.NewBillingRequestError(tc.code, "Safe local admission reason", cause)
			err := fmt.Errorf("outer context: %w", local)
			status, code, message := gatewayBillingError(err)
			require.Equal(t, tc.status, status)
			require.Equal(t, string(tc.code), code)
			require.Equal(t, "Safe local admission reason", message)
			require.ErrorIs(t, err, cause)
			require.NotContains(t, local.Error(), "secret-")
		})
	}
	// A local error wrapping a historical sentinel must still be classified
	// by its explicit reason, rather than by the generic state/conflict code.
	local := service.NewBillingRequestError(service.BillingModelPricingNotConfigured, "Configure a model price", bc.ErrState)
	status, code, _ := gatewayBillingError(local)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "MODEL_PRICING_NOT_CONFIGURED", code)
}

func TestGatewayBillingAuthorityAndUnknownFailuresRemainUnavailable(t *testing.T) {
	for _, err := range []error{
		&bc.RemoteError{Retryable: true},
		&bc.RemoteError{Status: http.StatusUnauthorized, Retryable: true},
		&bc.RemoteError{Status: http.StatusRequestTimeout, Retryable: true, UnknownOutcome: true},
		&bc.RemoteError{Status: http.StatusTooManyRequests, Retryable: true},
		&bc.RemoteError{Status: http.StatusServiceUnavailable, Retryable: true},
		bc.ErrState,
		errors.New("sensitive provider response and secret-proof"),
	} {
		status, code, message := gatewayBillingError(err)
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.Equal(t, "BILLING_CENTER_UNAVAILABLE", code)
		require.NotContains(t, message, "secret-proof")
	}
}

func TestGatewayBillingTextValidationDoesNotMasqueradeAsAuthorityOutage(t *testing.T) {
	coordinator := &service.GatewayBillingCoordinator{}
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"malformed JSON", `{`, "BILLING_REQUEST_INVALID", http.StatusBadRequest},
		{"missing model", `{}`, "BILLING_REQUEST_INVALID", http.StatusBadRequest},
		{"unknown capability", `{"model":"unregistered-model-alias","max_output_tokens":5}`, "MODEL_CAPABILITIES_UNAVAILABLE", http.StatusUnprocessableEntity},
		{"excessive output", `{"model":"gpt-6.2-astra","max_output_tokens":128001}`, "BILLING_REQUEST_INVALID", http.StatusBadRequest},
		{"conflicting output", `{"model":"gpt-6.2-astra","max_output_tokens":5,"max_tokens":6}`, "BILLING_REQUEST_INVALID", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := coordinator.BoundRequest("/v1/responses", []byte(tc.body))
			require.Error(t, err)
			status, code, _ := gatewayBillingError(err)
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.code, code)
		})
	}
}

func TestGatewayBillingFailureLogsOnlySafeDiagnostics(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"qwen4-max","input":"private-request-payload","api_key":"secret-api-key"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses?credential=secret-url-credential", strings.NewReader(body))
	c.Request.Header.Set("Authorization", "Bearer secret-api-key")
	c.Request.Header.Set("X-Subject-Proof", "secret-proof")
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core).With(zap.String("request_id", "request-safe-id"))))
	cause := errors.New("private-upstream-body secret-proof secret-api-key")
	for _, err := range []error{
		service.NewBillingRequestError(service.BillingModelPricingNotConfigured, "Configure a price for this model", cause),
		fmt.Errorf("secret-proof private-upstream-body: %w", &bc.RemoteError{Status: http.StatusServiceUnavailable, Retryable: true, UnknownOutcome: true}),
		cause,
	} {
		logGatewayBillingFailure(c, "admission", gatewayBillingLogModel([]byte(body)), err)
	}
	require.Len(t, logs.All(), 3)
	for _, entry := range logs.All() {
		require.Equal(t, "gateway.billing.prepare_failed", entry.Message)
		fields := entry.ContextMap()
		require.Equal(t, "admission", fields["stage"])
		require.Equal(t, "/v1/responses", fields["path"])
		require.Equal(t, "qwen4-max", fields["model"])
		require.Equal(t, "request-safe-id", fields["request_id"])
		encoded, err := json.Marshal(fields)
		require.NoError(t, err)
		for _, sensitive := range []string{"private-request-payload", "private-upstream-body", "secret-api-key", "secret-proof", "secret-url-credential"} {
			require.NotContains(t, string(encoded), sensitive)
		}
	}
	require.Equal(t, "Configure a price for this model", logs.All()[0].ContextMap()["reason"])
	require.EqualValues(t, http.StatusServiceUnavailable, logs.All()[1].ContextMap()["authority_status"])
	require.Equal(t, true, logs.All()[1].ContextMap()["unknown_outcome"])
	require.NotContains(t, logs.All()[2].ContextMap(), "reason", "arbitrary error strings are not safe diagnostic fields")
	require.Empty(t, gatewayBillingLogModel([]byte(`{"model":"private payload\nwith spaces"}`)))
	require.Empty(t, gatewayBillingLogModel([]byte(`{"model":"`+strings.Repeat("x", 201)+`"}`)))
}
