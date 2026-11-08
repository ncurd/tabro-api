package service

import (
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

func TestGatewayService_StreamingDeltasPreservePayloadAndCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{rateLimitService: &RateLimitService{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	setClaudeOAuthToolNamesReverseMap(c, map[string]string{"Glob": "glob"})
	deltas := []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello \"world\"\n你好"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"consider upstream-model"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"keep-this-signature"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"Glob\"}"}}`,
	}
	payloads := []string{
		`{"type":"message_start","message":{"model":"upstream-model","usage":{"input_tokens":10,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}}}}`,
		`{"type":"content_block_start","content_block":{"type":"tool_use","name":"Glob","id":"tool-1","input":{}}}`,
	}
	payloads = append(payloads, deltas...)
	payloads = append(payloads,
		// Compatibility fields on vendor-specific deltas still need rewriting.
		`{"type":"content_block_delta","mess\u0061ge":{"model":"upstream-model"}}`,
		`{"type":"content_block_delta","content_block":{"type":"tool_reference","tool_name":"Glob"}}`,
		`{"type":"message_delta","usage":{"output_tokens":12}}`,
		`{"type":"message_stop"}`,
	)
	var stream strings.Builder
	for _, payload := range payloads {
		stream.WriteString("data:\t")
		stream.WriteString(payload)
		stream.WriteString("\n\n")
	}
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream.String()))}
	account := &Account{
		ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Extra: map[string]any{"cache_ttl_override_enabled": true, "cache_ttl_override_target": "1h"},
	}
	result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "requested-model", "upstream-model", false)
	require.NoError(t, err)
	require.Equal(t, 10, result.usage.InputTokens)
	require.Equal(t, 12, result.usage.OutputTokens)
	require.Equal(t, 7, result.usage.CacheReadInputTokens)
	require.Equal(t, 0, result.usage.CacheCreation5mTokens)
	require.Equal(t, 5, result.usage.CacheCreation1hTokens)
	for _, delta := range deltas {
		require.Contains(t, rec.Body.String(), "event: content_block_delta\ndata: "+delta+"\n\n")
	}
	var output []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if data, ok := extractAnthropicSSEDataLine(line); ok {
			output = append(output, data)
		}
	}
	require.Len(t, output, len(payloads))
	require.Equal(t, "requested-model", gjson.Get(output[0], "message.model").String())
	require.Equal(t, "glob", gjson.Get(output[1], "content_block.name").String())
	require.Equal(t, "requested-model", gjson.Get(output[6], "message.model").String())
	require.Equal(t, "glob", gjson.Get(output[7], "content_block.tool_name").String())
}

func TestAnthropicContentDeltaRejectsPayloadsRequiringNormalParsing(t *testing.T) {
	for _, data := range []string{
		`{"type":"content_block_delta","delta":`,
		`{"type":"content_block_delta","type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		`{"type":"content_block_delta","type":"message_stop"}`,
		`{"type":"content_block_delta","type":null}`,
		`{"type":"content_block_delta","mess\u0061ge":{"model":"upstream"}}`,
		`{"type":"content_block_delta","content_block":{"type":"tool_use","name":"Glob"}}`,
	} {
		t.Run(data, func(t *testing.T) {
			require.False(t, anthropicContentDeltaCanPassThrough(data))
		})
	}
}
