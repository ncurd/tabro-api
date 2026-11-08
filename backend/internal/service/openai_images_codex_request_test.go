package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesOAuthUsesCodexIdentityForSynthesizedRequests(t *testing.T) {
	for _, edit := range []bool{false, true} {
		operation := "generation"
		if edit {
			operation = "edit"
		}
		for _, tc := range []struct {
			name, clientUA, clientOriginator, accountUA, expectedUA string
		}{
			{"python_opencode", "OpenAI/Python 2.0.0", "opencode", "", codexCLIUserAgent},
			{"missing_headers", "", "", "", codexCLIUserAgent},
			{"valid_client_codex", "codex_cli_rs/0.159.0 client", "opencode", "", "codex_cli_rs/0.159.0 client"},
			{"valid_account_codex", "OpenAI/Python 2.0.0", "external-provider", "codex_cli_rs/0.160.0 account", "codex_cli_rs/0.160.0 account"},
			{"invalid_account_ua", "codex_cli_rs/0.159.0 client", "opencode", "proxy-client/1.0", codexCLIUserAgent},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":1}`)
				contentType, path := "application/json", "/v1/images/generations"
				var editRequest *OpenAIImageEditRequest
				if edit {
					body, contentType = imageEditMultipart(t, "gpt-image-2", false, 1)
					path = "/v1/images/edits"
					var err error
					editRequest, err = ParseOpenAIImageEditRequest(body, contentType)
					require.NoError(t, err)
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", contentType)
				c.Request.Header.Set("Authorization", "Bearer client-secret")
				c.Request.Header.Set("User-Agent", tc.clientUA)
				c.Request.Header.Set("originator", tc.clientOriginator)
				c.Request.Header.Set("session_id", "untrusted-client-session")
				c.Request.Header.Set("conversation_id", "untrusted-client-conversation")
				c.Set("api_key", &APIKey{ID: 777})
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"image\"}]}}\n\n")),
				}}
				svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
					"access_token": "oauth-test", "chatgpt_account_id": "acct-42", "user_agent": tc.accountUA,
				}}
				var err error
				seedBody := body
				action := "generate"
				if edit {
					_, err = svc.ForwardImagesEdits(context.Background(), c, account, editRequest)
					seedBody, action = editRequest.MetadataBody, "edit"
				} else {
					_, err = svc.ForwardImagesGenerations(context.Background(), c, account, body)
				}
				require.NoError(t, err)
				require.Equal(t, "codex_cli_rs", upstream.lastReq.Header.Get("originator"))
				require.Equal(t, tc.expectedUA, upstream.lastReq.Header.Get("User-Agent"))
				require.Equal(t, "Bearer oauth-test", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "acct-42", upstream.lastReq.Header.Get("chatgpt-account-id"))
				require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
				require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
				require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
				require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
				require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
				expectedSession := isolateOpenAISessionID(777, openAIImagesOAuthSessionSeed(seedBody))
				require.Equal(t, expectedSession, upstream.lastReq.Header.Get("session_id"))
				require.Equal(t, expectedSession, upstream.lastReq.Header.Get("conversation_id"))
				require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
				require.Equal(t, action, gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
			})
		}
	}
}

func TestImagesAPIKeyKeepsItsNativeClientIdentity(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{false: "generation", true: "edit"}[edit], func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":1}`)
			contentType := "application/json"
			var editRequest *OpenAIImageEditRequest
			if edit {
				body, contentType = imageEditMultipart(t, "gpt-image-2", false, 1)
				var err error
				editRequest, err = ParseOpenAIImageEditRequest(body, contentType)
				require.NoError(t, err)
			}
			svc, upstream, c, _, account := imageEditServiceContext(t, body, contentType, AccountTypeAPIKey, `{"created":1,"data":[{"b64_json":"image"}]}`, http.StatusOK)
			c.Request.Header.Set("User-Agent", "OpenAI/Python 2.0.0")
			c.Request.Header.Set("originator", "opencode")
			var err error
			if edit {
				_, err = svc.ForwardImagesEdits(context.Background(), c, account, editRequest)
			} else {
				_, err = svc.ForwardImagesGenerations(context.Background(), c, account, body)
			}
			require.NoError(t, err)
			require.Equal(t, "OpenAI/Python 2.0.0", upstream.lastReq.Header.Get("User-Agent"))
			require.Empty(t, upstream.lastReq.Header.Get("originator"))
			require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "proxy.example.com", upstream.lastReq.URL.Host)
		})
	}
}
