package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

// RealtimeSpeechFailure is the only speech failure data safe to send to clients
// or persist in operation diagnostics. It never contains raw provider text.
type RealtimeSpeechFailure struct {
	Code               string
	Message            string
	StatusCode         int
	Stage              string
	UpstreamStatusCode int
	ProviderCode       string
}

// RealtimeSpeechError preserves the cause for errors.Is/As without including it
// in Error. Call DescribeRealtimeSpeechFailure before logging or serializing.
type RealtimeSpeechError struct {
	Failure RealtimeSpeechFailure
	cause   error
}

func (e *RealtimeSpeechError) Error() string { return e.Failure.Message }
func (e *RealtimeSpeechError) Unwrap() error { return e.cause }

var realtimeSpeechMessages = map[string]string{
	"speech_invalid_request":                "The speech request is invalid. Check the realtime model and PCM16 audio settings.",
	"speech_provider_endpoint_invalid":      "The speech provider endpoint is invalid. Configure the DashScope region or workspace root URL.",
	"speech_model_unsupported":              "The selected speech model is not supported by this realtime gateway.",
	"speech_pricing_not_configured":         "Speech model pricing is not configured in the gateway.",
	"speech_billing_unavailable":            "Speech billing authorization is unavailable. Check gateway billing configuration.",
	"speech_billing_denied":                 "Speech billing authorization was denied. Check the account balance and billing permissions.",
	"speech_provider_authentication_failed": "The speech provider rejected authentication. Check the DashScope API key and region.",
	"speech_provider_access_denied":         "The speech provider denied access. Check model activation and workspace permissions.",
	"speech_provider_rate_limited":          "The speech provider is busy or its quota is exhausted. Retry later.",
	"speech_provider_unavailable":           "The speech provider is unavailable. Check provider status and region.",
	"speech_provider_dns_failed":            "The speech provider hostname could not be resolved. Check gateway server DNS.",
	"speech_provider_tls_failed":            "The speech provider TLS connection failed. Check its certificate and gateway server trust.",
	"speech_provider_connection_failed":     "Could not connect to the speech provider. Check the endpoint and gateway server network.",
	"speech_provider_timeout":               "The speech provider connection timed out. Retry later.",
	"speech_provider_task_failed":           "The speech provider could not start or complete the task. Check model activation, region and request settings.",
	"speech_provider_protocol_failed":       "The speech provider returned an invalid realtime response.",
	"speech_usage_incomplete":               "The speech provider did not return complete usage. Gateway reconciliation is required.",
}

func realtimeSpeechFailure(code, stage string, status int) RealtimeSpeechFailure {
	return RealtimeSpeechFailure{Code: code, Message: realtimeSpeechMessages[code], StatusCode: status, Stage: stage}
}

func newRealtimeSpeechError(code, stage string, status int, cause error) *RealtimeSpeechError {
	return &RealtimeSpeechError{Failure: realtimeSpeechFailure(code, stage, status), cause: cause}
}

// DescribeRealtimeSpeechFailure maps all causes to a bounded, fixed diagnostic.
// In particular neither InvalidMediaRequestError.Message nor error.Error() is
// returned, because wrappers may contain request text, URLs or credentials.
func DescribeRealtimeSpeechFailure(err error) RealtimeSpeechFailure {
	var speech *RealtimeSpeechError
	if errors.As(err, &speech) {
		return speech.Failure
	}
	if errors.Is(err, ErrMediaPricingNotConfigured) {
		return realtimeSpeechFailure("speech_pricing_not_configured", "billing", http.StatusBadRequest)
	}
	if errors.Is(err, ErrMediaUsageIncomplete) {
		return realtimeSpeechFailure("speech_usage_incomplete", "billing", http.StatusBadGateway)
	}
	var remote *bc.RemoteError
	if errors.As(err, &remote) {
		if remote.Status == http.StatusPaymentRequired || remote.Status == http.StatusForbidden {
			return realtimeSpeechFailure("speech_billing_denied", "billing", remote.Status)
		}
		return realtimeSpeechFailure("speech_billing_unavailable", "billing", http.StatusServiceUnavailable)
	}
	if errors.Is(err, ErrMediaBillingUnavailable) || errors.Is(err, bc.ErrState) || errors.Is(err, bc.ErrConflict) || errors.Is(err, bc.ErrNotFound) {
		return realtimeSpeechFailure("speech_billing_unavailable", "billing", http.StatusServiceUnavailable)
	}
	var invalid *InvalidMediaRequestError
	if errors.As(err, &invalid) {
		return realtimeSpeechFailure("speech_invalid_request", "prepare", http.StatusBadRequest)
	}
	if errors.Is(err, ErrGroupNotAllowed) {
		return realtimeSpeechFailure("speech_provider_access_denied", "prepare", http.StatusForbidden)
	}
	if errors.Is(err, ErrGatewayRouteUnavailable) {
		return realtimeSpeechFailure("speech_provider_unavailable", "prepare", http.StatusServiceUnavailable)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return realtimeSpeechFailure("speech_provider_timeout", "upstream_connect", http.StatusGatewayTimeout)
	}
	return realtimeSpeechFailure("speech_provider_task_failed", "upstream_task", http.StatusBadGateway)
}

func realtimeSpeechBillingError(err error) error {
	if err == nil {
		return nil
	}
	failure := DescribeRealtimeSpeechFailure(err)
	if failure.Stage != "billing" {
		failure = realtimeSpeechFailure("speech_billing_unavailable", "billing", http.StatusServiceUnavailable)
	}
	return &RealtimeSpeechError{Failure: failure, cause: err}
}

func realtimeSpeechDialError(status int, err error) error {
	code, diagnosticStatus := "speech_provider_connection_failed", http.StatusBadGateway
	switch {
	case status == http.StatusUnauthorized:
		code = "speech_provider_authentication_failed"
	case status == http.StatusForbidden:
		code = "speech_provider_access_denied"
	case status == http.StatusTooManyRequests:
		code, diagnosticStatus = "speech_provider_rate_limited", http.StatusTooManyRequests
	case status >= 500:
		code, diagnosticStatus = "speech_provider_unavailable", http.StatusServiceUnavailable
	case status > 0:
		code = "speech_provider_endpoint_invalid"
	default:
		var dns *net.DNSError
		var unknownAuthority x509.UnknownAuthorityError
		var hostname x509.HostnameError
		var invalidCertificate x509.CertificateInvalidError
		var verification *tls.CertificateVerificationError
		var tlsRecord tls.RecordHeaderError
		var network net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout():
			code, diagnosticStatus = "speech_provider_timeout", http.StatusGatewayTimeout
		case errors.As(err, &dns):
			code = "speech_provider_dns_failed"
		case errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) || errors.As(err, &verification) || errors.As(err, &tlsRecord):
			code = "speech_provider_tls_failed"
		}
	}
	failure := realtimeSpeechFailure(code, "upstream_connect", diagnosticStatus)
	if status > 0 {
		failure.UpstreamStatusCode = status
	}
	return &RealtimeSpeechError{Failure: failure, cause: err}
}

func realtimeSpeechTaskError(providerCode string) error {
	// Only recognized codes are retained. A provider-controlled arbitrary string
	// could contain a key, request text or URL even if it has a code-like shape.
	code, status, safeProviderCode := "speech_provider_task_failed", http.StatusBadGateway, ""
	switch providerCode {
	case "InvalidApiKey", "invalid_api_key", "INVALID_API_KEY", "Unauthorized", "UNAUTHORIZED":
		code, status, safeProviderCode = "speech_provider_authentication_failed", http.StatusUnauthorized, providerCode
	case "AccessDenied", "Forbidden", "FORBIDDEN", "ModelAccessDenied", "ModelNotActivated", "ResourceNotOpened":
		code, status, safeProviderCode = "speech_provider_access_denied", http.StatusForbidden, providerCode
	case "Throttling", "Throttling.RateQuota", "Throttling.AllocationQuota", "QuotaExceeded", "QuotaExceed", "TooManyRequests", "RATE_LIMIT_EXCEEDED", "InsufficientQuota":
		code, status, safeProviderCode = "speech_provider_rate_limited", http.StatusTooManyRequests, providerCode
	case "InternalError", "InternalServerError", "ServiceUnavailable", "SERVER_ERROR":
		code, status, safeProviderCode = "speech_provider_unavailable", http.StatusServiceUnavailable, providerCode
	case "InvalidParameter", "InvalidParameter.Model", "ModelNotFound", "NotFound", "CLIENT_ERROR", "NO_VALID_AUDIO_ERROR", "TIMEOUT":
		safeProviderCode = providerCode
	}
	failure := realtimeSpeechFailure(code, "upstream_task", status)
	failure.ProviderCode = safeProviderCode
	return &RealtimeSpeechError{Failure: failure}
}
