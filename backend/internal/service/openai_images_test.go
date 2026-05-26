package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIImagesGenerationsURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "root_base", base: "https://api.openai.com", want: "https://api.openai.com/v1/images/generations"},
		{name: "v1_base", base: "https://api.openai.com/v1", want: "https://api.openai.com/v1/images/generations"},
		{name: "endpoint_base", base: "https://api.openai.com/v1/images/generations", want: "https://api.openai.com/v1/images/generations"},
		{name: "trailing_slash", base: "https://proxy.example.com/v1/", want: "https://proxy.example.com/v1/images/generations"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, buildOpenAIImagesGenerationsURL(tt.base))
		})
	}
}

func TestOpenAIGatewayServiceForwardImagesGenerations_ForwardsToImagesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","n":2,"size":"1024x1024"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"created":1,"data":[{"b64_json":"a"},{"b64_json":"b"}]}`)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://proxy.example.com/v1"},
	}

	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://proxy.example.com/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.JSONEq(t, string(body), string(upstream.lastBody))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"created":1,"data":[{"b64_json":"a"},{"b64_json":"b"}]}`, rec.Body.String())
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, 2, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
}

func TestOpenAIGatewayServiceForwardImagesGenerations_StreamsAPIKeySSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1","prompt":"draw","stream":true,"partial_images":1,"size":"1024x1024"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"image_generation.partial_image","b64_json":"partial","partial_image_index":0,"size":"1024x1024","quality":"high","background":"opaque","output_format":"png"}`,
		``,
		`data: {"type":"image_generation.completed","b64_json":"final","size":"1024x1024","quality":"high","background":"opaque","output_format":"png","usage":{"input_tokens":2,"output_tokens":7,"total_tokens":9}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req_img_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Body.String(), ":\n\n"))
	require.Contains(t, rec.Body.String(), `"type":"image_generation.partial_image"`)
	require.Contains(t, rec.Body.String(), `"type":"image_generation.completed"`)
	require.Contains(t, rec.Body.String(), `data: [DONE]`)
	require.True(t, result.Stream)
	require.Equal(t, "req_img_stream", result.RequestID)
	require.Equal(t, "gpt-image-1", result.Model)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceForwardImagesGenerations_FailoverStatusDoesNotWriteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"try another account"}}`)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, rec.Body.String())
}

func TestOpenAIGatewayServiceForwardImagesGenerations_OAuthUsesCodexResponsesImageTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","n":2,"size":"1024x1024","quality":"high","output_format":"png"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_oauth_img"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":11,\"output_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"image_tokens\":7}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMQ==\",\"revised_prompt\":\"draw a cat 1\",\"output_format\":\"png\"},{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMg==\",\"revised_prompt\":\"draw a cat 2\",\"output_format\":\"png\"}]}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          43,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "acct_123"},
	}

	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "acct_123", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "gpt-5.4-mini", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstream.lastBody, "tools.0.n").Int())
	require.Equal(t, "draw a cat", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "aW1hZ2UtMQ==", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "aW1hZ2UtMg==", gjson.Get(rec.Body.String(), "data.1.b64_json").String())
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, 2, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 22, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceForwardImagesGenerations_OAuthStreamsImagesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"partial_images":1,"size":"1024x1024","quality":"high","output_format":"png"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_oauth_img_stream"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.image_generation_call.partial_image","partial_image_index":0,"partial_image_b64":"partial-b64","size":"1024x1024","quality":"high","output_format":"png"}`,
			``,
			`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"final-b64","revised_prompt":"draw a cat","output_format":"png"}}`,
			``,
			`data: {"type":"response.completed","response":{"created_at":1710000000,"usage":{"input_tokens":11,"output_tokens":22,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"image_tokens":7}}}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          43,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "acct_123"},
	}

	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "tools.0.partial_images").Int())
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Body.String(), ":\n\n"))
	require.Contains(t, rec.Body.String(), `"type":"image_generation.partial_image"`)
	require.Contains(t, rec.Body.String(), `"b64_json":"partial-b64"`)
	require.Contains(t, rec.Body.String(), `"type":"image_generation.completed"`)
	require.Contains(t, rec.Body.String(), `"b64_json":"final-b64"`)
	require.Contains(t, rec.Body.String(), `"output_tokens":7`)
	require.Contains(t, rec.Body.String(), `data: [DONE]`)
	require.True(t, result.Stream)
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 22, result.Usage.OutputTokens)
	require.Equal(t, 7, result.Usage.ImageOutputTokens)
}

func TestOpenAICodexImageBridgeBuildsImagesRequest(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": "生成海报",
		"tools": [{"type":"image_generation","size":"1024x1024","quality":"medium","output_format":"png"}]
	}`)

	imagesBody, ok, err := buildOpenAICodexImageGenerationRequest(body)

	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `{
		"model":"gpt-image-2",
		"prompt":"生成海报",
		"n":1,
		"size":"1024x1024",
		"quality":"medium",
		"output_format":"png"
	}`, string(imagesBody))
}

func TestOpenAIGatewayServiceForwardCodexImageGeneration_WrapsImagesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model": "gpt-5.4",
		"input": "生成海报",
		"tools": [{"type":"image_generation","size":"1024x1024","quality":"medium","output_format":"png"}]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"created":1,"data":[{"b64_json":"image-data"}]}`)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.ForwardCodexImageGeneration(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.openai.com/v1/images/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{
		"model":"gpt-image-2",
		"prompt":"生成海报",
		"n":1,
		"size":"1024x1024",
		"quality":"medium",
		"output_format":"png"
	}`, string(upstream.lastBody))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "gpt-5.4", gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "image_generation_call", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.Equal(t, "image-data", gjson.Get(rec.Body.String(), "output.0.result").String())
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
}

func TestOpenAIGatewayServiceForwardCodexImageGeneration_OAuthUsesCodexResponsesImageTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model": "gpt-5.4",
		"input": "生成海报",
		"tools": [{"type":"image_generation","size":"1024x1024","quality":"medium","output_format":"png"}]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_oauth_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000001,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtYnJpZGdl\",\"revised_prompt\":\"生成海报\",\"output_format\":\"png\"}]}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          44,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}

	result, err := svc.ForwardCodexImageGeneration(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "gpt-5.4-mini", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, "生成海报", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "gpt-5.4", gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "image_generation_call", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.Equal(t, "aW1hZ2UtYnJpZGdl", gjson.Get(rec.Body.String(), "output.0.result").String())
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "1K", result.ImageSize)
	require.Equal(t, 4, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceRecordUsage_ImageCountWritesUsageLog(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:  "img-req",
			Model:      "gpt-image-2",
			Duration:   10,
			ImageCount: 2,
			ImageSize:  "1K",
		},
		APIKey:  &APIKey{ID: 10, UserID: 1, GroupID: testInt64Ptr(20), Group: &Group{ID: 20, RateMultiplier: 1}},
		User:    &User{ID: 1, Balance: 100},
		Account: &Account{ID: 42, RateMultiplier: f64p(1)},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 2, usageRepo.lastLog.ImageCount)
	require.NotNil(t, usageRepo.lastLog.ImageSize)
	require.Equal(t, "1K", *usageRepo.lastLog.ImageSize)
	require.Equal(t, "gpt-image-2", usageRepo.lastLog.Model)
}

func testInt64Ptr(v int64) *int64 { return &v }
