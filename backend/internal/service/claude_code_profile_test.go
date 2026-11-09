package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type claudeLegacyProfileCache struct {
	identityCacheStub
	fingerprint *Fingerprint
}

func (c *claudeLegacyProfileCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return c.fingerprint, nil
}

func TestClaudeCodeProfileUpgradesCachedSoftwareWithoutChangingDevice(t *testing.T) {
	for _, count := range []bool{false, true} {
		name := "messages"
		if count {
			name = "count_tokens"
		}
		t.Run(name, func(t *testing.T) {
			cache := &claudeLegacyProfileCache{fingerprint: &Fingerprint{
				ClientID: strings.Repeat("a", 64), UpdatedAt: time.Now().Unix(),
				UserAgent: "claude-cli/2.1.63 (external, cli)", StainlessPackageVersion: "0.74.0",
				StainlessRuntime: "node", StainlessRuntimeVersion: "v24.3.0", StainlessOS: "Linux", StainlessArch: "x64",
			}}
			svc := &GatewayService{identityService: NewIdentityService(cache)}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("User-Agent", "GitHubCopilotChat/1.0")
			account := &Account{ID: 1, Type: AccountTypeOAuth, Platform: PlatformAnthropic, Extra: map[string]any{"account_uuid": "test-account"}}
			body := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,"fallbacks":[{"model":"other"}],"metadata":{"user_id":"user_` + strings.Repeat("b", 64) + `_account__session_123e4567-e89b-42d3-a456-426614174000"},"system":"Keep the customer's complete instructions.","messages":[{"role":"user","content":"hello world"}]}`)
			body = rewriteSystemForNonClaudeCode(body, "Keep the customer's complete instructions.")
			var req *http.Request
			var err error
			if count {
				req, err = svc.buildCountTokensRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-opus-5-5", true)
			} else {
				req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-opus-5-5", true, true)
			}
			require.NoError(t, err)
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "2.1.280", ExtractCLIVersion(getHeaderRaw(req.Header, "User-Agent")), "reproduce the provider's minimum-version requirement")
			require.Equal(t, "0.112.1", getHeaderRaw(req.Header, "X-Stainless-Package-Version"))
			require.Equal(t, "v26.3.0", getHeaderRaw(req.Header, "X-Stainless-Runtime-Version"))
			require.Equal(t, "Linux", getHeaderRaw(req.Header, "X-Stainless-OS"))
			require.Equal(t, "x64", getHeaderRaw(req.Header, "X-Stainless-Arch"))
			require.Equal(t, "2.1.63", ExtractCLIVersion(cache.fingerprint.UserAgent), "do not mutate the shared cached device profile")
			require.Contains(t, string(wire), "Keep the customer's complete instructions.")
			require.False(t, gjson.GetBytes(wire, "fallbacks").Exists())
			require.Equal(t, int64(len(wire)), req.ContentLength)
			if count {
				for _, key := range []string{"system", "metadata", "max_tokens", "stream"} {
					require.False(t, gjson.GetBytes(wire, key).Exists(), key)
				}
			} else {
				require.Contains(t, gjson.GetBytes(wire, "system.0.text").String(), "cc_version="+claude.CLICurrentVersion+".")
				uid := ParseMetadataUserID(gjson.GetBytes(wire, "metadata.user_id").String())
				require.NotNil(t, uid)
				require.True(t, uid.IsNewFormat)
				require.Equal(t, cache.fingerprint.ClientID, uid.DeviceID)
				require.Equal(t, wire, signBillingHeaderCCH(wire), "sign only after all beta-dependent body changes")
			}
		})
	}
}

func TestClaudeCodeNativeClientKeepsItsVersionAndToolSchema(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.300 (external, cli)")
	c.Request.Header.Set("anthropic-beta", "oauth-2025-04-20,new-client-feature-2026-11-09")
	body := []byte(`{"model":"claude-opus-5-5","tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"read"},"thinking":{"type":"adaptive"},"messages":[]}`)
	req, err := (&GatewayService{}).buildUpstreamRequest(context.Background(), c, &Account{Type: AccountTypeOAuth}, body, "test-token", "oauth", "claude-opus-5-5", false, false)
	require.NoError(t, err)
	wire, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.300 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
	require.Contains(t, getHeaderRaw(req.Header, "anthropic-beta"), "new-client-feature-2026-11-09")
	require.Equal(t, "read", gjson.GetBytes(wire, "tools.0.name").String())
	require.Equal(t, "read", gjson.GetBytes(wire, "tool_choice.name").String())
	require.Equal(t, "adaptive", gjson.GetBytes(wire, "thinking.type").String())
}

func TestClaudeCodeNativeProfileIsNotUpgradedByAnotherClientsCache(t *testing.T) {
	for _, count := range []bool{false, true} {
		cache := &claudeLegacyProfileCache{fingerprint: &Fingerprint{UserAgent: "claude-cli/2.1.300 (external, cli)", ClientID: strings.Repeat("a", 64), UpdatedAt: time.Now().Unix()}}
		svc := &GatewayService{identityService: NewIdentityService(cache)}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
		c.Request.Header.Set("X-Stainless-Package-Version", "0.112.1")
		body := []byte(`{"model":"claude-opus-5-5","messages":[],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280.abc; cc_entrypoint=cli;"}]}`)
		account := &Account{ID: 1, Type: AccountTypeOAuth}
		var req *http.Request
		var err error
		if count {
			req, err = svc.buildCountTokensRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-opus-5-5", false)
		} else {
			req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-opus-5-5", false, false)
		}
		require.NoError(t, err)
		require.Equal(t, "claude-cli/2.1.280 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
		require.Equal(t, "claude-cli/2.1.300 (external, cli)", cache.fingerprint.UserAgent)
	}
}

func TestClaudeCodeProxyPreservesInstructionsFollowingOfficialBanner(t *testing.T) {
	text := claudeCodeSystemPrompt + "\n\nAlways respond using the customer's JSON schema."
	body := rewriteSystemForNonClaudeCode([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), text)
	require.Contains(t, gjson.GetBytes(body, "messages.0.content").String(), "Always respond using the customer's JSON schema.")
}

func TestClaudeCodeBodyFollowsFinalBetaPolicy(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive","block_binding":{"type":"none"}},"context_management":{"edits":[]},"fallbacks":[],"fallback_credit_token":"credit"}`)
	without := sanitizeClaudeBodyForBetaHeader(body, claude.BetaOAuth)
	for _, key := range []string{"thinking.block_binding", "context_management", "fallbacks", "fallback_credit_token"} {
		require.False(t, gjson.GetBytes(without, key).Exists(), key)
	}
	with := sanitizeClaudeBodyForBetaHeader(body, "thinking-binding-controls-2026-08-01,context-management-2025-06-27,server-side-fallback-2026-07-01,fallback-credit-2026-07-01")
	require.JSONEq(t, string(body), string(with))
}
