package handler

import (
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
}

type gatewayClaudeModel struct {
	claude.Model
	gatewayModelTokenLimits
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

func (h *GatewayHandler) openAIModelCatalog(models []openai.Model) []gatewayOpenAIModel {
	catalog := make([]gatewayOpenAIModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, gatewayOpenAIModel{Model: model, gatewayModelTokenLimits: h.modelTokenLimits(model.ID)})
	}
	return catalog
}

func (h *GatewayHandler) claudeModelCatalog(models []claude.Model) []gatewayClaudeModel {
	catalog := make([]gatewayClaudeModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, gatewayClaudeModel{Model: model, gatewayModelTokenLimits: h.modelTokenLimits(model.ID)})
	}
	return catalog
}
