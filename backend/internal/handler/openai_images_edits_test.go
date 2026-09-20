package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImagesEditsRejectsInvalidRequestsBeforeScheduling(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"malformed JSON", "application/json", `{"model":`},
		{"missing references", "application/json", `{"model":"gpt-image-2","prompt":"edit"}`},
		{"invalid stream", "application/json", `{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"https://example.com/image.png"}],"stream":"true"}`},
		{"missing multipart boundary", "multipart/form-data", "invalid"},
		{"unsupported format", "text/plain", "image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/images/edits", tt.body)
			c.Request.Header.Set("Content-Type", tt.contentType)
			h := &OpenAIGatewayHandler{
				gatewayService: &service.OpenAIGatewayService{}, billingCacheService: &service.BillingCacheService{},
				concurrencyHelper: &ConcurrencyHelper{},
			}
			h.ImagesEdits(c)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "invalid_request_error")
		})
	}
}

func TestImagesEditsEnforcesUploadBodyLimit(t *testing.T) {
	c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/images/edits", strings.Repeat("x", 65))
	c.Request.Body = http.MaxBytesReader(w, c.Request.Body, 64)
	h := &OpenAIGatewayHandler{
		gatewayService: &service.OpenAIGatewayService{}, billingCacheService: &service.BillingCacheService{},
		concurrencyHelper: &ConcurrencyHelper{},
	}
	h.ImagesEdits(c)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
