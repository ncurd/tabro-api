package gemini

import "testing"

func TestDefaultModels_ContainsFallbackCatalogModels(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byName := make(map[string]Model, len(models))
	for _, model := range models {
		byName[model.Name] = model
	}

	required := []string{
		"models/gemini-2.5-flash-image",
		"models/gemini-3.1-pro-preview-customtools",
		"models/gemini-3.1-flash-image",
	}

	for _, name := range required {
		model, ok := byName[name]
		if !ok {
			t.Fatalf("expected fallback model %q to exist", name)
		}
		if len(model.SupportedGenerationMethods) == 0 {
			t.Fatalf("expected fallback model %q to advertise generation methods", name)
		}
	}
}

func TestHasFallbackModel_RecognizesCustomtoolsModel(t *testing.T) {
	t.Parallel()

	if !HasFallbackModel("gemini-3.1-pro-preview-customtools") {
		t.Fatalf("expected customtools model to exist in fallback catalog")
	}
	if !HasFallbackModel("models/gemini-3.1-pro-preview-customtools") {
		t.Fatalf("expected prefixed customtools model to exist in fallback catalog")
	}
	if HasFallbackModel("gemini-unknown") {
		t.Fatalf("did not expect unknown model to exist in fallback catalog")
	}
}

func TestHasFallbackModel_ExcludesRetiredModelAndRetainsSupportedAlias(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"gemini-2.0-flash", "models/gemini-2.0-flash"} {
		if HasFallbackModel(id) {
			t.Fatalf("retired model %q must not be offered in fallback models", id)
		}
	}
	// Google redirects this model ID to Gemini 3.1 Pro Preview.
	if !HasFallbackModel("gemini-3-pro-preview") {
		t.Fatal("expected the supported Gemini 3 Pro compatibility alias to remain available")
	}
}
