package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

// Image requests are synthesized as Codex Responses calls. Their upstream
// identity belongs to this bridge, independently of the caller's SDK headers.
func (s *OpenAIGatewayService) buildUpstreamImagesOAuthRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token, sessionSeed string) (*http.Request, error) {
	req, err := s.buildUpstreamRequest(ctx, c, account, body, token, true, sessionSeed, true)
	if err != nil {
		return nil, err
	}
	req.Header.Set("originator", "codex_cli_rs")
	if !openai.IsCodexCLIRequest(req.Header.Get("User-Agent")) {
		req.Header.Set("User-Agent", codexCLIUserAgent)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}
