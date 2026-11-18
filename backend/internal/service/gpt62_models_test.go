package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestGPT62CompatibilityKeepsModelsAndIndependentReasoning(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		t.Run(model, func(t *testing.T) {
			for _, alias := range []string{model, "openai/" + model, strings.ToUpper(model), strings.ReplaceAll(model, "-", " ")} {
				require.Equal(t, model, normalizeCodexModel(alias))
			}
			for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
				alias := "openai/" + model + "-" + effort
				require.Equal(t, model, normalizeOpenAIModelForUpstream(&Account{Type: AccountTypeOAuth}, alias))
				require.NoError(t, validateGPT61SolReasoning(alias, model, effort))
				req := &apicompat.AnthropicRequest{Model: alias}
				applyOpenAICompatModelNormalization(req)
				require.Equal(t, model, req.Model)
				require.Equal(t, effort, req.OutputConfig.Effort)
				converted, err := apicompat.AnthropicToResponses(req)
				require.NoError(t, err)
				require.Equal(t, model, converted.Model)
				require.Equal(t, effort, converted.Reasoning.Effort)
			}
			converted, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: model})
			require.NoError(t, err)
			require.Equal(t, "medium", converted.Reasoning.Effort)
			require.True(t, shouldAutoInjectPromptCacheKeyForCompat(model))
			for _, effort := range []string{"none", "minimal"} {
				alias := model + "-" + effort
				require.Equal(t, alias, NormalizeOpenAICompatRequestedModel(alias))
				require.ErrorIs(t, validateGPT61SolReasoning(alias, "gpt-6-sol", ""), ErrUnsupportedGPT61SolReasoning)
				require.ErrorIs(t, validateGPT61SolReasoning("custom", model, effort), ErrUnsupportedGPT61SolReasoning)
				_, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: model, OutputConfig: &apicompat.AnthropicOutputConfig{Effort: effort}})
				require.ErrorContains(t, err, "not supported by "+model)
				_, err = textBillingQuoteRequest("/v1/responses", "unsupported", []byte(fmt.Sprintf(`{"model":%q,"reasoning":{"effort":%q}}`, model, effort)))
				require.ErrorIs(t, err, ErrUnsupportedGPT61SolReasoning)
			}
		})
	}
}

func TestGPT62TextCapabilitiesRequireExactSupportedModels(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, alias := range []string{model, model + "-high", "openai/" + model + "-max"} {
			caps, ok := (*PricingService)(nil).GetTextModelCapabilities(alias)
			require.True(t, ok, alias)
			require.EqualValues(t, 922000, caps.MaxInputTokens)
			require.EqualValues(t, 128000, caps.MaxOutputTokens)
		}
		for _, suffix := range []string{"-none", "-minimal", "-invented"} {
			_, ok := (*PricingService)(nil).GetTextModelCapabilities(model + suffix)
			require.False(t, ok, model+suffix)
		}
	}
}
