package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicRequest_Opus55AdaptiveThinking(t *testing.T) {
	for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			temperature, topP := 0.7, 0.9
			req := &ResponsesRequest{
				Model:       "claude-opus-5-5",
				Input:       json.RawMessage(`"Hello"`),
				Temperature: &temperature,
				TopP:        &topP,
				Reasoning:   &ResponsesReasoning{Effort: effort},
			}
			out, err := ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			require.Equal(t, "claude-opus-5-5", out.Model)
			require.Nil(t, out.Temperature)
			require.Nil(t, out.TopP)
			require.NotNil(t, req.Temperature, "conversion must not mutate the caller")
			if effort == "" {
				require.Nil(t, out.Thinking)
				require.Nil(t, out.OutputConfig, "upstream should retain its medium default")
				return
			}
			require.Equal(t, &AnthropicThinking{Type: "adaptive"}, out.Thinking)
			wantEffort := effort
			if effort == "none" || effort == "minimal" {
				wantEffort = "low"
			}
			require.Equal(t, wantEffort, out.OutputConfig.Effort)
		})
	}
}
