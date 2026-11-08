package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesOAuthGPTImage2AvoidsInheritedMediumComplexity(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "gpt-image-2-2026-04-21"} {
		for _, complexity := range []string{"", "low", "medium", "high"} {
			t.Run(model+"/"+complexity, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","quality":"low"}`, model))
				if complexity != "" {
					body = []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","quality":"low","complexity":%q}`, model, complexity))
				}
				request, err := buildOpenAIImagesOAuthResponsesRequest(body, model)
				require.NoError(t, err)
				require.Equal(t, "medium", gjson.GetBytes(request, "reasoning.effort").String())
				require.Equal(t, "high", gjson.GetBytes(request, "tools.0.complexity").String())
				require.Equal(t, "low", gjson.GetBytes(request, "tools.0.quality").String())
				require.Equal(t, model, gjson.GetBytes(request, "tools.0.model").String())
			})
		}
	}
}

func TestImagesOAuthGPTImage26KeepsComplexityIndependentOfQuality(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, complexity := range []string{"", "low", "medium", "high"} {
			t.Run(model+"/"+complexity, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","quality":"low","complexity":%q}`, model, complexity))
				request, err := buildOpenAIImagesOAuthResponsesRequest(body, model)
				require.NoError(t, err)
				require.Equal(t, complexity, gjson.GetBytes(request, "tools.0.complexity").String())
				if complexity == "" {
					require.False(t, gjson.GetBytes(request, "tools.0.complexity").Exists())
				}
				require.Equal(t, "low", gjson.GetBytes(request, "tools.0.quality").String())
			})
		}
	}
}

func TestImagesOAuthComplexityUsesEffectiveModelForGenerationsAndEdits(t *testing.T) {
	response := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"image\"}]}}\n\n"
	for _, edit := range []bool{false, true} {
		for _, models := range []struct{ requested, mapped, complexity string }{
			{"gpt-image-2", "gpt-image-2", "high"},
			{"gpt-image-2.6-flare", "gpt-image-2", "high"},
			{"gpt-image-2", "gpt-image-2.6-flare", "medium"},
		} {
			t.Run(fmt.Sprintf("edit=%t/%s_to_%s", edit, models.requested, models.mapped), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","quality":"low","complexity":"medium"}`, models.requested))
				if edit {
					body = []byte(fmt.Sprintf(`{"model":%q,"prompt":"edit","quality":"low","complexity":"medium","images":[{"file_id":"file-reference"}]}`, models.requested))
				}
				svc, upstream, c, _, account := imageEditServiceContext(t, body, "application/json", AccountTypeOAuth, response, http.StatusOK)
				account.Credentials["model_mapping"] = map[string]any{models.requested: models.mapped}
				var err error
				if edit {
					request, parseErr := ParseOpenAIImageEditRequest(body, "application/json")
					require.NoError(t, parseErr)
					_, err = svc.ForwardImagesEdits(context.Background(), c, account, request)
				} else {
					_, err = svc.ForwardImagesGenerations(context.Background(), c, account, body)
				}
				require.NoError(t, err)
				require.Equal(t, models.mapped, gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
				require.Equal(t, models.complexity, gjson.GetBytes(upstream.lastBody, "tools.0.complexity").String())
				require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "tools.0.quality").String())
			})
		}
	}
}
