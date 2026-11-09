package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Keep the account's device identity while selecting one coherent software
// profile for proxy-managed requests, even when Redis contains an older CLI.
func managedClaudeCodeFingerprint(fp *Fingerprint) *Fingerprint {
	copy := defaultFingerprint
	if fp != nil {
		copy = *fp
	}
	copy.UserAgent = claude.DefaultHeaders["User-Agent"]
	copy.StainlessLang = claude.DefaultHeaders["X-Stainless-Lang"]
	copy.StainlessPackageVersion = claude.DefaultHeaders["X-Stainless-Package-Version"]
	copy.StainlessRuntime = claude.DefaultHeaders["X-Stainless-Runtime"]
	copy.StainlessRuntimeVersion = claude.DefaultHeaders["X-Stainless-Runtime-Version"]
	return &copy
}

func ensureClaudeBillingCCHPlaceholder(body []byte) []byte {
	text := gjson.GetBytes(body, "system.0.text").String()
	if !strings.HasPrefix(text, "x-anthropic-billing-header:") || strings.Contains(text, "cch=") {
		return body
	}
	start := strings.Index(text, "cc_entrypoint=")
	if start < 0 {
		return body
	}
	end := strings.IndexByte(text[start:], ';')
	if end < 0 {
		return body
	}
	at := start + end + 1
	next, err := sjson.SetBytes(body, "system.0.text", text[:at]+" cch=00000;"+text[at:])
	if err != nil {
		return body
	}
	return next
}

func sanitizeClaudeBodyForBetaHeader(body []byte, beta string) []byte {
	for _, field := range []struct{ path, beta string }{
		{"context_management", claude.BetaContextManagement},
		{"thinking.block_binding", "thinking-binding-controls-2026-08-01"},
		{"fallbacks", "server-side-fallback-2026-07-01"},
		{"fallback_credit_token", "fallback-credit-2026-07-01"},
	} {
		allowed := containsBetaToken(beta, field.beta)
		if field.path == "fallbacks" {
			allowed = allowed || containsBetaToken(beta, "server-side-fallback-2026-06-01")
		}
		if field.path == "fallback_credit_token" {
			allowed = allowed || containsBetaToken(beta, "fallback-credit-2026-06-01")
		}
		if !allowed {
			if next, err := sjson.DeleteBytes(body, field.path); err == nil {
				body = next
			}
		}
	}
	return body
}

func finalizeClaudeOAuthBody(req *http.Request, body []byte, countTokens, managed bool) []byte {
	body = sanitizeClaudeBodyForBetaHeader(body, getHeaderRaw(req.Header, "anthropic-beta"))
	if countTokens {
		// count_tokens is not a Messages generation request. Native helpers do
		// not send metadata or generation controls, and must not be signed.
		for _, path := range []string{"metadata", "context_management", "diagnostics", "max_tokens", "stream", "temperature", "top_p", "top_k", "stop_sequences", "stop", "fallbacks", "fallback_credit_token"} {
			body, _ = sjson.DeleteBytes(body, path)
		}
		if managed {
			body, _ = sjson.DeleteBytes(body, "system")
		}
	} else {
		body = signBillingHeaderCCH(ensureClaudeBillingCCHPlaceholder(body))
	}
	// Signing is the last body mutation, after the final beta policy is known.
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return body
}
