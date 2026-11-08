package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) handleImagesForwardError(c *gin.Context, err error, streamStarted bool) {
	var upstreamErr *service.OpenAIImageUpstreamError
	if !errors.As(err, &upstreamErr) {
		h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Image request failed", streamStarted)
		return
	}
	payload := gin.H{"error": upstreamErr}
	if !streamStarted {
		c.JSON(http.StatusBadGateway, payload)
		return
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		encoded, _ := json.Marshal(payload)
		if _, writeErr := fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", encoded); writeErr != nil {
			_ = c.Error(writeErr)
		}
		flusher.Flush()
	}
}
