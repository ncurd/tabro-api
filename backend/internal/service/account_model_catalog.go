package service

import (
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// IsRetiredModel filters catalog suggestions by upstream lifecycle, not request
// authorization. Check mapping targets so aliases to supported models still work.
// Third-party Claude channels have independent retirement schedules.
func (a *Account) IsRetiredModel(model string) bool {
	if a == nil {
		return false
	}
	model = strings.ToLower(strings.TrimSpace(model))
	switch a.Platform {
	case PlatformOpenAI:
		return openai.IsRetiredModel(model, a.IsOAuth())
	case PlatformGemini:
		return model == "gemini-2.0-flash" || model == "gemini-2.0-flash-001"
	case PlatformAnthropic:
		if a.Type == AccountTypeBedrock {
			// All-region Bedrock EOL 2026-06-19. Other legacy Claude
			// models have region-specific schedules and remain configurable.
			return model == "claude-3-5-haiku-20241022" || model == "anthropic.claude-3-5-haiku-20241022-v1:0" || model == "us.anthropic.claude-3-5-haiku-20241022-v1:0"
		}
		// https://platform.claude.com/docs/en/about-claude/model-deprecations
		switch model {
		case "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20240620",
			"claude-3-5-haiku-20241022", "claude-3-opus-20240229",
			"claude-3-sonnet-20240229", "claude-3-haiku-20240307",
			"claude-3-7-sonnet-20250219", "claude-sonnet-4-20250514",
			"claude-opus-4-20250514", "claude-opus-4-1-20250805",
			"claude-2.1", "claude-2.0", "claude-instant-1.2":
			return true
		}
	}
	return false
}

// AvailableOpenAIModels lists the account's usable defaults or configured aliases.
func (a *Account) AvailableOpenAIModels() []openai.Model {
	defaults := openai.ModelsForAccount(a.IsOAuth())
	mapping := a.GetModelMapping()
	if a.IsOpenAIPassthroughEnabled() || len(mapping) == 0 {
		return defaults
	}
	metadata := make(map[string]openai.Model, len(defaults))
	for _, model := range defaults {
		metadata[model.ID] = model
	}
	models := make([]openai.Model, 0, len(mapping))
	for requested, upstream := range mapping {
		if a.IsRetiredModel(upstream) {
			continue
		}
		model, ok := metadata[requested]
		if !ok {
			model = openai.Model{ID: requested, Object: "model", Type: "model", DisplayName: requested}
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}
