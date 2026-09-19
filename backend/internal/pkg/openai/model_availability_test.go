package openai

import "testing"

func TestModelsForAccount_RespectsRetirementScope(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		ids := map[string]bool{}
		for _, model := range ModelsForAccount(oauth) {
			ids[model.ID] = true
		}
		for _, retired := range []string{"gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini", "gpt-5.2-codex"} {
			if ids[retired] {
				t.Errorf("oauth=%v advertises retired API model %s", oauth, retired)
			}
		}
		for _, retiredOnOAuth := range []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.2", "gpt-5.3-codex", "gpt-5.3-codex-spark"} {
			if ids[retiredOnOAuth] == oauth {
				t.Errorf("oauth=%v has incorrect availability for %s", oauth, retiredOnOAuth)
			}
		}
		for _, current := range []string{"gpt-6-astra", "gpt-5.6-luna", "gpt-5.5", "gpt-image-1.5", "gpt-image-2.5-flare", DefaultTestModel} {
			if !ids[current] {
				t.Errorf("oauth=%v lost currently available model %s", oauth, current)
			}
		}
	}
}

func TestIsRetiredModel_KeepsFutureShutdownsAndCustomIDs(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-image-1-mini", "gpt-image-1.5", "gpt-3.5-turbo", "o1", "gpt-realtime-mini", "custom-model"} {
		if IsRetiredModel(model, false) {
			t.Errorf("model %s has not shut down on the API", model)
		}
	}
	if !IsRetiredModel(" GPT-5.4-MINI ", true) || IsRetiredModel("gpt-5.4-mini", false) {
		t.Fatal("GPT-5.4 mini retirement must be scoped to ChatGPT accounts")
	}
	for _, model := range []string{"gpt-5.4-low", "gpt-5.3-codex-spark-high", "gpt-5.3-high", "gpt-5.4-mini-xhigh", "openai/gpt-5.4-low"} {
		if !IsRetiredModel(model, true) {
			t.Errorf("reasoning alias %s resolves to a retired Codex model", model)
		}
	}
	if IsRetiredModel("custom-model-high", true) {
		t.Fatal("custom model reasoning suffix must not imply retirement")
	}
}
