package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicForTargetModelUsesResolvedMapping(t *testing.T) {
	tests := []struct {
		name           string
		requestedModel string
		mapping        map[string]any
		wantModel      string
	}{
		{
			name:           "alias maps to fable 5.1",
			requestedModel: "claude-reasoner",
			mapping:        map[string]any{"claude-reasoner": "claude-fable-5-1"},
			wantModel:      "claude-fable-5-1",
		},
		{
			name:           "exact opus 5 mapping",
			requestedModel: "claude-opus-5",
			mapping:        map[string]any{"claude-opus-5": "claude-opus-5"},
			wantModel:      "claude-opus-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Type:     AccountTypeAPIKey,
				Platform: PlatformAnthropic,
				Credentials: map[string]any{
					"model_mapping": tt.mapping,
				},
			}
			req := &apicompat.ResponsesRequest{
				Model:     tt.requestedModel,
				Input:     json.RawMessage(`"hello"`),
				Reasoning: &apicompat.ResponsesReasoning{Effort: "xhigh"},
			}

			mappedModel := resolveAnthropicCompatTargetModel(account, req.Model)
			anthropicReq, err := responsesToAnthropicForTargetModel(req, mappedModel)

			require.NoError(t, err)
			require.Equal(t, tt.requestedModel, req.Model, "conversion must not mutate the client model")
			require.Equal(t, tt.wantModel, anthropicReq.Model)
			require.NotNil(t, anthropicReq.OutputConfig)
			require.Equal(t, "xhigh", anthropicReq.OutputConfig.Effort)
		})
	}
}

func TestChatCompletionsToAnthropicUsesResolvedTargetForEffort(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"chat-reasoner": "claude-opus-5",
			},
		},
	}
	chatReq := &apicompat.ChatCompletionsRequest{
		Model:           "chat-reasoner",
		Messages:        []apicompat.ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		ReasoningEffort: "xhigh",
	}

	mappedModel := resolveAnthropicCompatTargetModel(account, chatReq.Model)
	responsesReq, err := apicompat.ChatCompletionsToResponses(chatReq)
	require.NoError(t, err)
	anthropicReq, err := responsesToAnthropicForTargetModel(responsesReq, mappedModel)

	require.NoError(t, err)
	require.Equal(t, "chat-reasoner", chatReq.Model, "conversion must not mutate the client model")
	require.Equal(t, "claude-opus-5", anthropicReq.Model)
	require.NotNil(t, anthropicReq.OutputConfig)
	require.Equal(t, "xhigh", anthropicReq.OutputConfig.Effort)
}

func TestAnthropicToResponsesUsesResolvedOpenAITargetForEffort(t *testing.T) {
	tests := []struct {
		name           string
		requestedModel string
		mapping        map[string]any
		outputEffort   string
		wantModel      string
		wantEffort     string
	}{
		{
			name:           "alias maps max to gpt 6",
			requestedModel: "openai-reasoner",
			mapping:        map[string]any{"openai-reasoner": "gpt-6-astra"},
			outputEffort:   "max",
			wantModel:      "gpt-6-astra",
			wantEffort:     "max",
		},
		{
			name:           "exact gpt 5.6 mapping keeps max",
			requestedModel: "gpt-5.6",
			mapping:        map[string]any{"gpt-5.6": "gpt-5.6"},
			outputEffort:   "max",
			wantModel:      "gpt-5.6",
			wantEffort:     "max",
		},
		{
			name:           "none suffix follows mapped gpt 5.6 capability",
			requestedModel: "gpt-5.4-none",
			mapping:        map[string]any{"gpt-5.4": "gpt-5.6-sol"},
			wantModel:      "gpt-5.6-sol",
			wantEffort:     "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Type:     AccountTypeAPIKey,
				Platform: PlatformOpenAI,
				Credentials: map[string]any{
					"model_mapping": tt.mapping,
				},
			}
			req := &apicompat.AnthropicRequest{Model: tt.requestedModel}
			if tt.outputEffort != "" {
				req.OutputConfig = &apicompat.AnthropicOutputConfig{Effort: tt.outputEffort}
			}

			clientModel := req.Model
			normalizedModel := NormalizeOpenAICompatRequestedModel(clientModel)
			billingModel := resolveOpenAIForwardModel(account, normalizedModel, "")
			upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
			applyOpenAICompatModelNormalizationForTarget(req, upstreamModel)
			responsesReq, err := anthropicToResponsesForTargetModel(req, upstreamModel)

			require.NoError(t, err)
			require.Equal(t, clientModel, tt.requestedModel, "the saved client model must remain available for responses")
			require.Equal(t, tt.wantModel, responsesReq.Model)
			require.NotNil(t, responsesReq.Reasoning)
			require.Equal(t, tt.wantEffort, responsesReq.Reasoning.Effort)
		})
	}
}
