package openai

import "testing"

func TestDefaultModels_ContainsLatestOpenAIModels(t *testing.T) {
	want := []string{
		"gpt-5.4-pro",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-image-1-mini",
		"gpt-image-1.5",
		"gpt-image-1.5-2025-12-16",
		"gpt-image-2",
		"gpt-image-2-2026-04-21",
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
}
