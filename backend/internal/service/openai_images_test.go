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
