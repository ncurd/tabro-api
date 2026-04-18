package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func testChatCompletionChunkSSE() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl_qwen_1","object":"chat.completion.chunk","created":1,"model":"qwen-plus","choices":[{"index":0,"delta":{"role":"assistant","content":"hello "},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_qwen_1","object":"chat.completion.chunk","created":1,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_qwen_1","object":"chat.completion.chunk","created":1,"model":"qwen-plus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
}

func testAliyunResponsesRawJSONStream() string {
	return strings.Join([]string{
		`{"type":"response.created","response":{"id":"resp_qwen_1","object":"response","model":"qwen-plus","status":"queued"}}`,
		`{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"in_progress"},"output_index":0}`,
		`{"type":"response.output_text.delta","delta":"hello ","output_index":0,"content_index":0}`,
		`{"type":"response.output_text.delta","delta":"world","output_index":0,"content_index":0}`,
		`{"type":"response.completed","response":{"id":"resp_qwen_1","object":"response","model":"qwen-plus","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello world"}]}],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}}`,
	}, "\n")
}

func TestHandleChatBufferedStreamingResponse_FallsBackToChatCompletionChunkSSE(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  []string{"text/event-stream"},
			"x-request-id":  []string{"rid_chat_fallback"},
			"X-Request-Id":  []string{"rid_chat_fallback"},
			"Cache-Control": []string{"no-cache"},
		},
		Body: io.NopCloser(strings.NewReader(testChatCompletionChunkSSE())),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleChatBufferedStreamingResponse(resp, c, "qwen-plus", "qwen-plus", "qwen-plus", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	var chatResp apicompat.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &chatResp))
	require.Equal(t, "chatcmpl_qwen_1", chatResp.ID)
	require.Equal(t, "qwen-plus", chatResp.Model)
	require.Len(t, chatResp.Choices, 1)
	require.Equal(t, "stop", chatResp.Choices[0].FinishReason)

	var content string
	require.NoError(t, json.Unmarshal(chatResp.Choices[0].Message.Content, &content))
	require.Equal(t, "hello world", content)
}

func TestHandleAnthropicBufferedStreamingResponse_FallsBackToChatCompletionChunkSSE(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_messages_fallback"},
		},
		Body: io.NopCloser(strings.NewReader(testChatCompletionChunkSSE())),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "qwen-plus", "qwen-plus", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	var anthropicResp apicompat.AnthropicResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &anthropicResp))
	require.Equal(t, "chatcmpl_qwen_1", anthropicResp.ID)
	require.Equal(t, "claude-sonnet-4.5", anthropicResp.Model)
	require.Equal(t, "end_turn", anthropicResp.StopReason)
	require.NotEmpty(t, anthropicResp.Content)
	require.Equal(t, "text", anthropicResp.Content[0].Type)
	require.Equal(t, "hello world", anthropicResp.Content[0].Text)
}

func TestHandleChatBufferedStreamingResponse_AcceptsAliyunResponsesRawJSONStream(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_chat_aliyun_raw"},
		},
		Body: io.NopCloser(strings.NewReader(testAliyunResponsesRawJSONStream())),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleChatBufferedStreamingResponse(resp, c, "qwen-plus", "qwen-plus", "qwen-plus", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	var chatResp apicompat.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &chatResp))
	require.Equal(t, "resp_qwen_1", chatResp.ID)
	require.Len(t, chatResp.Choices, 1)
	require.Equal(t, "stop", chatResp.Choices[0].FinishReason)
	var content string
	require.NoError(t, json.Unmarshal(chatResp.Choices[0].Message.Content, &content))
	require.Equal(t, "hello world", content)
}

func TestHandleAnthropicBufferedStreamingResponse_AcceptsAliyunResponsesRawJSONStream(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_messages_aliyun_raw"},
		},
		Body: io.NopCloser(strings.NewReader(testAliyunResponsesRawJSONStream())),
	}

	svc := &OpenAIGatewayService{}
	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "qwen-plus", "qwen-plus", time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	var anthropicResp apicompat.AnthropicResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &anthropicResp))
	require.Equal(t, "resp_qwen_1", anthropicResp.ID)
	require.NotEmpty(t, anthropicResp.Content)
	require.Equal(t, "hello world", anthropicResp.Content[0].Text)
}
