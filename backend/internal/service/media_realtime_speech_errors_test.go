package service

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	ws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRealtimeSpeechDialFailurePreservesStatusWithoutProviderContent(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{200, "speech_provider_endpoint_invalid"},
		{400, "speech_provider_endpoint_invalid"},
		{401, "speech_provider_authentication_failed"},
		{403, "speech_provider_access_denied"},
		{404, "speech_provider_endpoint_invalid"},
		{426, "speech_provider_endpoint_invalid"},
		{429, "speech_provider_rate_limited"},
		{503, "speech_provider_unavailable"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			svc, _, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api-ws/v1/inference", r.URL.Path)
				require.Equal(t, "Bearer supplier-key", r.Header.Get("Authorization"))
				w.Header().Set("X-Private-Response", "private-header")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"private-body supplier-key transcript"}`))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := svc.dialSpeech(ctx, account, "/api-ws/v1/inference", "")
			require.Nil(t, conn)
			require.Error(t, err)
			failure := DescribeRealtimeSpeechFailure(err)
			require.Equal(t, tc.code, failure.Code)
			require.Equal(t, tc.status, failure.UpstreamStatusCode)
			require.Equal(t, "upstream_connect", failure.Stage)
			require.Empty(t, failure.ProviderCode)
			require.NotContains(t, fmt.Sprint(failure), "private-")
			require.NotContains(t, err.Error(), "supplier-key")
			require.NotContains(t, err.Error(), "transcript")
		})
	}
}

func TestRealtimeSpeechHTTPBasePrefixesReachFixedNativeEndpoint(t *testing.T) {
	for _, endpoint := range []struct{ path, model string }{
		{"/api-ws/v1/inference", ""},
		{"/api-ws/v1/realtime", "qwen3-tts-flash-realtime"},
	} {
		for _, prefix := range []string{"", "/", "/api/v1", "/api/v1/", "/compatible-mode/v1", "/compatible-mode/v1/"} {
			t.Run(endpoint.path+prefix, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				svc, _, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, endpoint.path, r.URL.Path)
					require.Equal(t, endpoint.model, r.URL.Query().Get("model"))
					require.Equal(t, "Bearer supplier-key", r.Header.Get("Authorization"))
					conn, err := ws.Accept(w, r, nil)
					require.NoError(t, err)
					_ = conn.CloseNow()
				})
				configured := account.GetCredential("base_url") + prefix
				account.Credentials["base_url"] = configured
				conn, err := svc.dialSpeech(ctx, account, endpoint.path, endpoint.model)
				require.NoError(t, err)
				require.NotNil(t, conn)
				_ = conn.CloseNow()
				require.Equal(t, configured, account.GetCredential("base_url"), "HTTP account configuration must remain unchanged")
			})
		}
	}
}

func TestRealtimeSpeechRejectsArbitraryOrEncodedProviderPathsBeforeSupplierConnection(t *testing.T) {
	for _, suffix := range []string{"/tenant", "/api/v1/models", "/api/v1////", "/api%2fv1", "/api/v1%2F", "/api/v1?private-key=value", "/api/v1?", "/api/v1#private-key", "/api/v1#"} {
		t.Run(suffix, func(t *testing.T) {
			svc, _, account, _ := realtimeSpeechFixture(t, func(http.ResponseWriter, *http.Request) {
				t.Error("invalid base URL must not reach the supplier")
			})
			account.Credentials["base_url"] = account.GetCredential("base_url") + suffix
			_, err := svc.dialSpeech(context.Background(), account, "/api-ws/v1/inference", "")
			require.Error(t, err)
			failure := DescribeRealtimeSpeechFailure(err)
			require.Equal(t, "speech_provider_endpoint_invalid", failure.Code)
			require.Equal(t, "prepare", failure.Stage)
			require.Zero(t, failure.UpstreamStatusCode)
			var invalid *InvalidMediaRequestError
			require.ErrorAs(t, err, &invalid)
		})
	}
	t.Run("userinfo", func(t *testing.T) {
		svc, _, account, _ := realtimeSpeechFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("userinfo must be rejected") })
		account.Credentials["base_url"] = strings.Replace(account.GetCredential("base_url"), "http://", "http://private-user:private-password@", 1)
		_, err := svc.dialSpeech(context.Background(), account, "/api-ws/v1/inference", "")
		require.Error(t, err)
		require.Equal(t, "speech_provider_endpoint_invalid", DescribeRealtimeSpeechFailure(err).Code)
		require.NotContains(t, err.Error(), "private-")
	})
}

func TestRealtimeSpeechSupplierTaskFailureRetainsOnlyKnownCode(t *testing.T) {
	for _, tc := range []struct {
		providerCode string
		wantCode     string
		retained     string
	}{
		{"InvalidApiKey", "speech_provider_authentication_failed", "InvalidApiKey"},
		{"AccessDenied", "speech_provider_access_denied", "AccessDenied"},
		{"Throttling.RateQuota", "speech_provider_rate_limited", "Throttling.RateQuota"},
		{"ModelNotFound", "speech_provider_task_failed", "ModelNotFound"},
		{"private-key-and-transcript", "speech_provider_task_failed", ""},
	} {
		t.Run(tc.providerCode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			task := uuid.NewString()
			svc, _, account, meta := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := ws.Accept(w, r, nil)
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				_, _, err = conn.Read(ctx)
				require.NoError(t, err)
				speechJSON(t, conn, ctx, map[string]any{
					"header": map[string]any{"event": "task-failed", "task_id": task, "error_code": tc.providerCode,
						"error_message": "private-body supplier-key transcript", "attributes": map[string]any{"private": "credential"}},
					"payload": map[string]any{"private": "transcript"},
				})
			})
			err := svc.StreamAudioTranscription(ctx, meta, account, "public-listen", speechStart("public-listen", task),
				&speechTestClient{incoming: make(chan speechTestFrame)})
			require.Error(t, err)
			failure := DescribeRealtimeSpeechFailure(err)
			require.Equal(t, tc.wantCode, failure.Code)
			require.Equal(t, tc.retained, failure.ProviderCode)
			require.Equal(t, "upstream_task", failure.Stage)
			require.NotContains(t, fmt.Sprint(failure), "private-")
			require.NotContains(t, err.Error(), "supplier-key")
		})
	}
}

func TestRealtimeSpeechBillingAndLocalValidationFailuresAreActionableAndSafe(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{fmt.Errorf("%w: private-price-info", ErrMediaPricingNotConfigured), "speech_pricing_not_configured"},
		{fmt.Errorf("%w: private-billing-info", ErrMediaBillingUnavailable), "speech_billing_unavailable"},
		{bc.ErrState, "speech_billing_unavailable"},
		{&bc.RemoteError{Status: 402}, "speech_billing_denied"},
		{&bc.RemoteError{Status: 403}, "speech_billing_denied"},
		{&bc.RemoteError{Status: 503}, "speech_billing_unavailable"},
		{&InvalidMediaRequestError{Message: "private-user-text-or-key"}, "speech_invalid_request"},
		{fmt.Errorf("%w: private-usage-info", ErrMediaUsageIncomplete), "speech_usage_incomplete"},
		{errors.New("private-unknown-error"), "speech_provider_task_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			failure := DescribeRealtimeSpeechFailure(tc.cause)
			require.Equal(t, tc.code, failure.Code)
			require.NotEmpty(t, failure.Message)
			require.NotContains(t, fmt.Sprint(failure), "private-")
		})
	}
	for _, cause := range []error{bc.ErrState, &bc.RemoteError{Status: 403}, errors.New("private-storage-error")} {
		err := realtimeSpeechBillingError(cause)
		require.ErrorIs(t, err, cause)
		require.Equal(t, "billing", DescribeRealtimeSpeechFailure(err).Stage)
		require.NotContains(t, err.Error(), "private-")
	}
}

func TestRealtimeSpeechNetworkFailuresPreserveCauseWithoutInventingHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{&net.DNSError{Err: "private-dns-detail", Name: "private-host"}, "speech_provider_dns_failed"},
		{x509.UnknownAuthorityError{Cert: nil}, "speech_provider_tls_failed"},
		{context.DeadlineExceeded, "speech_provider_timeout"},
		{errors.New("private-network-detail"), "speech_provider_connection_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := realtimeSpeechDialError(0, tc.cause)
			require.ErrorIs(t, err, tc.cause)
			failure := DescribeRealtimeSpeechFailure(err)
			require.Equal(t, tc.code, failure.Code)
			require.Zero(t, failure.UpstreamStatusCode)
			require.NotContains(t, err.Error(), "private-")
		})
	}
}
