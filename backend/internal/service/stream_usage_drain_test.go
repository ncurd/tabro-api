package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type countingDisconnectedWriter struct {
	gin.ResponseWriter
	writeAttempts int
	cancelClient  context.CancelFunc
}

func (w *countingDisconnectedWriter) Write(_ []byte) (int, error) {
	w.writeAttempts++
	if w.cancelClient != nil {
		w.cancelClient()
	}
	return 0, errors.New("client disconnected")
}

func newDisconnectedStreamContext(t *testing.T) (*gin.Context, *countingDisconnectedWriter) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	clientCtx, cancelClient := context.WithCancel(context.Background())
	t.Cleanup(cancelClient)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(clientCtx)
	w := &countingDisconnectedWriter{ResponseWriter: c.Writer, cancelClient: cancelClient}
	c.Writer = w
	return c, w
}

func newCanceledStreamContext(t *testing.T) (*gin.Context, *countingDisconnectedWriter) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	clientCtx, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(clientCtx)
	w := &countingDisconnectedWriter{ResponseWriter: c.Writer}
	c.Writer = w
	return c, w
}

func openAIStreamWithLateUsage() string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_drain","model":"gpt-6-astra","status":"in_progress"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_drain","model":"gpt-6-astra","status":"completed","usage":{"input_tokens":23,"output_tokens":11,"total_tokens":34,"input_tokens_details":{"cached_tokens":7}}}}`,
		``,
	}, "\n")
}

func anthropicStreamWithLateUsage() string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_drain","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":"","usage":{"input_tokens":29,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":13}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func TestChatCompletionsConversion_ClientDisconnectDrainsOpenAITerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, writer := newDisconnectedStreamContext(t)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_openai_chat_drain"}},
		Body:   io.NopCloser(strings.NewReader(openAIStreamWithLateUsage())),
	}

	result, err := (&OpenAIGatewayService{}).handleChatStreamingResponse(
		resp, c, "gpt-6-astra", "gpt-6-astra", "gpt-6-astra", true, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 23, result.Usage.InputTokens)
	require.Equal(t, 11, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, writer.writeAttempts, "must stop all downstream writes after disconnect")
	require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
}

func TestAnthropicMessagesConversion_ClientDisconnectDrainsOpenAITerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, writer := newDisconnectedStreamContext(t)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_openai_messages_drain"}},
		Body:   io.NopCloser(strings.NewReader(openAIStreamWithLateUsage())),
	}

	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}}}
	result, err := svc.handleAnthropicStreamingResponse(
		resp, c, "gpt-6-astra", "gpt-6-astra", "gpt-6-astra", time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 23, result.Usage.InputTokens)
	require.Equal(t, 11, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, writer.writeAttempts, "must stop all downstream writes after disconnect")
	require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
}

func TestChatCompletionsConversion_ClientContextCanceledBeforeWriteUsesDrainOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, writer := newCanceledStreamContext(t)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_openai_prewrite_cancel"}},
		Body:   io.NopCloser(strings.NewReader(openAIStreamWithLateUsage())),
	}

	result, err := (&OpenAIGatewayService{}).handleChatStreamingResponse(
		resp, c, "gpt-6-astra", "gpt-6-astra", "gpt-6-astra", true, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 23, result.Usage.InputTokens)
	require.Equal(t, 11, result.Usage.OutputTokens)
	require.Zero(t, writer.writeAttempts, "a canceled client must enter drain-only mode before writing")
}

func TestResponsesConversion_ClientDisconnectDrainsAnthropicTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, writer := newDisconnectedStreamContext(t)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_anthropic_responses_drain"}},
		Body:   io.NopCloser(strings.NewReader(anthropicStreamWithLateUsage())),
	}

	result, err := (&GatewayService{}).handleResponsesStreamingResponse(
		resp, c, "claude-opus-5", "claude-opus-5", nil, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 29, result.Usage.InputTokens)
	require.Equal(t, 13, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 1, writer.writeAttempts, "must stop all downstream writes after disconnect")
	require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
}

func TestChatCompletionsConversion_ClientDisconnectDrainsAnthropicTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, writer := newDisconnectedStreamContext(t)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_anthropic_chat_drain"}},
		Body:   io.NopCloser(strings.NewReader(anthropicStreamWithLateUsage())),
	}

	result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(
		resp, c, "gpt-6-astra", "claude-opus-5", nil, time.Now(), true,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 29, result.Usage.InputTokens)
	require.Equal(t, 13, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 1, writer.writeAttempts, "must stop all downstream writes after disconnect")
	require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
}

func TestClientDisconnectUsageDrain_ClientContextCancellationClosesBlockedUpstreamAtDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	clientCtx, cancelClient := context.WithCancel(context.Background())

	drain := newClientDisconnectUsageDrainWithTimeout(clientCtx, reader, 20*time.Millisecond)
	defer drain.stop()
	cancelClient()
	require.Eventually(t, drain.isDisconnected, time.Second, time.Millisecond)

	readDone := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		readDone <- err
	}()

	select {
	case err := <-readDone:
		require.Error(t, err)
		require.True(t, drain.didTimeOut())
	case <-time.After(time.Second):
		t.Fatal("bounded drain did not unblock the upstream read")
	}
}

func TestDetachedCancelableStreamContext_IgnoresClientCancellation(t *testing.T) {
	type contextKey string
	clientCtx, cancelClient := context.WithCancel(context.WithValue(context.Background(), contextKey("request"), "kept"))
	upstreamCtx, cancelUpstream := detachedCancelableStreamContext(clientCtx)
	defer cancelUpstream()

	cancelClient()
	require.NoError(t, upstreamCtx.Err())
	require.Equal(t, "kept", upstreamCtx.Value(contextKey("request")))

	cancelUpstream()
	require.ErrorIs(t, upstreamCtx.Err(), context.Canceled)
}

func TestDetachedCancelableStreamContext_CancelsAfterBoundedGrace(t *testing.T) {
	clientCtx, cancelClient := context.WithCancel(context.Background())
	upstreamCtx, cleanup := detachedCancelableStreamContextWithGrace(clientCtx, 20*time.Millisecond)
	defer cleanup()

	startedAt := time.Now()
	cancelClient()
	require.NoError(t, upstreamCtx.Err(), "client cancellation must not abort the provider immediately")

	select {
	case <-upstreamCtx.Done():
		require.GreaterOrEqual(t, time.Since(startedAt), 15*time.Millisecond)
		require.ErrorIs(t, upstreamCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("detached upstream context exceeded its cancellation grace period")
	}
}
