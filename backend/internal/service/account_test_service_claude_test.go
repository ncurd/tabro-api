//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_ClaudeOAuthUsesProductionRequestBuilder(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			c, recorder := newTestContext()
			c.Request.Header.Set("User-Agent", "admin-browser/1.0")
			c.Request.Header.Set("Anthropic-Beta", "admin-only-beta")
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")}}
			svc := &AccountTestService{httpUpstream: upstream}
			account := &Account{ID: 44, Platform: PlatformAnthropic, Type: accountType, Credentials: map[string]any{"access_token": "oauth-secret"}, Extra: map[string]any{"account_uuid": "account-uuid"}}
			require.NoError(t, svc.testClaudeAccountConnection(c, account, "claude-sonnet-4-5"))
			require.Len(t, upstream.requests, 1)
			req := upstream.requests[0]
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "claude-sonnet-4-5-20250929", gjson.GetBytes(body, "model").String())
			require.Equal(t, "Bearer oauth-secret", getHeaderRaw(req.Header, "Authorization"))
			require.Equal(t, claude.DefaultHeaders["User-Agent"], getHeaderRaw(req.Header, "User-Agent"))
			require.Equal(t, "application/json", getHeaderRaw(req.Header, "Accept"))
			require.NotContains(t, getHeaderRaw(req.Header, "Anthropic-Beta"), "admin-only-beta")
			require.True(t, upstream.tlsFlags[0])
			billing := gjson.GetBytes(body, "system.0.text").String()
			require.Contains(t, billing, "cc_version="+ExtractCLIVersion(getHeaderRaw(req.Header, "User-Agent"))+".")
			require.Regexp(t, `cch=[0-9a-f]{5};`, billing)
			require.NotContains(t, billing, "cch=00000;")
			require.Equal(t, string(body), string(signBillingHeaderCCH(body)), "test request must be signed after its final body transforms")
			userID := ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String())
			require.NotNil(t, userID)
			require.Equal(t, "account-uuid", userID.AccountUUID)
			require.Equal(t, userID.SessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))
			require.Contains(t, recorder.Body.String(), `"success":true`)
		})
	}
}

func TestAccountTestService_ClaudeAPIKeyPreservesExplicitMapping(t *testing.T) {
	c, _ := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 45, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "api-secret", "base_url": "https://compatible.example", "model_mapping": map[string]any{"claude-sonnet-4-5": "vendor-model"},
	}}
	require.NoError(t, svc.testClaudeAccountConnection(c, account, "claude-sonnet-4-5"))
	req := upstream.requests[0]
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "https://compatible.example/v1/messages?beta=true", req.URL.String())
	require.Equal(t, "vendor-model", gjson.GetBytes(body, "model").String())
	require.Equal(t, "api-secret", req.Header.Get("X-Api-Key"))
	require.Empty(t, req.Header.Get("Authorization"))
	require.NotContains(t, string(body), "x-anthropic-billing-header:")
}

func TestAccountTestService_ClaudeOAuthHonorsCustomRelay(t *testing.T) {
	c, _ := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 46, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "oauth-secret"}, Extra: map[string]any{
		"custom_base_url_enabled": true, "custom_base_url": "https://relay.example",
	}}
	require.NoError(t, svc.testClaudeAccountConnection(c, account, "claude-sonnet-4-6"))
	require.Equal(t, "https://relay.example/v1/messages?beta=true", upstream.requests[0].URL.String())
	require.False(t, upstream.tlsFlags[0], "custom relays retain their configured TLS behavior")
}

func TestProcessClaudeStreamRequiresCompletion(t *testing.T) {
	for _, body := range []string{"", "data: {\"type\":\"message_start\"}\n\n", "data: {\"type\":\"error\",\"error\":{\"message\":\"version rejected\"}}\n\n"} {
		c, recorder := newTestContext()
		svc := &AccountTestService{}
		require.Error(t, svc.processClaudeStream(c, strings.NewReader(body)))
		require.NotContains(t, recorder.Body.String(), `"success":true`)
	}
	c, recorder := newTestContext()
	svc := &AccountTestService{}
	require.NoError(t, svc.processClaudeStream(c, strings.NewReader("data: {\"type\":\"message_stop\"}")))
	require.Contains(t, recorder.Body.String(), `"success":true`)
}
