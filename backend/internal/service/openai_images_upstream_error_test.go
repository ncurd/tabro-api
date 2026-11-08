package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesOAuthUpstreamErrorFieldsReachClientAndOps(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.failed","response":{"status":"failed","error":{"message":"Image tool is unavailable for this account","type":"invalid_request_error","code":"tool_not_available","param":"tools"},"output":[{"type":"image_generation_call","result":"private-image"}]}}`,
		`{"type":"error","error":{"message":"Image tool is unavailable for this account","type":"invalid_request_error","code":"tool_not_available","param":"tools"},"image":"private-image"}`,
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", gjson.Get(event, "type").String(), stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"cat","stream":%t}`, stream))
				svc, _, c, rec, account := imageEditServiceContext(t, body, "application/json", AccountTypeOAuth, "data: "+event+"\n\n", http.StatusOK)
				result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
				var upstreamErr *OpenAIImageUpstreamError
				require.ErrorAs(t, err, &upstreamErr)
				require.Equal(t, "Image tool is unavailable for this account", upstreamErr.Message)
				require.Equal(t, "invalid_request_error", upstreamErr.Type)
				require.Equal(t, "tool_not_available", upstreamErr.Code)
				require.Equal(t, "tools", upstreamErr.Param)
				var failoverErr *UpstreamFailoverError
				require.NotErrorAs(t, err, &failoverErr)
				if result != nil {
					require.Zero(t, result.ImageCount)
				}
				require.Equal(t, http.StatusOK, c.GetInt(OpsUpstreamStatusCodeKey))
				require.Equal(t, upstreamErr.Message, c.GetString(OpsUpstreamErrorMessageKey))
				detail := c.GetString(OpsUpstreamErrorDetailKey)
				require.Equal(t, "tool_not_available", gjson.Get(detail, "error.code").String())
				require.NotContains(t, detail, "private-image")
				if stream {
					require.Contains(t, rec.Body.String(), `"code":"tool_not_available"`)
					require.Contains(t, rec.Body.String(), `"param":"tools"`)
					require.NotContains(t, rec.Body.String(), "private-image")
					require.NotContains(t, rec.Body.String(), "image_generation.completed")
				}
			})
		}
	}
}

func TestImagesOAuthUnknownFailureKeepsLocalFallback(t *testing.T) {
	_, _, count, err := buildOpenAIImagesOAuthClientResponse([]byte("data: {\"type\":\"response.failed\",\"response\":{}}\n\n"), []byte(`{}`))
	require.ErrorContains(t, err, "did not contain image results")
	var upstreamErr *OpenAIImageUpstreamError
	require.NotErrorAs(t, err, &upstreamErr)
	require.Zero(t, count)
	parsed := extractOpenAIImageUpstreamError([]byte(`{"type":"error","message":"Permission denied","code":"permission_denied","param":"model"}`))
	require.NotNil(t, parsed)
	require.Equal(t, "upstream_error", parsed.Type)
	require.Equal(t, "permission_denied", parsed.Code)
}

func TestImagesOAuthPartialBatchPreservesPublicErrorWithoutRetry(t *testing.T) {
	const errorFields = `{"message":"Image quota exhausted","type":"invalid_request_error","code":"image_quota_exhausted","param":"tools"}`
	for _, failure := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, "data: {\"type\":\"response.failed\",\"response\":{\"error\":" + errorFields + "}}\n\n"},
		{http.StatusForbidden, `{"error":` + errorFields + `}`},
		{http.StatusServiceUnavailable, `{"error":` + errorFields + `}`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", failure.status, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"cat","n":2,"stream":%t}`, stream))
				svc, _, c, rec, account := imageEditServiceContext(t, body, "application/json", AccountTypeOAuth, "", http.StatusOK)
				upstream := &imagesOAuthSequenceUpstream{
					t: t,
					responses: []string{
						"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"first-image\"}]}}\n\ndata: [DONE]\n\n",
						failure.body,
					},
					statuses: []int{http.StatusOK, failure.status},
				}
				svc.httpUpstream = upstream
				result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
				require.Error(t, err)
				require.Equal(t, 1, result.ImageCount)
				require.Len(t, upstream.requests, 2)
				var publicErr *OpenAIImageUpstreamError
				require.ErrorAs(t, err, &publicErr)
				require.Equal(t, "Image quota exhausted", publicErr.Message)
				require.Equal(t, "image_quota_exhausted", publicErr.Code)
				require.Equal(t, "tools", publicErr.Param)
				var failoverErr *UpstreamFailoverError
				require.NotErrorAs(t, err, &failoverErr, "a completed image must prevent retrying the whole batch")
				if stream {
					require.Contains(t, rec.Body.String(), `"code":"image_quota_exhausted"`)
					require.Contains(t, rec.Body.String(), `"param":"tools"`)
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"image_generation.completed"`))
					require.NotContains(t, rec.Body.String(), "data: [DONE]")
				}
			})
		}
	}
}
