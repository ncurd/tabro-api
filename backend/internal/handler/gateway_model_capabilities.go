package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Independent input/output bounds must never be added to infer context capacity.
// A context window is included only when the catalog explicitly documents it.
type gatewayModelTokenLimits struct {
	MaxInputTokens  int64 `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int64 `json:"max_output_tokens,omitempty"`
	ContextWindow   int64 `json:"context_window,omitempty"`
}

type gatewayOpenAIModel struct {
	openai.Model
	gatewayModelTokenLimits
	SpeechTasks []string `json:"speech_tasks"`
}

type gatewayClaudeModel struct {
	claude.Model
	gatewayModelTokenLimits
	SpeechTasks []string `json:"speech_tasks"`
}

func (h *GatewayHandler) modelTokenLimits(model string) gatewayModelTokenLimits {
	capabilities, _ := h.gatewayService.GetTextModelCapabilities(model)
	limits := gatewayModelTokenLimits{}
	if capabilities.MaxInputTokens > 0 {
		limits.MaxInputTokens = capabilities.MaxInputTokens
	}
	if capabilities.MaxOutputTokens > 0 {
		limits.MaxOutputTokens = capabilities.MaxOutputTokens
	}
	limits.ContextWindow = capabilities.ContextWindow
	return limits
}

func (h *GatewayHandler) openAIModelCatalog(models []openai.Model, contexts ...context.Context) []gatewayOpenAIModel {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	speech := h.gatewayService.SpeechModelTaskCatalog(ctx, ids)
	catalog := make([]gatewayOpenAIModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, gatewayOpenAIModel{Model: model, gatewayModelTokenLimits: h.modelTokenLimits(model.ID), SpeechTasks: speech[model.ID]})
	}
	return catalog
}

func (h *GatewayHandler) claudeModelCatalog(models []claude.Model, contexts ...context.Context) []gatewayClaudeModel {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	speech := h.gatewayService.SpeechModelTaskCatalog(ctx, ids)
	catalog := make([]gatewayClaudeModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, gatewayClaudeModel{Model: model, gatewayModelTokenLimits: h.modelTokenLimits(model.ID), SpeechTasks: speech[model.ID]})
	}
	return catalog
}
