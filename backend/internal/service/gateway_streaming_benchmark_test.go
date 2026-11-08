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
)

// Measure a complete token stream, including framing, parsing, and usage collection.
func BenchmarkGatewayService_StreamingContentDeltas(b *testing.B) {
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{rateLimitService: &RateLimitService{}}
	payload := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"upstream-model\",\"usage\":{\"input_tokens\":10}}}\n\n" +
		strings.Repeat("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello world\"}}\n\n", 128) +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":128}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	account := &Account{ID: 1}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
		result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "requested-model", "upstream-model", false)
		if err != nil || result.usage.InputTokens != 10 || result.usage.OutputTokens != 128 {
			b.Fatalf("invalid stream result: result=%+v err=%v", result, err)
		}
	}
}
