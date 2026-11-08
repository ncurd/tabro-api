package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func imageOAuthUsageResponse(t *testing.T, id string, includeImageUsage bool) string {
	t.Helper()
	response := map[string]any{
		"object": "response", "status": "completed",
		"output": []map[string]any{{"id": id, "type": "image_generation_call", "status": "completed", "result": id}},
		"usage":  map[string]any{"input_tokens": 100, "output_tokens": 200},
	}
	if includeImageUsage {
		response["tool_usage"] = map[string]any{"image_gen": map[string]any{
			"input_tokens": 30, "output_tokens": 40, "total_tokens": 70,
			"input_tokens_details":  map[string]int{"text_tokens": 10, "image_tokens": 20},
			"output_tokens_details": map[string]int{"text_tokens": 0, "image_tokens": 40},
			"cached_tokens":         5, "cached_tokens_details": map[string]int{"cached_text_tokens": 2, "cached_image_tokens": 3},
		}}
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	return string(encoded)
}

func forwardOAuthUsageFixture(t *testing.T, stream bool, responses ...string) (*OpenAIForwardResult, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := []byte(fmt.Sprintf(`{"model":"gpt-image-2.6-flare","prompt":"draw","n":%d,"stream":%t}`, len(responses), stream))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	upstream := &imagesOAuthSequenceUpstream{t: t}
	for _, response := range responses {
		if strings.HasPrefix(response, "data:") {
			upstream.responses = append(upstream.responses, response)
		} else {
			upstream.responses = append(upstream.responses, "data: {\"type\":\"response.completed\",\"response\":"+response+"}\n\ndata: [DONE]\n\n")
		}
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
	result, err := svc.ForwardImagesGenerations(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Len(t, upstream.requests, len(responses))
	return result, rec
}

func TestImagesOAuthClientUsageComesFromImageTool(t *testing.T) {
	body := []byte(`{"object":"response","status":"completed","output":[{"type":"image_generation_call","id":"ig_1","status":"completed","result":"image"}],"usage":{"input_tokens":100,"output_tokens":200},"tool_usage":{"image_gen":{"input_tokens":30,"input_tokens_details":{"text_tokens":10,"image_tokens":20},"output_tokens":40,"output_tokens_details":{"image_tokens":40,"text_tokens":0},"total_tokens":70,"cached_tokens":5,"cached_tokens_details":{"cached_text_tokens":2,"cached_image_tokens":3}}}}`)
	out, baseUsage, _, err := buildOpenAIImagesOAuthClientResponse(body, []byte(`{"model":"gpt-image-2.6-flare"}`))
	require.NoError(t, err)
	require.Equal(t, 100, baseUsage.InputTokens)
	require.Equal(t, 200, baseUsage.OutputTokens)
	require.EqualValues(t, 30, gjson.GetBytes(out, "usage.input_tokens").Int())
	require.EqualValues(t, 40, gjson.GetBytes(out, "usage.output_tokens").Int())
	require.EqualValues(t, 20, gjson.GetBytes(out, "usage.input_tokens_details.image_tokens").Int())
}

func TestImagesOAuthKeepsBaseAndImageUsageSeparate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			result, rec := forwardOAuthUsageFixture(t, stream, imageOAuthUsageResponse(t, "ig_1", true))
			require.Equal(t, 100, result.Usage.InputTokens)
			require.Equal(t, 200, result.Usage.OutputTokens)
			require.Equal(t, openAIImagesResponsesMainModel, result.ImageOrchestrationModel)
			require.Equal(t, "auto:auto", result.ImageBillingTier)
			require.NotNil(t, result.ImageUsage)
			require.Equal(t, 30, result.ImageUsage.InputTokens)
			require.Equal(t, 40, result.ImageUsage.ImageOutputTokens)
			require.Equal(t, 20, result.ImageUsage.ImageInputTokens)
			require.Equal(t, 5, result.ImageUsage.CacheReadInputTokens)
			require.Equal(t, 3, result.ImageUsage.CacheReadImageTokens)
			require.True(t, result.ImageUsage.ImageUsageComplete)
			require.Contains(t, rec.Body.String(), `"input_tokens":30`)
			require.NotContains(t, rec.Body.String(), `"input_tokens":100`)
		})
	}
}

func TestImagesOAuthAggregatesIndependentImageUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			result, rec := forwardOAuthUsageFixture(t, stream, imageOAuthUsageResponse(t, "ig_1", true), imageOAuthUsageResponse(t, "ig_2", true))
			require.Equal(t, 200, result.Usage.InputTokens)
			require.Equal(t, 400, result.Usage.OutputTokens)
			require.Equal(t, 60, result.ImageUsage.InputTokens)
			require.Equal(t, 80, result.ImageUsage.OutputTokens)
			require.Equal(t, 40, result.ImageUsage.ImageInputTokens)
			require.Equal(t, 10, result.ImageUsage.CacheReadInputTokens)
			require.Equal(t, 6, result.ImageUsage.CacheReadImageTokens)
			require.True(t, result.ImageUsage.ImageUsageComplete)
			require.Contains(t, rec.Body.String(), `"input_tokens":60`)
		})
	}
}

func TestImagesOAuthMissingToolUsageStaysUnknown(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			result, rec := forwardOAuthUsageFixture(t, stream, imageOAuthUsageResponse(t, "ig_1", false))
			require.Nil(t, result.ImageUsage)
			require.Equal(t, 100, result.Usage.InputTokens)
			require.NotContains(t, rec.Body.String(), `"usage"`)
			for _, firstPresent := range []bool{false, true} {
				result, _ = forwardOAuthUsageFixture(t, stream, imageOAuthUsageResponse(t, "ig_1", firstPresent), imageOAuthUsageResponse(t, "ig_2", !firstPresent))
				require.NotNil(t, result.ImageUsage)
				require.Equal(t, 30, result.ImageUsage.InputTokens)
				require.False(t, result.ImageUsage.ImageUsageComplete)
			}
		})
	}
}

func TestImagesOAuthDeduplicatesSVGUsageFromRepeatedSSEItems(t *testing.T) {
	item := `{"id":"ig_svg","type":"image_generation_call","status":"completed","output_format":"svg","result":"svg-image","svg_usage":{"model":"gpt-6-luna","input_tokens":2,"output_tokens":3,"reasoning_tokens":1,"total_tokens":5}}`
	response := `{"object":"response","status":"completed","output":[` + item + `],"usage":{"input_tokens":100,"output_tokens":200}}`
	body := []byte("data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\ndata: {\"type\":\"response.done\",\"response\":" + response + "}\n\n")
	_, usage, count, state, err := buildOpenAIImagesOAuthClientResponseWithUsage(body, []byte(`{"model":"gpt-image-2.6-flare"}`))
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 100, usage.InputTokens)
	require.Len(t, state.SVGUsage, 1)
	require.Equal(t, "gpt-6-luna", state.SVGUsage[0].Model)
	require.Equal(t, 3, state.SVGUsage[0].OutputTokens)
	require.False(t, state.SVGUsageIncomplete)
	streamResult, _ := forwardOAuthUsageFixture(t, true, string(body))
	require.Len(t, streamResult.SVGUsage, 1)
	require.Equal(t, 100, streamResult.Usage.InputTokens)
}

func TestImagesOAuthStreamingResultPreservesSVGUsage(t *testing.T) {
	response := imageOAuthUsageResponse(t, "ig_svg", true)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(response), &payload))
	item := payload["output"].([]any)[0].(map[string]any)
	item["svg_usage"] = map[string]any{"model": "gpt-6-luna", "input_tokens": 2, "output_tokens": 3, "reasoning_tokens": 1, "total_tokens": 5}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	result, rec := forwardOAuthUsageFixture(t, true, string(encoded))
	require.Len(t, result.SVGUsage, 1)
	require.Equal(t, "gpt-6-luna", result.SVGUsage[0].Model)
	require.False(t, result.SVGUsageIncomplete)
	require.Contains(t, rec.Body.String(), `"svg_usage"`)
}

func TestImagesOAuthFlagsMissingOrInvalidSVGUsage(t *testing.T) {
	for _, value := range []string{"", `,"svg_usage":{"model":"gpt-6-luna","input_tokens":2,"output_tokens":3,"reasoning_tokens":1,"total_tokens":99}`} {
		response := `{"object":"response","status":"completed","output":[{"id":"ig_svg","type":"image_generation_call","status":"completed","output_format":"svg","result":"image"` + value + `}]}`
		for _, stream := range []bool{false, true} {
			result, _ := forwardOAuthUsageFixture(t, stream, response)
			require.Empty(t, result.SVGUsage)
			require.True(t, result.SVGUsageIncomplete)
		}
	}
}

func TestImagesOAuthLaterSVGUsageCompletesEarlierSnapshot(t *testing.T) {
	item := `{"id":"ig_svg","type":"image_generation_call","status":"completed","output_format":"svg","result":"image"}`
	later := strings.TrimSuffix(item, "}") + `,"svg_usage":{"model":"gpt-6-luna","input_tokens":2,"output_tokens":3,"reasoning_tokens":1,"total_tokens":5}}`
	body := []byte("data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[" + later + "]}}\n\n")
	_, _, _, state, err := buildOpenAIImagesOAuthClientResponseWithUsage(body, nil)
	require.NoError(t, err)
	result := &OpenAIForwardResult{ImageCount: 1}
	state.apply(result, nil)
	require.Len(t, result.SVGUsage, 1)
	require.False(t, result.SVGUsageIncomplete)
}

func TestImagesOAuthMergesSVGAuthorUsageWithoutCombiningModels(t *testing.T) {
	first := &OpenAIForwardResult{ImageCount: 1, SVGUsage: []OpenAIImageSVGUsage{{Model: "gpt-6-luna", InputTokens: 2, OutputTokens: 3, ReasoningTokens: 1, TotalTokens: 5}}}
	second := &OpenAIForwardResult{ImageCount: 1, SVGUsage: []OpenAIImageSVGUsage{{Model: "gpt-6-sol", InputTokens: 7, OutputTokens: 11, ReasoningTokens: 5, TotalTokens: 18}}, SVGUsageIncomplete: true}
	mergeOpenAIImagesOAuthResult(first, second)
	require.Equal(t, 2, first.ImageCount)
	require.Len(t, first.SVGUsage, 2)
	require.Equal(t, "gpt-6-luna", first.SVGUsage[0].Model)
	require.Equal(t, "gpt-6-sol", first.SVGUsage[1].Model)
	require.True(t, first.SVGUsageIncomplete)
}

func TestImagesOAuthZeroToolFamilyCountsDoNotProveFreeRendering(t *testing.T) {
	response := `{"object":"response","status":"completed","output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"image"}],"usage":{"input_tokens":100,"output_tokens":200},"tool_usage":{"image_gen":{"input_tokens":0,"input_tokens_details":{"text_tokens":0,"image_tokens":0},"output_tokens":0,"total_tokens":0}}}`
	for _, stream := range []bool{false, true} {
		result, _ := forwardOAuthUsageFixture(t, stream, response)
		require.NotNil(t, result.ImageUsage)
		require.False(t, result.ImageUsage.ImageUsageComplete)
		require.Zero(t, result.ImageUsage.OutputTokens)
	}
}
