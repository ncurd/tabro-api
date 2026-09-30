package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

var ErrUnsupportedGPT61SolReasoning = errors.New("unsupported gpt-6.1-sol reasoning effort")

func NormalizeOpenAICompatRequestedModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}

	normalized, _, ok := splitOpenAICompatReasoningModel(trimmed)
	if !ok || normalized == "" {
		return trimmed
	}
	return normalized
}

func applyOpenAICompatModelNormalization(req *apicompat.AnthropicRequest) {
	applyOpenAICompatModelNormalizationForTarget(req, "")
}

func applyOpenAICompatModelNormalizationForTarget(req *apicompat.AnthropicRequest, targetModel string) {
	if req == nil {
		return
	}

	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return
	}

	normalizedModel, derivedEffort, hasReasoningSuffix := splitOpenAICompatReasoningModel(originalModel)
	if hasReasoningSuffix && normalizedModel != "" {
		req.Model = normalizedModel
	}

	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		return
	}

	effortModel := strings.TrimSpace(targetModel)
	if effortModel == "" {
		effortModel = normalizedModel
	}
	claudeEffort := openAIReasoningEffortToClaudeOutputEffortForModel(derivedEffort, effortModel)
	if claudeEffort == "" {
		return
	}

	if req.OutputConfig == nil {
		req.OutputConfig = &apicompat.AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = claudeEffort
}

func splitOpenAICompatReasoningModel(model string) (normalizedModel string, reasoningEffort string, ok bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", "", false
	}

	modelID := trimmed
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	modelID = strings.TrimSpace(modelID)
	if !strings.HasPrefix(strings.ToLower(modelID), "gpt-") {
		return trimmed, "", false
	}

	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return trimmed, "", false
	}

	normalizedModel = normalizeCodexModel(modelID)
	last := strings.NewReplacer("-", "", "_", "", " ", "").Replace(parts[len(parts)-1])
	if isGPT61SolModelFamily(modelID) && (last == "none" || last == "minimal") {
		return trimmed, "", false
	}
	switch last {
	case "none":
		reasoningEffort = "none"
	case "minimal":
	case "low", "medium", "high":
		reasoningEffort = last
	case "max":
		if strings.EqualFold(modelID, "gpt-5.1-codex-max") {
			return trimmed, "", false
		}
		reasoningEffort = "max"
	case "xhigh", "extrahigh":
		reasoningEffort = "xhigh"
	default:
		return trimmed, "", false
	}

	return normalizedModel, reasoningEffort, true
}

func openAIReasoningEffortToClaudeOutputEffort(effort string) string {
	return openAIReasoningEffortToClaudeOutputEffortForModel(effort, "")
}

func openAIReasoningEffortToClaudeOutputEffortForModel(effort, model string) string {
	switch strings.TrimSpace(effort) {
	case "none":
		if supportsOpenAINoneReasoningEffort(model) {
			return "none"
		}
	case "low", "medium", "high", "max":
		return effort
	case "xhigh":
		if supportsIndependentOpenAIReasoningEfforts(model) {
			return "xhigh"
		}
		return "max"
	default:
		return ""
	}
	return ""
}

func supportsIndependentOpenAIReasoningEfforts(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "gpt-6.1-sol") || strings.Contains(model, "gpt-6-astra") || strings.Contains(model, "gpt-6-sol") ||
		strings.Contains(model, "gpt-6-luna") || strings.Contains(model, "gpt-5.6")
}

func isGPT61SolModelFamily(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if lastSlash := strings.LastIndex(model, "/"); lastSlash >= 0 {
		model = model[lastSlash+1:]
	}
	return model == "gpt-6.1-sol" || strings.HasPrefix(model, "gpt-6.1-sol-") ||
		model == "gpt 6.1 sol" || strings.HasPrefix(model, "gpt 6.1 sol ")
}

func validateGPT61SolReasoning(requestedModel, targetModel, effort string) error {
	modelID := strings.ToLower(strings.TrimSpace(requestedModel))
	if lastSlash := strings.LastIndex(modelID, "/"); lastSlash >= 0 {
		modelID = modelID[lastSlash+1:]
	}
	modelID = strings.NewReplacer("_", "-", " ", "-").Replace(modelID)
	if modelID == "gpt-6.1-sol-none" || modelID == "gpt-6.1-sol-minimal" {
		return fmt.Errorf("%w: reasoning effort %q is not supported by gpt-6.1-sol", ErrUnsupportedGPT61SolReasoning, strings.TrimPrefix(modelID, "gpt-6.1-sol-"))
	}
	if isGPT61SolModelFamily(targetModel) {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "none" || effort == "minimal" {
			return fmt.Errorf("%w: reasoning effort %q is not supported by gpt-6.1-sol", ErrUnsupportedGPT61SolReasoning, effort)
		}
	}
	return nil
}

func supportsOpenAINoneReasoningEffort(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "gpt-6-sol") || strings.Contains(model, "gpt-6-luna") || strings.Contains(model, "gpt-5.6")
}
