package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHandleChatBufferedStreamingResponse_ReconstructsFromDeltasWithoutTerminalResponseObject(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_chat_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_chat_buffered","model":"gpt-5.4"}}`,
			``,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			``,
			`data: {"type":"response.output_text.delta","delta":" world"}`,
			``,
			`data: {"type":"response.done"}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleChatBufferedStreamingResponse(resp, c, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello world", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

func TestForwardAsChatCompletions_SyncClientReconstructsDeltaOnlyUpstreamStream(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_forward_chat_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_forward_chat_buffered","model":"gpt-5.4"}}`,
			``,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			``,
			`data: {"type":"response.output_text.delta","delta":" world"}`,
			``,
			`data: {"type":"response.done"}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello world", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

func TestHandleAnthropicBufferedStreamingResponse_ReconstructsFromDeltasWithoutTerminalResponseObject(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_messages_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_messages_buffered","model":"gpt-5.4"}}`,
			``,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			``,
			`data: {"type":"response.output_text.delta","delta":" world"}`,
			``,
			`data: {"type":"response.done"}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello world", gjson.GetBytes(rec.Body.Bytes(), "content.0.text").String())
}
