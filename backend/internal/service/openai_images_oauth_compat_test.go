package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesOAuthRequestUsesResponsesToolSchema(t *testing.T) {
	body := []byte(`{"prompt":"draw","model":"gpt-image-2","n":1,"style":"vivid","input_fidelity":"high","complexity":"low","frame_count":4,"frame_duration_ms":100,"svg":{"model":"author"},"safety_identifier":"hashed-user"}`)
	request, err := buildOpenAIImagesOAuthResponsesRequest(body, "gpt-image-2")
	require.NoError(t, err)
	for _, field := range []string{"n", "style", "input_fidelity", "safety_identifier"} {
		require.False(t, gjson.GetBytes(request, "tools.0."+field).Exists(), field)
	}
	require.Equal(t, "gpt-image-2", gjson.GetBytes(request, "tools.0.model").String())
	require.Equal(t, "hashed-user", gjson.GetBytes(request, "safety_identifier").String())
	require.Equal(t, "high", gjson.GetBytes(request, "tools.0.complexity").String(), "GPT Image 2 must avoid unsupported complexity inherited from its orchestration model")
	require.EqualValues(t, 4, gjson.GetBytes(request, "tools.0.frame_count").Int())
	require.EqualValues(t, 100, gjson.GetBytes(request, "tools.0.frame_duration_ms").Int())
	require.Equal(t, "author", gjson.GetBytes(request, "tools.0.svg.model").String())
	require.False(t, gjson.GetBytes(request, "parallel_tool_calls").Bool())
}

func TestImagesOAuthAcceptsCompletedJSONResponseAndRejectsFailure(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete", "in_progress", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"object":"response","status":%q,"created_at":1710000000,"output":[{"type":"image_generation_call","status":"completed","result":"aW1hZ2U="}],"usage":{"input_tokens":3,"output_tokens":5}}`, status))
			out, usage, count, err := buildOpenAIImagesOAuthClientResponse(body, []byte(`{"model":"gpt-image-2"}`))
			if status != "completed" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "aW1hZ2U=", gjson.GetBytes(out, "data.0.b64_json").String())
			require.EqualValues(t, 1710000000, gjson.GetBytes(out, "created").Int())
			require.Equal(t, 1, count)
			require.Equal(t, 3, usage.InputTokens)
		})
	}
	_, _, _, err := buildOpenAIImagesOAuthClientResponse([]byte(`data: {"type":"response.completed","response":{"status":"incomplete","output":[{"type":"image_generation_call","result":"image"}]}}

`), nil)
	require.Error(t, err)
}

type imagesOAuthSequenceUpstream struct {
	httpUpstreamRecorder
	t         *testing.T
	responses []string
	statuses  []int
	requests  [][]byte
}

func (u *imagesOAuthSequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.t.Helper()
	body, err := io.ReadAll(req.Body)
	require.NoError(u.t, err)
	u.requests = append(u.requests, body)
	require.LessOrEqual(u.t, len(u.requests), len(u.responses), "unexpected extra upstream generation")
	status := http.StatusOK
	if len(u.statuses) >= len(u.requests) {
		status = u.statuses[len(u.requests)-1]
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(u.responses[len(u.requests)-1]))}, nil
}

func TestImagesOAuthMultipleImagesUseSeparateCalls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw","n":2,"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			upstream := &imagesOAuthSequenceUpstream{t: t}
			for _, image := range []string{"first-image", "second-image"} {
				upstream.responses = append(upstream.responses, fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":%q}],\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"output_tokens_details\":{\"image_tokens\":4}}}}\n\ndata: [DONE]\n\n", image))
			}
			svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
			result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			for _, request := range upstream.requests {
				require.False(t, gjson.GetBytes(request, "tools.0.n").Exists())
			}
			require.Equal(t, 2, result.ImageCount)
			require.Equal(t, 6, result.Usage.InputTokens)
			require.Equal(t, 10, result.Usage.OutputTokens)
			require.Equal(t, 8, result.Usage.ImageOutputTokens)
			if stream {
				require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]"))
				require.Equal(t, 2, strings.Count(rec.Body.String(), `"type":"image_generation.completed"`))
				require.Contains(t, rec.Body.String(), `"image_index":0`)
				require.Contains(t, rec.Body.String(), `"image_index":1`)
				require.Contains(t, rec.Body.String(), `"input_tokens":6,"output_tokens":8,"total_tokens":14`)
				require.True(t, strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n"))
			} else {
				require.Len(t, gjson.Get(rec.Body.String(), "data").Array(), 2)
				require.Equal(t, "second-image", gjson.Get(rec.Body.String(), "data.1.b64_json").String())
			}
		})
	}
}

func TestImagesOAuthCentralMultipleImagesRejectedBeforeSending(t *testing.T) {
	upstream := &imagesOAuthSequenceUpstream{t: t}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	ctx := bc.WithExecution(context.Background(), &bc.Execution{Mode: "central"})
	_, _, err := svc.forwardImagesOAuth(ctx, nil, &Account{Type: AccountTypeOAuth}, []byte(`{"model":"gpt-image-2","prompt":"draw","n":2}`), true, false)
	require.ErrorContains(t, err, "require n=1")
	var requestErr *OpenAIImageRequestError
	require.ErrorAs(t, err, &requestErr)
	require.Empty(t, upstream.requests)
}

func TestImagesOAuthStreamFailureAfterFirstImageDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":2,"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	upstream := &imagesOAuthSequenceUpstream{
		t: t,
		responses: []string{
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"first-image\"}]}}\n\ndata: [DONE]\n\n",
			`{"error":{"message":"retry another account"}}`,
		},
		statuses: []int{http.StatusOK, http.StatusBadGateway},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, 1, result.ImageCount)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"image_generation.completed"`))
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"error"`))
	require.NotContains(t, rec.Body.String(), "data: [DONE]")
}

func TestImagesOAuthLimitsOutputToRequestedCount(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw","n":1,"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			upstream := &imagesOAuthSequenceUpstream{t: t, responses: []string{
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"first-image\"},{\"type\":\"image_generation_call\",\"result\":\"unexpected-extra-image\"}],\"usage\":{\"output_tokens\":5}}}\n\n",
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
			result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
			require.NoError(t, err)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "unexpected-extra-image")
		})
	}
}

func TestImagesOAuthStreamRejectsFailedImageItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	upstream := &imagesOAuthSequenceUpstream{t: t, responses: []string{
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"status\":\"failed\",\"result\":\"invalid-image\"}]}}\n\ndata: [DONE]\n\n",
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
	require.Error(t, err)
	require.Zero(t, result.ImageCount)
	require.NotContains(t, rec.Body.String(), "image_generation.completed")
	require.NotContains(t, rec.Body.String(), "data: [DONE]")
}

func TestImagesOAuthStreamAccumulatesUsageFromFailedLaterRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":2,"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	upstream := &imagesOAuthSequenceUpstream{t: t, responses: []string{
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"first-image\"}],\"usage\":{\"input_tokens\":3,\"output_tokens\":5}}}\n\ndata: [DONE]\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"second-image\"}]}}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":11}}}\n\n",
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
	require.Error(t, err)
	require.Equal(t, 2, result.ImageCount)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 16, result.Usage.OutputTokens)
	require.NotContains(t, rec.Body.String(), "data: [DONE]")
}

func TestImagesOAuthRejectsInvalidCountsBeforeSending(t *testing.T) {
	for _, n := range []string{"0", "-1", "11", "1.5", `"1"`} {
		t.Run(n, func(t *testing.T) {
			upstream := &imagesOAuthSequenceUpstream{t: t}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			_, _, err := svc.forwardImagesOAuth(context.Background(), nil, &Account{Type: AccountTypeOAuth}, []byte(`{"model":"gpt-image-2","prompt":"draw","n":`+n+`}`), true, false)
			var requestErr *OpenAIImageRequestError
			require.ErrorAs(t, err, &requestErr)
			require.Empty(t, upstream.requests)
		})
	}
}
