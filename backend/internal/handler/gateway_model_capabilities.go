package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Input and output limits are independent catalog bounds. The catalog does not
// supply a combined context capacity, so context_window is deliberately absent;
// adding these bounds would incorrectly promise that both can be used together.
type gatewayModelTokenLimits struct {
	MaxInputTokens  int64 `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int64 `json:"max_output_tokens,omitempty"`
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
	capabilities, known := h.gatewayService.GetTextModelCapabilities(model)
	limits := gatewayModelTokenLimits{}
	if known {
		if capabilities.MaxInputTokens > 0 {
			limits.MaxInputTokens = capabilities.MaxInputTokens
		}
		if capabilities.MaxOutputTokens > 0 {
			limits.MaxOutputTokens = capabilities.MaxOutputTokens
		}
	}
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
