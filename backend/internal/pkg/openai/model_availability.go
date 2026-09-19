package openai

import "strings"

// IsRetiredModel applies the published shutdowns as of 2026-09-19.
// API and ChatGPT-authenticated Codex have different model lifecycles:
// https://developers.openai.com/api/docs/deprecations
// https://learn.chatgpt.com/docs/models#deprecated-codex-models
// Keep future shutdowns and historical pricing out of this catalog filter.
func IsRetiredModel(model string, chatGPTAccount bool) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if chatGPTAccount {
		// Codex also accepts provider-prefixed model names.
		if index := strings.LastIndex(model, "/"); index >= 0 {
			model = model[index+1:]
		}
		// The gateway accepts these reasoning aliases before forwarding to Codex.
		for _, suffix := range []string{"-none", "-low", "-medium", "-high", "-xhigh", "-max"} {
			if strings.HasSuffix(model, suffix) {
				base := strings.TrimSuffix(model, suffix)
				if IsRetiredModel(base, true) {
					return true
				}
			}
		}
	}
	switch model {
	case "gpt-4-turbo-preview", "gpt-4-0125-preview", "gpt-4-0314",
		"gpt-4.5-preview", "gpt-4.5-preview-2025-02-27",
		"o1-preview", "o1-preview-2024-09-12", "o1-mini", "o1-mini-2024-09-12",
		"gpt-5-chat-latest", "gpt-5-codex", "gpt-5.1-chat-latest",
		"gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini",
		"gpt-5.2-codex", "gpt-5.2-chat-latest", "gpt-5.3-chat-latest",
		"chatgpt-4o-latest", "codex-mini-latest",
		"gpt-4o-audio-preview", "gpt-4o-realtime-preview",
		"gpt-4o-mini-audio-preview", "gpt-4o-mini-realtime-preview",
		"dall-e-2", "dall-e-3":
		return true
	}
	if chatGPTAccount {
		// These older mainline/pro aliases normalize to retired Codex models in
		// the ChatGPT adapter. They remain available to API-key accounts.
		for _, prefix := range []string{"gpt-5.2-", "gpt-5.1-", "gpt-5-mini", "gpt-5-nano", "gpt-5-pro", "gpt-5-chat", "gpt-5-2025-"} {
			if strings.HasPrefix(model, prefix) {
				return true
			}
		}
		switch model {
		case "gpt-5", "gpt-5.1", "gpt-5.2", "gpt-5.3", "gpt-5.3-codex", "gpt-5.3-codex-spark",
			"gpt-5.4", "gpt-5.4-2026-03-05", "gpt-5.4-mini", "gpt-5.4-pro":
			// The Codex adapter normalizes gpt-5.4-pro to gpt-5.4.
			return true
		}
	}
	return false
}

// ModelsForAccount returns defaults without advertising models retired on the
// account's upstream. Explicit aliases are handled separately by the service.
func ModelsForAccount(chatGPTAccount bool) []Model {
	models := make([]Model, 0, len(DefaultModels))
	for _, model := range DefaultModels {
		if !IsRetiredModel(model.ID, chatGPTAccount) {
			models = append(models, model)
		}
	}
	return models
}
