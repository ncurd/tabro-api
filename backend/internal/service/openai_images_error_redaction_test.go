package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImagesOAuthUpstreamErrorRedactsEchoedSecretsAndImageData(t *testing.T) {
	message := "Rejected Authorization: Bearer fake.oauth-token_123; key sk-proj-fakeKey123456; image data:image/png;base64,cHJpdmF0ZS1pbWFnZQ==; https://example.com?api_key=fake-query-secret"
	body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": "invalid_request_error", "code": "invalid_image", "param": "images"}})
	require.NoError(t, err)
	upstreamErr := extractOpenAIImageUpstreamError(body)
	require.NotNil(t, upstreamErr)
	encoded, err := json.Marshal(upstreamErr)
	require.NoError(t, err)
	for _, secret := range []string{"fake.oauth-token_123", "fakeKey123456", "cHJpdmF0ZS1pbWFnZQ==", "fake-query-secret"} {
		require.NotContains(t, string(encoded), secret)
	}
	require.Contains(t, upstreamErr.Message, "Rejected Authorization: Bearer ***")
	require.Contains(t, upstreamErr.Message, "[image data omitted]")
	require.Equal(t, "invalid_image", upstreamErr.Code)
	require.Equal(t, "images", upstreamErr.Param)
}

func TestImagesOAuthUpstreamErrorPreservesBoundedDiagnosticText(t *testing.T) {
	message := strings.Repeat("权限", 1500)
	body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": "invalid_request_error", "code": "tool_not_available"}})
	require.NoError(t, err)
	upstreamErr := extractOpenAIImageUpstreamError(body)
	require.NotNil(t, upstreamErr)
	require.Equal(t, 2048, len([]rune(upstreamErr.Message)))
	require.Equal(t, "tool_not_available", upstreamErr.Code)
}
