package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeValidator_ProbeBypass(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.IsMaxTokensOneHaikuRequest, true))

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_ProbeBypassRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.IsMaxTokensOneHaikuRequest, true))

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesWithoutProbeStillNeedStrictValidation(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_NonMessagesPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(req, nil)
	require.True(t, ok)
}

func TestExtractVersion(t *testing.T) {
	v := NewClaudeCodeValidator()
	tests := []struct {
		ua   string
		want string
	}{
		{"claude-cli/2.1.22 (darwin; arm64)", "2.1.22"},
		{"claude-cli/1.0.0", "1.0.0"},
		{"Claude-CLI/3.10.5 (linux; x86_64)", "3.10.5"}, // 大小写不敏感
		{"curl/8.0.0", ""},                              // 非 Claude CLI
		{"", ""},                                        // 空字符串
		{"claude-cli/", ""},                             // 无版本号
		{"claude-cli/2.1.22-beta", "2.1.22"},            // 带后缀仍提取主版本号
	}
	for _, tt := range tests {
		got := v.ExtractVersion(tt.ua)
		require.Equal(t, tt.want, got, "ExtractVersion(%q)", tt.ua)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.1.0", "2.1.0", 0},   // 相等
		{"2.1.1", "2.1.0", 1},   // patch 更大
		{"2.0.0", "2.1.0", -1},  // minor 更小
		{"3.0.0", "2.99.99", 1}, // major 更大
		{"1.0.0", "2.0.0", -1},  // major 更小
		{"0.0.1", "0.0.0", 1},   // patch 差异
		{"", "1.0.0", -1},       // 空字符串 vs 正常版本
		{"v2.1.0", "2.1.0", 0},  // v 前缀处理
	}
	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		require.Equal(t, tt.want, got, "CompareVersions(%q, %q)", tt.a, tt.b)
	}
}

func TestSetGetClaudeCodeVersion(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "", GetClaudeCodeVersion(ctx), "empty context should return empty string")

	ctx = SetClaudeCodeVersion(ctx, "2.1.63")
	require.Equal(t, "2.1.63", GetClaudeCodeVersion(ctx))
}

func nativeClaudeValidationRequest(path string) (*http.Request, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", claude.BetaClaudeCode+","+claude.BetaOAuth)
	return req, map[string]any{
		"model":    "claude-sonnet-4-6",
		"metadata": map[string]any{"user_id": FormatMetadataUserID(strings.Repeat("a", 64), "", "12345678-1234-4234-8234-123456789abc", "2.1.280")},
	}
}

func TestClaudeCodeValidator_CountTokensRequiresNativeHeaders(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req, body := nativeClaudeValidationRequest("/v1/messages/count_tokens")
	delete(body, "metadata")
	require.True(t, validator.Validate(req, body), "native token counting omits the identity system and metadata")
	for _, header := range []string{"X-App", "Anthropic-Version", "Anthropic-Beta"} {
		t.Run(header, func(t *testing.T) {
			candidate := req.Clone(req.Context())
			candidate.Header.Del(header)
			require.False(t, validator.Validate(candidate, body))
		})
	}
	req.Header.Set("Anthropic-Beta", claude.BetaOAuth)
	require.False(t, validator.Validate(req, body), "a copied UA and unrelated beta do not grant native access")
}

func TestClaudeCodeValidator_BillingOnlyNativeRequest(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req, body := nativeClaudeValidationRequest("/v1/messages")
	body["system"] = []any{map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.280.179; cc_entrypoint=claude-vscode;"}}
	require.True(t, validator.Validate(req, body))
	req.Header.Set("Anthropic-Beta", claude.BetaOAuth)
	require.False(t, validator.Validate(req, body))
	req.Header.Set("Anthropic-Beta", claude.BetaClaudeCode)
	delete(body, "metadata")
	require.False(t, validator.Validate(req, body))
}

func TestClaudeCodeValidator_NativeMinimalHelpers(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req, body := nativeClaudeValidationRequest("/v1/messages")
	body["max_tokens"] = 1
	body["messages"] = []any{map[string]any{"role": "user", "content": "quota probe"}}
	require.True(t, validator.Validate(req, body), "native probes can target Sonnet as well as Haiku")
	body["max_tokens"] = 2
	require.False(t, validator.Validate(req, body))
	body["max_tokens"] = 1
	body["tools"] = []any{map[string]any{"name": "Bash"}}
	require.False(t, validator.Validate(req, body))
	delete(body, "tools")
	body["model"] = "claude-haiku-4-5-20251001"
	req.Header.Set("Anthropic-Beta", strings.Join([]string{claude.BetaOAuth, claude.BetaInterleavedThinking, claude.BetaContextManagement, claude.BetaPromptCachingScope, "thinking-token-count-2026-05-13"}, ","))
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Package-Version", "0.112.1")
	req.Header.Set("X-Claude-Code-Session-Id", "12345678-1234-4234-8234-123456789abc")
	require.True(t, validator.Validate(req, body), "measured Haiku probes omit the Claude Code beta")
	body["max_tokens"] = 80
	body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object"}}}
	require.True(t, validator.Validate(req, body), "native session-title helpers omit system prose")
	req.Header.Set("X-Claude-Code-Session-Id", "different-session")
	require.False(t, validator.Validate(req, body), "helper session must match metadata")
}

func TestClaudeCodeValidator_SecurityMonitorRequiresCompleteProfile(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req, body := nativeClaudeValidationRequest("/v1/messages")
	prompt := claudeCodeSecurityMonitorPrefix + "\n" + strings.Join(claudeCodeSecurityMonitorMarkers, "\n") + strings.Repeat("x", 10_000)
	body["system"] = []any{map[string]any{"type": "text", "text": prompt}, map[string]any{"type": "text", "text": "session context"}}
	require.True(t, validator.Validate(req, body))
	body["system"] = []any{map[string]any{"type": "text", "text": strings.ReplaceAll(prompt, "## HARD BLOCK", "")}}
	require.False(t, validator.Validate(req, body))
}
