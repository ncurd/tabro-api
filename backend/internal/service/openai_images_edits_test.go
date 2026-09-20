package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func imageEditMultipart(t *testing.T, model string, stream bool) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"prompt", "edit both references"}, {"model", model}, {"n", "2"}, {"output_compression", "85"}, {"vendor_option", "preserved"}} {
		if field[1] != "" {
			require.NoError(t, w.WriteField(field[0], field[1]))
		}
	}
	if stream {
		require.NoError(t, w.WriteField("stream", "true"))
	}
	for _, file := range [][3]string{{"image[]", "first.png", "first\x00image\xff"}, {"image[]", "second.png", "second image"}, {"mask", "mask.png", "mask bytes"}} {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": file[0], "filename": file[1]}))
		header.Set("Content-Type", "image/png")
		header.Set("X-Part-Metadata", "preserved")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte(file[2]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

func imageEditServiceContext(t *testing.T, body []byte, contentType, accountType, response string, status int) (*OpenAIGatewayService, *httpUpstreamRecorder, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", contentType)
	c.Request.Header.Set("Authorization", "Bearer client-secret")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: status,
		Header:     http.Header{"X-Request-Id": {"edit-request"}},
		Body:       io.NopCloser(strings.NewReader(response)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://proxy.example.com/v1", "access_token": "oauth-test", "chatgpt_account_id": "acct-42"}}
	return svc, upstream, c, rec, account
}

func TestParseOpenAIImageEditRequestValidation(t *testing.T) {
	for _, body := range []string{
		`[]`,
		`{"prompt":"edit"}`,
		`{"prompt":"edit","images":[]}`,
		`{"prompt":"edit","images":[{}]}`,
		`{"prompt":"edit","images":[{"image_url":"https://example.com/image.png","file_id":"file-1"}]}`,
		`{"prompt":"edit","images":[{"image_url":1}]}`,
		`{"prompt":"edit","images":[{"file_id":"file-1"}],"mask":"mask.png"}`,
		`{"prompt":"edit","images":[{"file_id":"file-1"}],"stream":"true"}`,
		`{"prompt":1,"images":[{"file_id":"file-1"}]}`,
		`{"model":"first","model":"second","prompt":"edit","images":[{"file_id":"file-1"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, err := ParseOpenAIImageEditRequest([]byte(body), "application/json")
			require.Error(t, err)
		})
	}
	for _, contentType := range []string{"text/plain", "multipart/form-data", "multipart/form-data; boundary=bad"} {
		_, err := ParseOpenAIImageEditRequest([]byte(`{"prompt":"edit"}`), contentType)
		require.Error(t, err)
	}
	request, err := ParseOpenAIImageEditRequest([]byte(`{"prompt":"edit","images":[{"image_url":"data:image/png;base64,c2VjcmV0"}],"mask":{"file_id":"private-file"},"unknown":"data:image/png;base64,c2VjcmV0"}`), "application/json")
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", request.Model)
	require.NotContains(t, string(request.MetadataBody), "c2VjcmV0")
	require.NotContains(t, string(request.MetadataBody), "private-file")
	require.NotContains(t, string(request.MetadataBody), "images")
}

func TestForwardImagesEditsMultipartPreservationAndMapping(t *testing.T) {
	for _, mappedModel := range []string{"client-model", "upstream-model"} {
		t.Run(mappedModel, func(t *testing.T) {
			body, contentType := imageEditMultipart(t, "client-model", false)
			request, err := ParseOpenAIImageEditRequest(body, contentType)
			require.NoError(t, err)
			svc, upstream, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeAPIKey, `{"data":[{"b64_json":"a"},{"b64_json":"b"}],"usage":{"input_tokens":8,"output_tokens":15,"input_tokens_details":{"cached_tokens":2}}}`, 200)
			account.Credentials["model_mapping"] = map[string]any{"client-model": mappedModel}
			result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
			require.NoError(t, err)
			require.Equal(t, "/v1/images/edits", upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "client-model", result.Model)
			require.Equal(t, mappedModel, result.UpstreamModel)
			require.Equal(t, "client-model", request.Model, "account mapping must not mutate a request reused for failover")
			require.Equal(t, 2, result.ImageCount)
			require.Equal(t, 8, result.Usage.InputTokens)
			require.Equal(t, 15, result.Usage.ImageOutputTokens)
			require.Equal(t, 2, result.Usage.CacheReadInputTokens)
			require.Equal(t, http.StatusOK, rec.Code)
			if mappedModel == "client-model" {
				require.Equal(t, body, upstream.lastBody)
				require.Equal(t, contentType, upstream.lastReq.Header.Get("Content-Type"))
			}
			parsed, err := ParseOpenAIImageEditRequest(upstream.lastBody, upstream.lastReq.Header.Get("Content-Type"))
			require.NoError(t, err)
			require.Equal(t, mappedModel, parsed.Model)
			require.Len(t, parsed.parts, len(request.parts))
			for i, part := range request.parts {
				require.Equal(t, part.header, parsed.parts[i].header)
				if part.name != "model" {
					require.Equal(t, part.data, parsed.parts[i].data)
				}
			}
			opsBody, exists := c.Get(OpsUpstreamRequestBodyKey)
			require.True(t, exists)
			require.NotContains(t, string(opsBody.([]byte)), "mask bytes")
		})
	}
}

func TestForwardImagesEditsJSONAndFailures(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"https://example.com/image.png"},{"file_id":"file-1"}],"mask":{"file_id":"file-mask"},"vendor_option":{"enabled":true}}`)
	for _, status := range []int{200, 400, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			request, err := ParseOpenAIImageEditRequest(body, "application/json")
			require.NoError(t, err)
			svc, upstream, c, rec, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, `{"data":[{"b64_json":"ok"}]}`, status)
			_, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
			require.Equal(t, body, upstream.lastBody)
			if status == 200 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if status == 400 {
					require.Equal(t, status, rec.Code)
				} else {
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.False(t, c.Writer.Written())
				}
			}
		})
	}
	request, err := ParseOpenAIImageEditRequest(body, "application/json")
	require.NoError(t, err)
	svc, upstream, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, "", 200)
	upstream.err = errors.New("network failed")
	_, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.False(t, c.Writer.Written())
}

func TestForwardImagesEditsNativeStreamAccounting(t *testing.T) {
	body, contentType := imageEditMultipart(t, "gpt-image-2", true)
	request, err := ParseOpenAIImageEditRequest(body, contentType)
	require.NoError(t, err)
	response := "event: image_edit.partial_image\ndata: {\"type\":\"image_edit.partial_image\",\"b64_json\":\"partial\"}\n\n" +
		"event: image_edit.completed\ndata: {\"type\":\"image_edit.completed\",\"b64_json\":\"final\",\"usage\":{\"input_tokens\":8,\"output_tokens\":15}}\n\n"
	svc, upstream, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeAPIKey, response, 200)
	result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
	require.NoError(t, err)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount, "completed events take priority over requested n")
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Equal(t, 15, result.Usage.ImageOutputTokens)
	require.Contains(t, rec.Body.String(), response)
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
}

func TestForwardImagesEditsOAuthPreservesReferences(t *testing.T) {
	for _, multipartInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "multipart"}[multipartInput], func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2.5-sunburst","prompt":"edit","images":[{"image_url":"https://example.com/image.png"},{"file_id":"file-1"}],"mask":{"file_id":"file-mask"},"input_fidelity":"high","output_compression":85}`)
			contentType := "application/json"
			if multipartInput {
				body, contentType = imageEditMultipart(t, "gpt-image-2.5-sunburst", false)
			}
			request, err := ParseOpenAIImageEditRequest(body, contentType)
			require.NoError(t, err)
			response := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"final\"}],\"usage\":{\"input_tokens\":8,\"output_tokens\":15}}}\n\n"
			svc, upstream, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeOAuth, response, 200)
			result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
			require.NoError(t, err)
			require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
			require.Equal(t, "edit", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
			require.Equal(t, "gpt-image-2.5-sunburst", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
			require.Equal(t, int64(85), gjson.GetBytes(upstream.lastBody, "tools.0.output_compression").Int())
			require.Len(t, gjson.GetBytes(upstream.lastBody, "input.0.content").Array(), 3)
			if multipartInput {
				require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("first\x00image\xff")), gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
				require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("second image")), gjson.GetBytes(upstream.lastBody, "input.0.content.2.image_url").String())
				require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("mask bytes")), gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.image_url").String())
			} else {
				require.Equal(t, "https://example.com/image.png", gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
				require.Equal(t, "file-1", gjson.GetBytes(upstream.lastBody, "input.0.content.2.file_id").String())
				require.Equal(t, "file-mask", gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.file_id").String())
			}
			require.Equal(t, "final", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
			require.Equal(t, 1, result.ImageCount)
		})
	}
}

func TestForwardImagesEditsOAuthStreamEventTypes(t *testing.T) {
	body, contentType := imageEditMultipart(t, "gpt-image-2", true)
	request, err := ParseOpenAIImageEditRequest(body, contentType)
	require.NoError(t, err)
	response := "data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"partial\",\"partial_image_index\":0}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"final\"}],\"usage\":{\"input_tokens\":8,\"output_tokens\":15}}}\n\n"
	svc, _, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeOAuth, response, 200)
	result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
	require.NoError(t, err)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Contains(t, rec.Body.String(), `"type":"image_edit.partial_image"`)
	require.Contains(t, rec.Body.String(), `"type":"image_edit.completed"`)
	require.NotContains(t, rec.Body.String(), `"type":"image_generation.`)
}

func TestBuildOpenAIImagesEditsURL(t *testing.T) {
	for _, base := range []string{"https://proxy.example.com", "https://proxy.example.com/v1", "https://proxy.example.com/v1/", "https://proxy.example.com/v1/images/generations", "https://proxy.example.com/v1/images/edits"} {
		require.Equal(t, "https://proxy.example.com/v1/images/edits", buildOpenAIImagesEditsURL(base))
	}
}

func TestForwardImagesEditsOAuthFailuresDoNotBecomeSuccessfulImages(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.failed","response":{"error":{"message":"image tool failed"}}}`,
		`{"type":"response.incomplete","response":{}}`,
		`{"type":"response.cancelled","response":{}}`,
		`{"type":"error","message":"tool unavailable"}`,
		`{"type":"response.completed","response":{"output":[]}}`,
		`{"type":"response.image_generation_call.partial_image","partial_image_b64":"partial"}`,
	} {
		t.Run(gjson.Get(event, "type").String(), func(t *testing.T) {
			body, contentType := imageEditMultipart(t, "gpt-image-2", true)
			request, err := ParseOpenAIImageEditRequest(body, contentType)
			require.NoError(t, err)
			svc, _, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeOAuth, "data: "+event+"\n\n", 200)
			result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
			require.Error(t, err)
			require.Equal(t, 0, result.ImageCount)
			require.Contains(t, rec.Body.String(), `"type":"error"`)
			require.NotContains(t, rec.Body.String(), `"type":"image_edit.completed"`)
		})
	}
	// An output item before a terminal failure must not turn a buffered request
	// into a successful image response either.
	response := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"image\"}}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{}}\n\n"
	_, _, count, err := buildOpenAIImagesOAuthClientResponse([]byte(response), []byte(`{}`))
	require.Error(t, err)
	require.Zero(t, count)
}

func TestForwardImagesEditsNativeFailuresDoNotBecomeSuccessfulImages(t *testing.T) {
	for _, response := range []string{"data: [DONE]\n\n", "data: {\"type\":\"error\",\"message\":\"failed\"}\n\ndata: [DONE]\n\n"} {
		body, contentType := imageEditMultipart(t, "gpt-image-2", true)
		request, err := ParseOpenAIImageEditRequest(body, contentType)
		require.NoError(t, err)
		svc, _, c, _, account := imageEditServiceContext(t, body, contentType, AccountTypeAPIKey, response, 200)
		result, err := svc.ForwardImagesEdits(context.Background(), c, account, request)
		require.Error(t, err)
		require.Zero(t, result.ImageCount)
	}
}

func TestOpenAIImageBridgePreservesNativeReferenceContext(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5.6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"edit"},{"type":"input_image","file_id":"file-1"}]}],"tools":[{"type":"image_generation"}]}`,
		`{"model":"gpt-5.6-luna","input":"edit","tools":[{"type":"image_generation","action":"edit"}]}`,
		`{"model":"gpt-5.6-luna","input":"edit","tools":[{"type":"image_generation","input_image_mask":{"file_id":"mask"}}]}`,
		`{"model":"gpt-5.6-luna","input":"edit","previous_response_id":"resp-1","tools":[{"type":"image_generation"}]}`,
		`{"model":"gpt-5.6-luna","input":"edit","conversation":"conv-1","tools":[{"type":"image_generation"}]}`,
		`{"model":"gpt-5.6-luna","input":[{"type":"image_generation_call","id":"image-1","result":"base64"},{"type":"input_text","text":"edit"}],"tools":[{"type":"image_generation"}]}`,
	} {
		imagesBody, handled, err := BuildOpenAICodexImageGenerationRequest([]byte(body))
		require.NoError(t, err)
		require.False(t, handled)
		require.Nil(t, imagesBody)
	}
	_, handled, err := BuildOpenAICodexImageGenerationRequest([]byte(`{"model":"gpt-image-2","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"edit"},{"type":"input_image","file_id":"file-1"}]}]}`))
	require.True(t, handled)
	require.ErrorContains(t, err, "/images/edits")
}

func TestImageEditsJSONDataURLsPreservedAndValidated(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"data:image/png;base64,aW1hZ2U="},{"image_url":"https://media.example.com/ref.png?signature=abc%2Fdef"}],"mask":{"image_url":"data:image/png;base64,bWFzaw=="}}`)
	request, err := ParseOpenAIImageEditRequest(body, "application/json")
	require.NoError(t, err)
	svc, upstream, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, `{"data":[{"b64_json":"ok"}]}`, 200)
	_, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
	require.NoError(t, err)
	require.Equal(t, body, upstream.lastBody)
	oauth, err := request.oauthBody()
	require.NoError(t, err)
	require.Equal(t, "data:image/png;base64,aW1hZ2U=", gjson.GetBytes(oauth, "images.0.image_url").String())
	require.Equal(t, "data:image/png;base64,bWFzaw==", gjson.GetBytes(oauth, "mask.image_url").String())
	require.NotContains(t, string(request.MetadataBody), "base64")
	for _, input := range []string{
		`{"prompt":"edit","images":[{"image_url":"file:///tmp/image.png"}]}`,
		`{"prompt":"edit","images":[{"image_url":"aW1hZ2U="}]}`,
		`{"prompt":"edit","images":[{"image_url":"data:image/png;base64,!!"}]}`,
		`{"prompt":"edit","images":[{"image_url":"data:video/mp4;base64,YQ=="}]}`,
		`{"prompt":"edit","images":[{"image_url":"data:image/svg+xml;base64,YQ=="}]}`,
		`{"prompt":"edit","images":[{"image_url":"https://localhost/image.png"}]}`,
		`{"prompt":"edit","images":[{"image_url":"https://media.example.com/image.png"}],"mask":{"image_url":"data:audio/wav;base64,YQ=="}}`,
	} {
		_, err := ParseOpenAIImageEditRequest([]byte(input), "application/json")
		require.Error(t, err, input)
	}
}
