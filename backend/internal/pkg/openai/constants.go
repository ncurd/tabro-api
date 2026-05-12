// Package openai provides helpers and types for OpenAI API integration.
package openai

import _ "embed"

// Model represents an OpenAI model
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// DefaultModels OpenAI models list
var DefaultModels = []Model{
	{ID: "gpt-5.5", Object: "model", Created: 1776902400, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5"},
	{ID: "gpt-5.5-pro", Object: "model", Created: 1776902400, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5 Pro"},
	{ID: "gpt-5.4", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4"},
	{ID: "gpt-5.4-pro", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Pro"},
	{ID: "gpt-5.4-mini", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Mini"},
	{ID: "gpt-5.4-nano", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Nano"},
	{ID: "gpt-5.3-codex", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex"},
	{ID: "gpt-5.3-codex-spark", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex Spark"},
	{ID: "gpt-5.2", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.2"},
	{ID: "gpt-5.2-codex", Object: "model", Created: 1733011200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.2 Codex"},
	{ID: "gpt-5.1-codex-max", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex Max"},
	{ID: "gpt-5.1-codex", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex"},
	{ID: "gpt-5.1", Object: "model", Created: 1731456000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1"},
	{ID: "gpt-5.1-codex-mini", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex Mini"},
	{ID: "gpt-5", Object: "model", Created: 1722988800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5"},
	{ID: "gpt-image-2", Object: "model", Created: 1776729600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2"},
	{ID: "gpt-image-2-2026-04-21", Object: "model", Created: 1776729600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2 (2026-04-21)"},
	{ID: "gpt-image-1.5", Object: "model", Created: 1765843200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5"},
	{ID: "gpt-image-1.5-2025-12-16", Object: "model", Created: 1765843200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5 (2025-12-16)"},
	{ID: "gpt-image-1-mini", Object: "model", Created: 1765843200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1 Mini"},
	{ID: "gpt-realtime-2", Object: "model", Created: 1776729600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Realtime 2"},
	{ID: "gpt-realtime-1.5", Object: "model", Created: 1765843200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Realtime 1.5"},
	{ID: "gpt-realtime-mini", Object: "model", Created: 1765843200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Realtime Mini"},
	{ID: "gpt-realtime-translate", Object: "model", Created: 1776729600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Realtime Translate"},
}

// DefaultModelIDs returns the default model ID list
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel default model for testing OpenAI accounts
const DefaultTestModel = "gpt-5.1-codex"

// DefaultInstructions default instructions for non-Codex CLI requests
// Content loaded from instructions.txt at compile time
//
//go:embed instructions.txt
var DefaultInstructions string
