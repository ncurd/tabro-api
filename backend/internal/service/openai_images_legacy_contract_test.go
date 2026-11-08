package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Reproduce the reported Codex rejection rather than assuming that a different
// complexity value is valid. This contract stub accepts legacy requests only
// when the adapter leaves both image complexity and parent effort unset.
type legacyImageComplexityUpstream struct {
	httpUpstreamRecorder
	calls int
}

func (u *legacyImageComplexityUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	u.lastReq = req
	var err error
	u.lastBody, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	complexity := gjson.GetBytes(u.lastBody, "tools.0.complexity")
	if !complexity.Exists() {
		complexity = gjson.GetBytes(u.lastBody, "reasoning.effort")
	}
	response := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"image_generation_call","status":"completed","result":"generated-image"}]}}`
	if complexity.Exists() {
		payload, _ := json.Marshal(map[string]any{
			"type": "response.failed",
			"response": map[string]any{"status": "failed", "error": map[string]string{
				"type": "invalid_request_error", "message": fmt.Sprintf("Complexity '%s' is not supported for this image model.", complexity.String()),
			}},
		})
		response = string(payload)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + response + "\n\ndata: [DONE]\n\n")),
	}, nil
}

func TestImagesOAuthLegacyContractDoesNotInventComplexity(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "gpt-image-2-2026-04-21"} {
		for _, operation := range []string{"generation", "json_edit", "multipart_edit"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", model, operation, stream), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","n":1,"quality":"low","size":"1024x1024","output_format":"png","stream":%t}`, model, stream))
					contentType := "application/json"
					if operation == "json_edit" {
						body = []byte(fmt.Sprintf(`{"model":%q,"prompt":"edit","n":1,"quality":"low","stream":%t,"images":[{"file_id":"file-reference"}]}`, model, stream))
					} else if operation == "multipart_edit" {
						var form bytes.Buffer
						writer := multipart.NewWriter(&form)
						for _, field := range [][2]string{{"model", model}, {"prompt", "edit"}, {"n", "1"}, {"quality", "low"}, {"stream", strconv.FormatBool(stream)}} {
							require.NoError(t, writer.WriteField(field[0], field[1]))
						}
						part, err := writer.CreateFormFile("image", "reference.png")
						require.NoError(t, err)
						_, err = part.Write([]byte("private-reference-image"))
						require.NoError(t, err)
						require.NoError(t, writer.Close())
						body, contentType = form.Bytes(), writer.FormDataContentType()
					}
					svc, _, c, rec, account := imageEditServiceContext(t, body, contentType, AccountTypeOAuth, "", http.StatusOK)
					upstream := &legacyImageComplexityUpstream{}
					svc.httpUpstream = upstream
					var result *OpenAIForwardResult
					var err error
					if operation == "generation" {
						result, err = svc.ForwardImagesGenerations(context.Background(), c, account, body)
					} else {
						request, parseErr := ParseOpenAIImageEditRequest(body, contentType)
						require.NoError(t, parseErr)
						result, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
					}
					require.NoError(t, err)
					require.Equal(t, 1, result.ImageCount)
					require.Equal(t, 1, upstream.calls, "parameter compatibility must not replay paid image creation")
					require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.complexity").Exists())
					require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
					require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "tools.0.quality").String())
					require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
					require.Contains(t, rec.Body.String(), "generated-image")
				})
			}
		}
	}
}
