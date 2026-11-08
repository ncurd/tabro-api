package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImageEditOAuthRetainsCurrentParameters(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"animate","images":[{"file_id":"file-source"}],"complexity":"low","safety_identifier":"hashed-user","frame_count":4,"frame_duration_ms":200,"output_format":"gif"}`)
	request, err := ParseOpenAIImageEditRequest(body, "application/json")
	require.NoError(t, err)
	oauthBody, err := request.oauthBody()
	require.NoError(t, err)
	for _, key := range []string{"complexity", "safety_identifier", "frame_count", "frame_duration_ms"} {
		require.Equal(t, gjson.GetBytes(body, key).Raw, gjson.GetBytes(oauthBody, key).Raw, key)
	}
	require.Equal(t, "file-source", gjson.GetBytes(oauthBody, "images.0.file_id").String())
	require.NotContains(t, string(request.MetadataBody), "file-source")
}

func TestOpenAIImageEditMarkupIsNotSilentlyDiscarded(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"edit the marked area","images":[{"file_id":"file-source"}],"markup":{"file_id":"file-markup"}}`)
	request, err := ParseOpenAIImageEditRequest(body, "application/json")
	require.NoError(t, err)
	forwardBody, _, err := request.upstreamBody()
	require.NoError(t, err)
	require.Equal(t, body, forwardBody, "native edits must preserve the markup reference")
	require.NotContains(t, string(request.MetadataBody), "file-markup")
	_, err = request.oauthBody()
	require.ErrorContains(t, err, "markup")
	require.ErrorContains(t, err, "API key")

	_, err = ParseOpenAIImageEditRequest([]byte(`{"prompt":"edit","images":[{"file_id":"file-source"}],"markup":{"image_url":12}}`), "application/json")
	require.ErrorContains(t, err, "markup")
}

func TestForwardImagesEditsSingleUploadCompatibility(t *testing.T) {
	const encodedPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
	imageBytes, err := base64.StdEncoding.DecodeString(encodedPNG)
	require.NoError(t, err)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"model", "gpt-image-2"}, {"prompt", "change the background"}, {"n", "1"}, {"size", "1024x1024"}, {"quality", "medium"}, {"output_format", "png"}} {
		require.NoError(t, w.WriteField(field[0], field[1]))
	}
	part, err := w.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write(imageBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	contentType := w.FormDataContentType()
	request, err := ParseOpenAIImageEditRequest(body.Bytes(), contentType)
	require.NoError(t, err)
	svc, upstream, c, rec, account := imageEditServiceContext(t, body.Bytes(), contentType, AccountTypeAPIKey, `{"created":1,"data":[{"b64_json":"`+encodedPNG+`"}]}`, 200)
	result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, body.Bytes(), upstream.lastBody)
	require.Equal(t, contentType, upstream.lastReq.Header.Get("Content-Type"))
	decoded, err := base64.StdEncoding.DecodeString(gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.NoError(t, err)
	imageConfig, err := png.DecodeConfig(bytes.NewReader(decoded))
	require.NoError(t, err)
	require.Equal(t, 1, imageConfig.Width)
	require.Equal(t, 1, imageConfig.Height)
}

func TestImagesEditAccountMappingTakesPrecedenceOverChannel(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mapping  map[string]any
		upstream string
	}{
		{"channel default", nil, "gpt-image-2.5-flare"},
		{"explicit account mapping", map[string]any{"client-image": "gpt-image-2.5-sunburst"}, "gpt-image-2.5-sunburst"},
		{"explicit identity mapping", map[string]any{"client-image": "client-image"}, "client-image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"client-image","prompt":"edit","n":1,"images":[{"file_id":"file-source"}]}`)
			request, err := ParseOpenAIImageEditRequest(body, "application/json")
			require.NoError(t, err)
			svc, upstream, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, `{"created":1,"data":[{"b64_json":"image"}]}`, 200)
			if tt.mapping != nil {
				account.Credentials["model_mapping"] = tt.mapping
			}
			result, err := svc.ForwardImagesEdits(context.Background(), c, account, request, "gpt-image-2.5-flare")
			require.NoError(t, err)
			require.Equal(t, tt.upstream, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "client-image", result.Model)
			require.Equal(t, tt.upstream, result.UpstreamModel)
			require.Equal(t, "client-image", request.Model, "retries must retain the original model")
			require.Equal(t, body, request.body)
		})
	}
}
