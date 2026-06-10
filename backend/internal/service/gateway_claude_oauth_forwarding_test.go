package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildUpstreamRequest_AnthropicOAuthHeadersMatchCLIProxyShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{
		ID:          901,
		Name:        "anthropic-oauth-cli-proxy-shape",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	svc := &GatewayService{}

	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "oauth-token", "oauth", "claude-sonnet-4-5-20250929", false, false)
	require.NoError(t, err)

	require.Equal(t, "Bearer oauth-token", getHeaderRaw(req.Header, "authorization"))
	require.Equal(t, "application/json", getHeaderRaw(req.Header, "content-type"))
	require.Equal(t, "application/json", getHeaderRaw(req.Header, "Accept"))
	require.Equal(t, "gzip, deflate, br, zstd", getHeaderRaw(req.Header, "Accept-Encoding"))
	require.Equal(t, "keep-alive", getHeaderRaw(req.Header, "Connection"))
	require.NotEmpty(t, getHeaderRaw(req.Header, "x-client-request-id"))
	require.Equal(t, "", getHeaderRaw(req.Header, "anthropic-dangerous-direct-browser-access"))

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	for _, token := range strings.Split(claude.DefaultBetaHeader, ",") {
		require.Contains(t, beta, strings.TrimSpace(token))
	}

	streamReq, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "oauth-token", "oauth", "claude-sonnet-4-5-20250929", true, true)
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", getHeaderRaw(streamReq.Header, "Accept"))
	require.Equal(t, "identity", getHeaderRaw(streamReq.Header, "Accept-Encoding"))
	require.Equal(t, "stream", getHeaderRaw(streamReq.Header, "x-stainless-helper-method"))
}

func TestBuildUpstreamRequest_ClaudeCodeMimicOverridesVSCodeHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "GitHubCopilotChat/0.29.0 VSCode/1.102.0")
	c.Request.Header.Set("X-App", "vscode")
	c.Request.Header.Set("X-Stainless-Lang", "python")
	c.Request.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	c.Request.Header.Set("anthropic-beta", "client-beta-2026-01-01")

	account := &Account{
		ID:          903,
		Name:        "anthropic-oauth-vscode-mimic",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
	}
	body := []byte(`{"model":"claude-fable-5","betas":["body-beta-2026-01-01"],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	svc := &GatewayService{}

	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "oauth-token", "oauth", "claude-fable-5", false, true)
	require.NoError(t, err)

	reqBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(reqBody, "betas").Exists())

	require.Equal(t, claude.DefaultHeaders["User-Agent"], getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, claude.DefaultHeaders["X-App"], getHeaderRaw(req.Header, "X-App"))
	require.Equal(t, claude.DefaultHeaders["X-Stainless-Lang"], getHeaderRaw(req.Header, "X-Stainless-Lang"))
	require.Equal(t, claude.DefaultHeaders["X-Stainless-Runtime"], getHeaderRaw(req.Header, "X-Stainless-Runtime"))
	require.Equal(t, "", getHeaderRaw(req.Header, "Anthropic-Dangerous-Direct-Browser-Access"))
	require.NotContains(t, getHeaderRaw(req.Header, "User-Agent"), "VSCode")

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.Contains(t, beta, claude.BetaClaudeCode)
	require.Contains(t, beta, claude.BetaOAuth)
	require.Contains(t, beta, "client-beta-2026-01-01")
	require.Contains(t, beta, "body-beta-2026-01-01")
}

func TestIsClaudeCodeRequest_HonorsExplicitFalseContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.63 (external, cli)")
	c.Request = c.Request.WithContext(SetClaudeCodeClient(c.Request.Context(), false))

	parsed := &ParsedRequest{
		MetadataUserID: "session_123e4567-e89b-12d3-a456-426614174000",
	}

	require.False(t, isClaudeCodeRequest(c.Request.Context(), c, parsed))
	require.True(t, shouldMimicClaudeCodeForOAuth(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, isClaudeCodeRequest(c.Request.Context(), c, parsed)))
}

func TestIsClaudeCodeMimicRelevantError_IncludesThirdPartyUsage(t *testing.T) {
	msg := "Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going."
	require.True(t, isClaudeCodeMimicRelevantError(msg))
}

func TestRewriteSystemForNonClaudeCode_InjectsCLIProxySystemBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"You are a product-specific coding assistant.","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	rewritten := rewriteSystemForNonClaudeCode(body, "You are a product-specific coding assistant.")
	signed := signBillingHeaderCCH(rewritten)

	system := gjson.GetBytes(signed, "system")
	require.True(t, system.IsArray())
	require.Len(t, system.Array(), 3)
	require.Regexp(t, `^x-anthropic-billing-header: cc_version=2\.1\.63\.[0-9a-f]{3}; cc_entrypoint=cli; cch=[0-9a-f]{5};$`, system.Array()[0].Get("text").String())
	require.Equal(t, claudeCodeSystemPrompt, system.Array()[1].Get("text").String())
	require.Contains(t, system.Array()[2].Get("text").String(), "# System")
	require.False(t, system.Array()[1].Get("cache_control").Exists())

	firstUserText := gjson.GetBytes(signed, "messages.0.content.0.text").String()
	require.Contains(t, firstUserText, "<system-reminder>")
	require.Contains(t, firstUserText, "Use the available tools when needed to help with software engineering tasks.")
	require.NotContains(t, firstUserText, "[System Instructions]")
}

func TestRewriteSystemForNonClaudeCode_NoSystemStillBuildsCLIProxySystemBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	rewritten := rewriteSystemForNonClaudeCode(body, nil)
	signed := signBillingHeaderCCH(rewritten)

	system := gjson.GetBytes(signed, "system")
	require.True(t, system.IsArray())
	require.Len(t, system.Array(), 3)
	require.Regexp(t, `^x-anthropic-billing-header: cc_version=2\.1\.63\.[0-9a-f]{3}; cc_entrypoint=cli; cch=[0-9a-f]{5};$`, system.Array()[0].Get("text").String())
	require.Equal(t, claudeCodeSystemPrompt, system.Array()[1].Get("text").String())
	require.Contains(t, system.Array()[2].Get("text").String(), "# System")

	firstUserText := gjson.GetBytes(signed, "messages.0.content.0.text").String()
	require.Equal(t, "hello", firstUserText)
}

func TestGatewayService_AnthropicOAuth_CountTokensUsesClaudeCodeMimicForVSCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("User-Agent", "GitHubCopilotChat/0.29.0 VSCode/1.102.0")
	c.Request.Header.Set("X-App", "vscode")

	body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		},
	}
	svc := &GatewayService{
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
	}
	account := &Account{
		ID:          904,
		Name:        "anthropic-oauth-vscode-count-tokens",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
	}

	err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.NoError(t, err)

	require.Equal(t, claude.DefaultHeaders["User-Agent"], getHeaderRaw(upstream.lastReq.Header, "User-Agent"))
	require.Equal(t, claude.DefaultHeaders["X-App"], getHeaderRaw(upstream.lastReq.Header, "X-App"))
	beta := getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
	require.Contains(t, beta, claude.BetaClaudeCode)
	require.Contains(t, beta, claude.BetaOAuth)
	require.Contains(t, beta, claude.BetaTokenCounting)

	system := gjson.GetBytes(upstream.lastBody, "system")
	require.True(t, system.IsArray())
	require.Len(t, system.Array(), 3)
	require.Equal(t, claudeCodeSystemPrompt, system.Array()[1].Get("text").String())
	metadataUserID := gjson.GetBytes(upstream.lastBody, "metadata.user_id").String()
	require.NotEmpty(t, metadataUserID)
	require.NotNil(t, ParseMetadataUserID(metadataUserID))
	require.False(t, gjson.GetBytes(upstream.lastBody, "betas").Exists())
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"input_tokens":42}`, rec.Body.String())
}

func TestResolveAnthropicOAuthTLSProfile_DefaultsForOAuthAndSetupToken(t *testing.T) {
	svc := &GatewayService{}

	require.NotNil(t, svc.resolveAnthropicOAuthTLSProfile(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.NotNil(t, svc.resolveAnthropicOAuthTLSProfile(&Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))
	require.Nil(t, svc.resolveAnthropicOAuthTLSProfile(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
	require.Nil(t, svc.resolveAnthropicOAuthTLSProfile(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.Nil(t, svc.resolveAnthropicOAuthTLSProfile(&Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"custom_base_url_enabled": true, "custom_base_url": "https://relay.example.com"},
	}))
}

func TestNormalizeClaudeOAuthRequestBody_MatchesCLIProxyThinkingControls(t *testing.T) {
	noThinking := []byte(`{"model":"claude-3-5-sonnet-latest","temperature":0.2,"messages":[],"tool_choice":{"type":"auto"}}`)
	out, _ := normalizeClaudeOAuthRequestBody(noThinking, "claude-3-5-sonnet-latest", claudeOAuthNormalizeOptions{})
	require.Equal(t, 0.2, gjson.GetBytes(out, "temperature").Float())
	require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice.type").String())

	withThinking := []byte(`{"model":"claude-3-5-sonnet-latest","temperature":0.2,"messages":[],"thinking":{"type":"enabled","budget_tokens":2048}}`)
	out, _ = normalizeClaudeOAuthRequestBody(withThinking, "claude-3-5-sonnet-latest", claudeOAuthNormalizeOptions{})
	require.Equal(t, 1.0, gjson.GetBytes(out, "temperature").Float())
	require.True(t, gjson.GetBytes(out, "thinking").Exists())

	forcedTool := []byte(`{"model":"claude-3-5-sonnet-latest","temperature":0,"messages":[],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"tool_choice":{"type":"any"}}`)
	out, _ = normalizeClaudeOAuthRequestBody(forcedTool, "claude-3-5-sonnet-latest", claudeOAuthNormalizeOptions{})
	require.Equal(t, 0.0, gjson.GetBytes(out, "temperature").Float())
	require.Equal(t, "any", gjson.GetBytes(out, "tool_choice.type").String())
	require.False(t, gjson.GetBytes(out, "thinking").Exists())
	require.False(t, gjson.GetBytes(out, "output_config").Exists())
}

func TestPrepareClaudeOAuthToolNamesForUpstream_RestoresOnlyRenamedNames(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"name":"Bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}},` +
		`{"name":"glob","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}}}}` +
		`],"tool_choice":{"type":"tool","name":"glob"},"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}},` +
		`{"type":"tool_use","id":"toolu_2","name":"glob","input":{}},` +
		`{"type":"tool_reference","tool_name":"glob"}` +
		`]}]}`)

	out, reverseMap := prepareClaudeOAuthToolNamesForUpstream(body)
	require.Equal(t, "Bash", gjson.GetBytes(out, "tools.0.name").String())
	require.Equal(t, "Glob", gjson.GetBytes(out, "tools.1.name").String())
	require.Equal(t, "Glob", gjson.GetBytes(out, "tool_choice.name").String())
	require.Equal(t, "Bash", gjson.GetBytes(out, "messages.0.content.0.name").String())
	require.Equal(t, "Glob", gjson.GetBytes(out, "messages.0.content.1.name").String())
	require.Equal(t, "Glob", gjson.GetBytes(out, "messages.0.content.2.tool_name").String())
	require.Equal(t, map[string]string{"Glob": "glob"}, reverseMap)

	resp := []byte(`{"content":[` +
		`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}},` +
		`{"type":"tool_use","id":"toolu_2","name":"Glob","input":{}}` +
		`]}`)
	restored := restoreClaudeOAuthToolNamesFromResponse(resp, reverseMap)
	require.Equal(t, "Bash", gjson.GetBytes(restored, "content.0.name").String())
	require.Equal(t, "glob", gjson.GetBytes(restored, "content.1.name").String())

	line := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"Glob","input":{}}}`)
	restoredLine := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
	require.Contains(t, string(restoredLine), `"name":"glob"`)
}

func TestBuildUpstreamRequest_AnthropicOAuthExtractsBodyBetasIntoHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("anthropic-beta", "client-beta-2026-01-01")

	account := &Account{
		ID:          902,
		Name:        "anthropic-oauth-body-betas",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","betas":["body-beta-2026-01-01"],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	svc := &GatewayService{}

	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "oauth-token", "oauth", "claude-sonnet-4-5-20250929", false, false)
	require.NoError(t, err)

	reqBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(reqBody, "betas").Exists())

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.Contains(t, beta, "body-beta-2026-01-01")
	require.Contains(t, beta, "client-beta-2026-01-01")
	require.Contains(t, beta, claude.BetaOAuth)
}
