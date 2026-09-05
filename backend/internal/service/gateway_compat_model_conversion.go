package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// resolveAnthropicCompatTargetModel resolves the model that an Anthropic
// upstream will actually receive. Compatibility conversion must use this
// model so model-specific request fields are mapped against the target rather
// than a client-side alias.
func resolveAnthropicCompatTargetModel(account *Account, requestedModel string) string {
	mappedModel := requestedModel
	if account == nil {
		return mappedModel
	}
	if account.Type == AccountTypeAPIKey {
		mappedModel = account.GetMappedModel(requestedModel)
	}
	if mappedModel == requestedModel && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		mappedModel = claude.NormalizeModelID(requestedModel)
	}
	return mappedModel
}

func responsesToAnthropicForTargetModel(req *apicompat.ResponsesRequest, targetModel string) (*apicompat.AnthropicRequest, error) {
	targetReq := *req
	targetReq.Model = targetModel
	return apicompat.ResponsesToAnthropicRequest(&targetReq)
}

func anthropicToResponsesForTargetModel(req *apicompat.AnthropicRequest, targetModel string) (*apicompat.ResponsesRequest, error) {
	targetReq := *req
	targetReq.Model = targetModel
	return apicompat.AnthropicToResponses(&targetReq)
}
