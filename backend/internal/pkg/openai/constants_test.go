package openai

import "testing"

func TestDefaultModels_ContainsLatestOpenAIModels(t *testing.T) {
	want := []string{
		"gpt-6-astra",
		"gpt-5.6",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.4-pro",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-image-1-mini",
		"gpt-image-1.5",
		"gpt-image-1.5-2025-12-16",
		"gpt-image-2",
		"gpt-image-2-2026-04-21",
		"gpt-image-2.5-sunburst",
		"gpt-image-2.5-sunburst-2026-09-08",
		"gpt-image-2.5-flare",
		"gpt-image-2.5-flare-2026-09-08",
		"gpt-realtime-1.5",
		"gpt-realtime-2",
		"gpt-realtime-mini",
		"gpt-realtime-translate",
	}

	models := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		models[model.ID] = model
	}

	for _, id := range want {
		if _, ok := models[id]; !ok {
			t.Fatalf("expected OpenAI default model list to contain %q", id)
		}
	}

	latest := DefaultModels[0]
	if latest.ID != "gpt-6-astra" {
		t.Fatalf("expected gpt-6-astra to be the first default model, got %q", latest.ID)
	}
	if latest.Created != 1788393600 || latest.DisplayName != "GPT-6 Astra" {
		t.Fatalf("unexpected gpt-6-astra metadata: %+v", latest)
	}
}
