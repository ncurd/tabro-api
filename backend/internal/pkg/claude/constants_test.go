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

func TestDefaultModels_ContainsLatestClaudeModels(t *testing.T) {
	t.Parallel()

	want := map[string]Model{
		"claude-fable-5-1": {
			ID:          "claude-fable-5-1",
			Type:        "model",
			DisplayName: "Claude Fable 5.1",
			CreatedAt:   "2026-09-01T00:00:00Z",
		},
		"claude-opus-5": {
			ID:          "claude-opus-5",
			Type:        "model",
			DisplayName: "Claude Opus 5",
			CreatedAt:   "2026-07-24T00:00:00Z",
		},
	}

	indexes := make(map[string]int, len(DefaultModels))
	for i, model := range DefaultModels {
		indexes[model.ID] = i
		if expected, ok := want[model.ID]; ok && model != expected {
			t.Fatalf("unexpected metadata for %q: got %+v want %+v", model.ID, model, expected)
		}
	}

	for id := range want {
		if _, ok := indexes[id]; !ok {
			t.Fatalf("expected Claude default model list to contain %q", id)
		}
	}
	if indexes["claude-fable-5-1"] >= indexes["claude-fable-5"] {
		t.Fatal("expected claude-fable-5-1 before claude-fable-5")
	}
	if indexes["claude-opus-5"] >= indexes["claude-fable-5"] {
		t.Fatal("expected claude-opus-5 before older default models")
	}
	if indexes["claude-opus-5"] >= indexes["claude-opus-4-5-20251101"] {
		t.Fatal("expected claude-opus-5 before older Opus models")
	}
}
