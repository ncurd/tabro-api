package service

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeHeaderValueForLogRedactsGatewaySecrets(t *testing.T) {
	secretHeaders := []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		"X-API-Key",
		"X-Goog-API-Key",
		"Api-Key",
		"X-Auth-Token",
		"X-OpenAI-API-Key",
		"Idempotency-Key",
	}
	for _, header := range secretHeaders {
		t.Run(header, func(t *testing.T) {
			got := safeHeaderValueForLog(header, "Bearer highly-sensitive-value")
			if strings.Contains(got, "highly-sensitive-value") {
				t.Fatalf("%s was not redacted: %q", header, got)
			}
		})
	}
}

func TestClaudeMimicDebugLineOmitsSensitiveHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer oidc-secret")
	req.Header.Set("X-API-Key", "provider-secret")
	req.Header.Set("Content-Type", "application/json")

	line := buildClaudeMimicDebugLine(req, nil, nil, "oauth", false)
	lower := strings.ToLower(line)
	for _, forbidden := range []string{"authorization", "x-api-key", "oidc-secret", "provider-secret"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("debug line contains sensitive header material %q: %s", forbidden, line)
		}
	}
	if !strings.Contains(lower, "content-type") {
		t.Fatalf("debug line unexpectedly omitted safe headers: %s", line)
	}
}

func TestGatewaySnapshotOmitsSensitiveHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-debug.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })

	service := &GatewayService{}
	service.debugGatewayBodyFile.Store(f)
	service.debugLogGatewaySnapshot("CLIENT_ORIGINAL", http.Header{
		"Authorization":   {"Bearer oidc-secret"},
		"Idempotency-Key": {"run:node:call"},
		"Content-Type":    {"application/json"},
	}, nil, nil)
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(contents))
	for _, forbidden := range []string{"authorization", "idempotency-key", "oidc-secret", "run:node:call"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("snapshot contains sensitive header material %q: %s", forbidden, contents)
		}
	}
	if !strings.Contains(lower, "content-type") {
		t.Fatalf("snapshot unexpectedly omitted safe headers: %s", contents)
	}
}
