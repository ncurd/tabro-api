package service

import (
	"bytes"
	"context"
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

func mappedImagesTestContext(t *testing.T, body []byte, accountType, response string) (*OpenAIGatewayService, *httpUpstreamRecorder, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{
		"api_key": "sk-test", "base_url": "https://proxy.example.com/v1", "access_token": "oauth-test", "chatgpt_account_id": "acct-42",
	}}
	return svc, upstream, c, rec, account
}

func TestImagesGenerationModelMappingPreservesRequestedAndUpstreamModels(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, tt := range []struct {
				name           string
				mapping        map[string]any
				channelDefault string
				wantModel      string
			}{
				{"account mapping", map[string]any{"gpt-image-2": "gpt-image-2.5-sunburst"}, "", "gpt-image-2.5-sunburst"},
				{"channel fallback", nil, "gpt-image-2.5-flare", "gpt-image-2.5-flare"},
				{"account overrides channel", map[string]any{"gpt-image-2": "gpt-image-2.5-sunburst"}, "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"},
				{"explicit identity overrides channel", map[string]any{"gpt-image-2": "gpt-image-2"}, "gpt-image-2.5-flare", "gpt-image-2"},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", accountType, stream, tt.name), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw a cat","n":1,"size":"1024x1024","quality":"medium","output_format":"png","stream":%t}`, stream))
					originalBody := bytes.Clone(body)
					response := `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`
					if accountType == AccountTypeOAuth {
						response = "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\"}]}}\n\n"
					} else if stream {
						response = "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"aW1hZ2U=\"}\n\ndata: [DONE]\n\n"
					}
					svc, upstream, c, rec, account := mappedImagesTestContext(t, body, accountType, response)
					account.Credentials["model_mapping"] = tt.mapping

					result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body, tt.channelDefault)

					require.NoError(t, err)
					require.NotNil(t, result)
					modelPath := "model"
					if accountType == AccountTypeOAuth {
						modelPath = "tools.0.model"
					}
					require.Equal(t, tt.wantModel, gjson.GetBytes(upstream.lastBody, modelPath).String())
					require.Equal(t, "gpt-image-2", result.Model)
					require.Equal(t, tt.wantModel, result.UpstreamModel)
					require.Equal(t, stream, result.Stream)
					require.Equal(t, 1, result.ImageCount)
					require.Equal(t, originalBody, body, "a failed account's mapping must not alter the request reused for failover")
					require.Equal(t, http.StatusOK, rec.Code)
				})
			}
		}
	}
}

func TestImagesGenerationModelMappingRetainsFrozenBillingProduct(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, accountMapping := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/accountMapping=%t", accountType, accountMapping), func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","n":1,"size":"1024x1024","quality":"medium"}`)
				svc, upstream, c, _, account := mappedImagesTestContext(t, body, accountType, "")
				channelDefault := "gpt-image-2.5-flare"
				if accountMapping {
					account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": "gpt-image-2.5-sunburst"}
					channelDefault = ""
				}
				ctx := bc.WithExecution(context.Background(), &bc.Execution{
					Mode: "central", ProductKey: "ai:gpt-image-2:images",
					Quote: bc.Quote{Request: bc.ReserveRequest{
						ServiceTier: "1024x1024:medium", MaximumUsage: map[string]bc.Decimal{"request_count": "1", "image_count": "1"},
					}},
				})
				require.NoError(t, ValidateBillingImageRequest(ctx, body))

				result, err := svc.ForwardImagesGenerations(ctx, c, account, body, channelDefault)

				require.ErrorContains(t, err, "image request differs from frozen billing quote")
				require.Nil(t, result)
				require.Nil(t, upstream.lastReq, "a mapping must not execute a product that billing did not authorize")
			})
		}
	}
}

func TestCodexImageGenerationMappingRetainsMainlineResponseModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`)
	svc, upstream, c, rec, account := mappedImagesTestContext(t, body, AccountTypeAPIKey, `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)

	result, err := svc.ForwardCodexImageGeneration(context.Background(), c, account, body, "gpt-image-2.5-flare")

	require.NoError(t, err)
	require.Equal(t, "gpt-image-2.5-flare", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "gpt-5.4", gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, "gpt-image-2.5-flare", result.UpstreamModel)
}

func TestImagesGenerationsURLNormalizesCompleteEditEndpoint(t *testing.T) {
	require.Equal(t, "https://proxy.example.com/v1/images/generations", buildOpenAIImagesGenerationsURL("https://proxy.example.com/v1/images/edits/"))
}
