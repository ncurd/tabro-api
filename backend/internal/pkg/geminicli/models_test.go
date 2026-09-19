package geminicli

import "testing"

func TestDefaultModels_ContainsImageModels(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		byID[model.ID] = model
	}

	required := []string{
		"gemini-2.5-flash-image",
		"gemini-3.1-flash-image",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected curated Gemini model %q to exist", id)
		}
	}
}

func TestDefaultModels_ExcludesRetiredModelAndIncludesDefault(t *testing.T) {
	t.Parallel()

	foundDefault := false
	for _, model := range DefaultModels {
		if model.ID == "gemini-2.0-flash" {
			t.Fatal("retired Gemini 2.0 Flash must not be offered for account tests")
		}
		if model.ID == DefaultTestModel {
			foundDefault = true
		}
	}
	if DefaultTestModel != "gemini-2.5-flash" || !foundDefault {
		t.Fatalf("expected an available Gemini 2.5 Flash test default, got %q", DefaultTestModel)
	}
}
