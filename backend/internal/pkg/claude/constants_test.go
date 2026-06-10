package claude

import "testing"

func TestDefaultModels_ContainsClaudeFable5(t *testing.T) {
	t.Parallel()

	foundID := false
	for _, id := range DefaultModelIDs() {
		if id == "claude-fable-5" {
			foundID = true
			break
		}
	}
	if !foundID {
		t.Fatal("expected claude-fable-5 in DefaultModelIDs")
	}

	for _, model := range DefaultModels {
		if model.ID == "claude-fable-5" {
			if model.DisplayName != "Claude Fable 5" {
				t.Fatalf("unexpected display name: %q", model.DisplayName)
			}
			return
		}
	}

	t.Fatal("expected claude-fable-5 in DefaultModels")
}
