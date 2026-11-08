package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesOAuthRequestSummaryReadsFinalRequestAndExcludesPrivateData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"original-client-model","complexity":"medium"}`))
	c.Set(OpsUpstreamRequestBodyKey, []byte(`{"model":"earlier-mapped-model"}`))
	const finalBody = `{"model":"gpt-6-sol","reasoning":{"effort":"low"},"input":[{"type":"input_text","text":"private-prompt"},{"type":"input_image","image_url":"data:image/png;base64,cHJpdmF0ZS1pbWFnZQ=="}],"tools":[{"type":"web_search"},{"type":"image_generation","model":"gpt-image-2.6-flare","complexity":"high","quality":"low","action":"edit"}]}`
	req, err := http.NewRequest(http.MethodPost, "https://upstream.example/backend-api/codex/responses?token=private-query", strings.NewReader(finalBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer private-oauth-token")
	req.Header.Set("Cookie", "session=private-cookie")
	id := int64(1)
	account := &Account{ProxyID: &id, Proxy: &Proxy{Protocol: "http", Host: "private-proxy.example", Port: 8080, Username: "private-proxy-user", Password: "private-proxy-password"}}
	recordOpenAIImagesOAuthRequestSummary(c, req, account)
	value, ok := c.Get(OpsOpenAIImagesRequestSummaryKey)
	require.True(t, ok)
	summary, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, "upstream.example", gjson.GetBytes(summary, "host").String())
	require.Equal(t, "/backend-api/codex/responses", gjson.GetBytes(summary, "path").String())
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(summary, "parent_model").String())
	require.True(t, gjson.GetBytes(summary, "reasoning_present").Bool())
	require.True(t, gjson.GetBytes(summary, "effort_present").Bool())
	require.Equal(t, "low", gjson.GetBytes(summary, "effort").String())
	require.Equal(t, "gpt-image-2.6-flare", gjson.GetBytes(summary, "image_model").String())
	require.True(t, gjson.GetBytes(summary, "complexity_present").Bool())
	require.Equal(t, "high", gjson.GetBytes(summary, "complexity").String())
	require.Equal(t, "low", gjson.GetBytes(summary, "quality").String())
	require.Equal(t, "edit", gjson.GetBytes(summary, "action").String())
	require.True(t, gjson.GetBytes(summary, "proxy_configured").Bool())
	for _, private := range []string{"private-", "cHJpdmF0ZS1pbWFnZQ==", "original-client-model", "earlier-mapped-model", "input", "Authorization", "Cookie"} {
		require.NotContains(t, string(summary), private)
	}
	remaining, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, finalBody, string(remaining), "diagnostics must not consume or alter the outgoing body")
}

func TestImagesOAuthFailureOpsIncludesFinalRequestSummaryOnly(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"client-image","prompt":"private-prompt","quality":"low","stream":%t}`, stream))
			response := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Image tool unavailable\",\"code\":\"tool_unavailable\"}}}\n\n"
			svc, upstream, c, rec, account := imageEditServiceContext(t, body, "application/json", AccountTypeOAuth, response, http.StatusOK)
			account.Credentials["model_mapping"] = map[string]any{"client-image": "gpt-image-2.6-flare"}
			account.Credentials["base_url"] = "https://third-party.example/v1"
			_, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
			require.Error(t, err)
			detail := c.GetString(OpsUpstreamErrorDetailKey)
			require.Equal(t, "tool_unavailable", gjson.Get(detail, "error.code").String())
			require.Equal(t, upstream.lastReq.URL.Host, gjson.Get(detail, "request_summary.host").String())
			require.Equal(t, upstream.lastReq.URL.EscapedPath(), gjson.Get(detail, "request_summary.path").String())
			require.Equal(t, gjson.GetBytes(upstream.lastBody, "model").String(), gjson.Get(detail, "request_summary.parent_model").String())
			require.Equal(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists(), gjson.Get(detail, "request_summary.reasoning_present").Bool())
			require.Equal(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists(), gjson.Get(detail, "request_summary.effort_present").Bool())
			require.Equal(t, gjson.GetBytes(upstream.lastBody, "tools.0.complexity").Exists(), gjson.Get(detail, "request_summary.complexity_present").Bool())
			require.Equal(t, "gpt-image-2.6-flare", gjson.Get(detail, "request_summary.image_model").String())
			require.Equal(t, "low", gjson.Get(detail, "request_summary.quality").String())
			require.False(t, gjson.Get(detail, "request_summary.proxy_configured").Bool())
			for _, private := range []string{"private-prompt", "oauth-test", "sk-test", "client-secret", "third-party.example", "input", "image_url"} {
				require.NotContains(t, detail, private)
			}
			require.NotContains(t, rec.Body.String(), "request_summary", "diagnostics must stay out of client responses")
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)
		})
	}
}

func TestImagesOAuthMissingOptionsRemainAbsentInFailureDiagnostics(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5.6-luna","tools":[{"type":"image_generation","model":"gpt-image-2","quality":"low","action":"generate"}]}`))
	require.NoError(t, err)
	recordOpenAIImagesOAuthRequestSummary(c, req, nil)
	recordOpenAIImageUpstreamError(c, http.StatusOK, errors.New("no image results"))
	detail := c.GetString(OpsUpstreamErrorDetailKey)
	for _, field := range []string{"reasoning_present", "effort_present", "complexity_present", "proxy_configured"} {
		require.True(t, gjson.Get(detail, "request_summary."+field).Exists())
		require.False(t, gjson.Get(detail, "request_summary."+field).Bool())
	}
	require.False(t, gjson.Get(detail, "request_summary.effort").Exists())
	require.False(t, gjson.Get(detail, "request_summary.complexity").Exists())
	require.Equal(t, "no image results", c.GetString(OpsUpstreamErrorMessageKey))
}
