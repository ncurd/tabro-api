package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMediaInputURLAcceptsPublicAndInlineReferences(t *testing.T) {
	for _, value := range []string{
		"https://media.example.com/path/image.png?signature=a%2Bb%2Fc&expires=42",
		"http://media.example.com/image.png",
		"https://8.8.8.8/image.png",
		"data:image/png;base64,aW1hZ2U=",
	} {
		require.NoError(t, ValidateMediaInputURL(value, "image", true, false), value)
	}
	require.NoError(t, ValidateMediaInputURL("data:audio/mpeg;base64,YXVkaW8=", "audio", true, false))
	require.NoError(t, ValidateMediaInputURL("asset://source", "video", false, true))
	mediaType, size, err := validateMediaDataURL("data:audio/mpeg;base64,YXVkaW8=")
	require.NoError(t, err)
	require.Equal(t, "audio/mpeg", mediaType)
	require.EqualValues(t, 5, size)
}

func TestValidateMediaInputURLRejectsMalformedAndLocalInputs(t *testing.T) {
	for _, value := range []string{
		"", "/tmp/image.png", "file:///tmp/image.png", "ftp://media.example.com/image.png", "javascript:alert(1)",
		"aW1hZ2U=", "https://", "https://user:password@media.example.com/image.png", " https://media.example.com/image.png",
		"http://localhost/image.png", "http://example.local/image.png", "http://example.internal/image.png", "http://localhost./image.png",
		"http://127.0.0.1/image.png", "http://10.1.1.1/image.png", "http://169.254.169.254/image.png", "http://192.168.1.1/image.png",
		"http://[::1]/image.png", "http://[::ffff:127.0.0.1]/image.png", "http://127.1/image.png", "http://0177.0.0.1/image.png",
		"data:image/png,abc", "data:;base64,YQ==", "data:image/png;base64,", "data:image/png;base64,!!!",
		"data:image/png;base64,YQ", "data:image/png;base64,YR==", "data:image/png;base64,YQ==\n", "data:image/png;charset=utf-8;base64,YQ==",
		"data:audio/mpeg;base64,YXVkaW8=", "asset://source",
	} {
		require.Error(t, ValidateMediaInputURL(value, "image", true, false), value)
	}
	require.ErrorContains(t, ValidateMediaInputURL("data:video/mp4;base64,dmlkZW8=", "video", false, true), "public HTTP(S) URL")
	for _, value := range []string{"asset://", "asset://source/path", "asset://source?key=value", "asset://user:pass@source"} {
		require.Error(t, ValidateMediaInputURL(value, "video", false, true), value)
	}
}
