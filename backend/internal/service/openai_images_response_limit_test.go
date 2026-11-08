package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImagesBufferedResponsesExceedTextLimit(t *testing.T) {
	encodedImage := strings.Repeat("AAAA", 2*1024*1024+1)
	imagesJSON, err := json.Marshal(map[string]any{"created": 1, "data": []any{map[string]string{"b64_json": encodedImage}}})
	require.NoError(t, err)
	oauthJSON, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
		"status": "completed", "output": []any{map[string]string{"type": "image_generation_call", "result": encodedImage}},
	}})
	require.NoError(t, err)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, edit := range []bool{false, true} {
			name := accountType + "/generate"
			if edit {
				name = accountType + "/edit"
			}
			t.Run(name, func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":1,"images":[{"file_id":"file-source"}]}`)
				response := string(imagesJSON)
				if accountType == AccountTypeOAuth {
					response = "data: " + string(oauthJSON) + "\n\n"
				}
				svc, _, c, rec, account := imageEditServiceContext(t, body, "application/json", accountType, response, 200)
				svc.cfg.Gateway.UpstreamResponseReadMaxBytes = 8 * 1024 * 1024
				if edit {
					request, err := ParseOpenAIImageEditRequest(body, "application/json")
					require.NoError(t, err)
					result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
					require.NoError(t, err)
					require.Equal(t, 1, result.ImageCount)
				} else {
					result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
					require.NoError(t, err)
					require.Equal(t, 1, result.ImageCount)
				}
				require.Greater(t, rec.Body.Len(), 8*1024*1024)
			})
		}
	}
	_, err = ReadUpstreamResponseBody(bytes.NewReader(imagesJSON), nil, nil, nil)
	require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge, "text responses retain their existing limit")
}

func TestImagesResponseConfiguredLimitIsEnforced(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{ImageResponseReadMaxBytes: 16}}
	called := false
	body, err := ReadUpstreamImagesResponseBody(strings.NewReader(strings.Repeat("x", 17)), cfg, nil, func(*gin.Context) { called = true })
	require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge)
	require.Nil(t, body)
	require.True(t, called)
}
