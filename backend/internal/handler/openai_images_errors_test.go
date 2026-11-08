package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHandleImagesForwardErrorPreservesUpstreamFields(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			err := fmt.Errorf("image upstream: %w", &service.OpenAIImageUpstreamError{Message: "Image tool unavailable", Type: "invalid_request_error", Code: "tool_not_available", Param: "tools"})
			(&OpenAIGatewayHandler{}).handleImagesForwardError(c, err, stream)
			body := rec.Body.String()
			if stream {
				require.Contains(t, body, "event: error\ndata: ")
			} else {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Equal(t, "Image tool unavailable", gjson.Get(body, "error.message").String())
			}
			require.Contains(t, body, `"type":"invalid_request_error"`)
			require.Contains(t, body, `"code":"tool_not_available"`)
			require.Contains(t, body, `"param":"tools"`)
		})
	}
}

func TestHandleImagesForwardErrorKeepsGenericLocalError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	(&OpenAIGatewayHandler{}).handleImagesForwardError(c, errors.New("internal collector detail"), false)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "Image request failed", gjson.Get(rec.Body.String(), "error.message").String())
	require.NotContains(t, rec.Body.String(), "internal collector detail")
}
