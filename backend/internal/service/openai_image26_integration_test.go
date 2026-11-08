package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const image26ReportedUsage = `{"input_tokens":1800,"input_tokens_details":{"text_tokens":600,"image_tokens":1200},"output_tokens":1000,"cached_tokens":300,"cached_tokens_details":{"cached_text_tokens":100,"cached_image_tokens":200}}`

func image26Usage(t *testing.T) OpenAIUsage {
	t.Helper()
	usage, ok := extractOpenAIImageUsageFromJSONBytes([]byte(`{"usage":` + image26ReportedUsage + `}`))
	require.True(t, ok)
	require.True(t, usage.ImageUsageComplete)
	return usage
}

func image26RecordFixture(t *testing.T, result *OpenAIForwardResult) (*OpenAIGatewayService, *OpenAIRecordUsageInput, *openAIRecordUsageLogRepoStub, *openAIRecordUsageUserRepoStub) {
	t.Helper()
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	users := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{}, nil)
	input := &OpenAIRecordUsageInput{
		Result: result,
		APIKey: &APIKey{ID: 10, UserID: 1, GroupID: testInt64Ptr(20), Group: &Group{ID: 20, RateMultiplier: 1.5}},
		User:   &User{ID: 1, Balance: 100}, Account: &Account{ID: 42, RateMultiplier: f64p(1)},
	}
	return svc, input, logs, users
}

func TestImage26NativeGenerationAndEditChargeReportedModalities(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, edit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/edit=%t", model, edit), func(t *testing.T) {
				refs := ""
				if edit {
					refs = `,"images":[{"file_id":"file-source"}]`
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","n":1,"size":"1024x1024","quality":"max","complexity":"low"%s}`, model, refs))
				response := `{"created":1,"data":[{"b64_json":"aW1hZ2U="}],"usage":` + image26ReportedUsage + `}`
				svc, _, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, response, 200)
				var result *OpenAIForwardResult
				var err error
				if edit {
					request, parseErr := ParseOpenAIImageEditRequest(body, "application/json")
					require.NoError(t, parseErr)
					result, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
				} else {
					result, err = svc.ForwardImagesGenerations(context.Background(), c, account, body)
				}
				require.NoError(t, err)
				require.NotNil(t, result.ImageUsage)
				require.True(t, result.ImageUsage.ImageUsageComplete)
				require.Equal(t, "1024x1024:max", result.ImageBillingTier)
				billing, input, logs, users := image26RecordFixture(t, result)
				require.NoError(t, billing.RecordUsage(context.Background(), input))
				require.InDelta(t, 0.041025, logs.lastLog.TotalCost, 1e-12)
				require.InDelta(t, 0.0615375, users.lastAmount, 1e-12)
				require.Equal(t, 1500, logs.lastLog.InputTokens)
				require.Equal(t, 300, logs.lastLog.CacheReadTokens)
				require.Equal(t, 1000, logs.lastLog.ImageOutputTokens)
				require.Equal(t, string(BillingModeToken), *logs.lastLog.BillingMode)
			})
		}
	}
}

func TestImage26RecordUsageAddsOrchestrationAndSVGWithoutDoubleReasoning(t *testing.T) {
	image := image26Usage(t)
	result := &OpenAIForwardResult{
		RequestID: "image26-svg", Model: "gpt-image-2.6-sunburst", ImageCount: 1, ImageSize: "1K",
		Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 5}, ImageUsage: &image, ImageOrchestrationModel: "gpt-5.6-luna",
		SVGUsage: []OpenAIImageSVGUsage{{Model: "gpt-6-sol", InputTokens: 20, OutputTokens: 10, ReasoningTokens: 4, TotalTokens: 30}},
	}
	svc, input, logs, users := image26RecordFixture(t, result)
	svc.channelService = newOpenAIWSTurnChannelService(20, Channel{ID: 30, Status: StatusActive})
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	// Image $0.041025 + orchestration $0.000008 + author $0.00014.
	require.InDelta(t, 0.041173, logs.lastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.0617595, users.lastAmount, 1e-12)
	require.Equal(t, 1530, logs.lastLog.InputTokens)
	require.Equal(t, 1015, logs.lastLog.OutputTokens)
	require.Equal(t, 300, logs.lastLog.CacheReadTokens)
	require.Equal(t, 1000, logs.lastLog.ImageOutputTokens)
	require.NotNil(t, logs.lastLog.AccountStatsCost)
	require.InDelta(t, 0.041173, *logs.lastLog.AccountStatsCost, 1e-12, "provider statistics also price each model independently")
}

func TestImage26MissingUsageCannotBecomeGeminiEstimateOrFreeUsage(t *testing.T) {
	for _, kind := range []string{"missing image usage", "missing svg author usage"} {
		t.Run(kind, func(t *testing.T) {
			result := &OpenAIForwardResult{RequestID: kind, Model: "gpt-image-2.6-flare", ImageCount: 1, ImageSize: "1K"}
			if kind == "missing svg author usage" {
				usage := image26Usage(t)
				result.Usage, result.ImageUsage, result.SVGUsageIncomplete = usage, &usage, true
			}
			svc, input, logs, users := image26RecordFixture(t, result)
			require.Error(t, svc.RecordUsage(context.Background(), input))
			require.Zero(t, logs.calls)
			require.Zero(t, users.deductCalls)
			zero := 0.0
			input.APIKey.Group.ImagePrice1K = &zero
			require.NoError(t, svc.RecordUsage(context.Background(), input), "an explicit per-image contract does not need token evidence")
			require.NotNil(t, logs.lastLog)
			require.Zero(t, logs.lastLog.ActualCost)
			require.Equal(t, string(BillingModeImage), *logs.lastLog.BillingMode)
		})
	}
}

func TestImage26ConfiguredQualityTierPreservesExplicitZero(t *testing.T) {
	zero, high := 0.0, 0.3
	resolved := &ResolvedPricing{Mode: BillingModeImage, DefaultPerRequestPrice: 0.4, DefaultPriceConfigured: true, RequestTiers: []PricingInterval{
		{TierLabel: "1024x1024:low", PerRequestPrice: &zero}, {TierLabel: "1024x1024:high", PerRequestPrice: &high},
	}}
	unit, ok := openAIImageConfiguredTierPrice(resolved, "1024x1024:low", "1K")
	require.True(t, ok)
	require.Zero(t, unit)
	unit, ok = openAIImageConfiguredTierPrice(resolved, "1024x1024:high", "1K")
	require.True(t, ok)
	require.Equal(t, high, unit)
	unit, ok = openAIImageConfiguredTierPrice(resolved, "1536x1024:medium", "1K")
	require.True(t, ok)
	require.Equal(t, 0.4, unit)
}

func TestImage26UsageRejectsMissingOrAmbiguousModalities(t *testing.T) {
	for _, usageJSON := range []string{
		`{"input_tokens":300,"output_tokens":50}`,
		`{"input_tokens":300,"output_tokens":50,"input_tokens_details":{"text_tokens":100,"image_tokens":200},"cached_tokens":30}`,
		`{"input_tokens":300,"output_tokens":50,"input_tokens_details":{"text_tokens":100,"image_tokens":201}}`,
		`{"input_tokens":300,"output_tokens":50,"input_tokens_details":{"text_tokens":100,"image_tokens":200},"cached_tokens":30,"cached_tokens_details":{"cached_text_tokens":10,"cached_image_tokens":30}}`,
	} {
		usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":` + usageJSON + `}`))
		require.True(t, ok)
		require.False(t, usage.ImageUsageComplete, usageJSON)
	}
}

func TestImage26NativeMultiImageStreamRequiresExplicitImagePriceBeforeSend(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"draw","n":2,"stream":true}`)
	svc, upstream, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeAPIKey, "", 200)
	_, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
	var requestErr *OpenAIImageRequestError
	require.ErrorAs(t, err, &requestErr)
	require.Nil(t, upstream.lastReq)
	zero := 0.0
	group := &Group{ID: 20, ImagePrice1K: &zero}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	require.NoError(t, svc.validateOpenAIImage26NativeStream(ctx, body), "an explicit free image-count price remains valid")
}

func TestImage26ResponsesBridgeRetainsNewParametersAndUsage(t *testing.T) {
	request := []byte(`{"model":"gpt-6-sol","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2.6-flare","complexity":"low","output_format":"gif","frame_count":4,"frame_duration_ms":200}]}`)
	imageBody, handled, err := BuildOpenAICodexImageGenerationRequest(request)
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, "gpt-image-2.6-flare", gjson.GetBytes(imageBody, "model").String())
	require.Equal(t, "low", gjson.GetBytes(imageBody, "complexity").String())
	require.EqualValues(t, 4, gjson.GetBytes(imageBody, "frame_count").Int())
	require.EqualValues(t, 200, gjson.GetBytes(imageBody, "frame_duration_ms").Int())
	svgRequest := []byte(`{"model":"gpt-6-sol","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2.6-sunburst","output_format":"svg","svg":{"model":"gpt-6-sol","reasoning":{"effort":"low"}}}]}`)
	svgBody, handled, err := BuildOpenAICodexImageGenerationRequest(svgRequest)
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(svgBody, "svg.model").String())
	response, err := buildOpenAICodexImageGenerationResponse(svgRequest, []byte(`{"data":[{"b64_json":"image"}],"usage":`+image26ReportedUsage+`,"svg_usage":{"model":"gpt-6-sol","input_tokens":20,"output_tokens":10,"reasoning_tokens":4,"total_tokens":30}}`))
	require.NoError(t, err)
	require.EqualValues(t, 1800, gjson.GetBytes(response, "tool_usage.image_gen.input_tokens").Int())
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(response, "output.0.svg_usage.model").String())
	require.False(t, gjson.GetBytes(response, "usage").Exists(), "no orchestration model was called by the direct Image API bridge")
}

func TestImage26ModalityParserDoesNotTurnTextOutputIntoImageUsage(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":300,"input_tokens_details":{"text_tokens":100,"image_tokens":200},"output_tokens":50}}`))
	require.True(t, ok)
	require.Equal(t, 200, usage.ImageInputTokens)
	require.Zero(t, usage.ImageOutputTokens)
}
