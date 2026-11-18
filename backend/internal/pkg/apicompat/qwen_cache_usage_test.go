package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestQwenCacheUsageDecodesOfficialChatAndAnthropicBreakdowns(t *testing.T) {
	for _, test := range []struct {
		name, aggregates string
		total            int
	}{
		{"both_aggregate_aliases", `"cache_write_tokens":2048,"cache_creation_input_tokens":2048,`, 2048},
		{"compatibility_aggregate", `"cache_creation_input_tokens":2048,`, 2048},
		{"details_only", ``, 2048},
		{"positive_total_is_authoritative", `"cache_write_tokens":1024,"cache_creation_input_tokens":2048,`, 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			var chat ChatCompletionsResponse
			require.NoError(t, json.Unmarshal([]byte(`{"usage":{"prompt_tokens":2304,"completion_tokens":128,"total_tokens":2432,"prompt_tokens_details":{"cached_tokens":0,`+test.aggregates+`"cache_type":"ephemeral","cache_creation":{"ephemeral_5m_input_tokens":512,"ephemeral_1h_input_tokens":1536}}}}`), &chat))
			require.NotNil(t, chat.Usage)
			details := chat.Usage.PromptTokensDetails
			require.NotNil(t, details)
			require.Equal(t, test.total, details.CacheWriteTokens, "aggregate aliases must not be added together")
			require.NotNil(t, details.CacheWriteTokensDetails)
			require.Equal(t, 512, details.CacheWriteTokensDetails.TTL5mTokens)
			require.Equal(t, 1536, details.CacheWriteTokensDetails.TTL1hTokens)
			require.Zero(t, details.CacheWriteTokensDetails.TTL30mTokens)
			require.Zero(t, details.CacheWriteTokensDetails.TTL12hTokens)
		})
	}
	var anthropic AnthropicResponse
	require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_qwen","model":"qwen4-max","usage":{"input_tokens":8,"output_tokens":128,"cache_read_input_tokens":0,"cache_creation_input_tokens":2048,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2048}}}`), &anthropic))
	require.Zero(t, anthropic.Usage.CacheCreation5mTokens)
	require.Equal(t, 2048, anthropic.Usage.CacheCreation1hTokens)
	require.Zero(t, anthropic.Usage.CacheCreation30mTokens)
	require.Zero(t, anthropic.Usage.CacheCreation12hTokens)
	responses := AnthropicToResponsesResponse(&anthropic)
	require.Equal(t, 2056, responses.Usage.InputTokens)
	require.Equal(t, 2048, responses.Usage.InputTokensDetails.CacheWriteTokens)
	require.Equal(t, 2048, responses.Usage.InputTokensDetails.CacheWriteTokensDetails.TTL1hTokens)
}

func TestQwenResponsesCacheUsageSurvivesProtocolRoundTrips(t *testing.T) {
	for _, aggregate := range []string{`"cache_write_tokens":3000,`, ``} {
		var responses ResponsesResponse
		require.NoError(t, json.Unmarshal([]byte(`{"id":"resp_qwen","status":"completed","usage":{"input_tokens":6000,"output_tokens":37,"input_tokens_details":{"cached_tokens":1000,`+aggregate+`"cache_write_tokens_details":{"ttl_5m_tokens":1000,"ttl_1h_tokens":2000}}}}`), &responses))
		anthropic := ResponsesToAnthropic(&responses, "qwen4-max")
		require.Equal(t, 2000, anthropic.Usage.InputTokens)
		require.Equal(t, 3000, anthropic.Usage.CacheCreationInputTokens)
		require.Equal(t, 1000, anthropic.Usage.CacheCreation5mTokens)
		require.Equal(t, 2000, anthropic.Usage.CacheCreation1hTokens)
		require.Zero(t, anthropic.Usage.CacheCreation30mTokens)
		require.Zero(t, anthropic.Usage.CacheCreation12hTokens)
		wire, err := json.Marshal(anthropic)
		require.NoError(t, err)
		require.EqualValues(t, 1000, gjson.GetBytes(wire, "usage.cache_creation.ephemeral_5m_input_tokens").Int())
		require.EqualValues(t, 2000, gjson.GetBytes(wire, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
		var decoded AnthropicResponse
		require.NoError(t, json.Unmarshal(wire, &decoded))
		roundTrip := AnthropicToResponsesResponse(&decoded)
		require.Equal(t, 6000, roundTrip.Usage.InputTokens, "read/write tokens are included once in OpenAI input totals")
		require.Equal(t, 37, roundTrip.Usage.OutputTokens)
		require.Equal(t, 3000, roundTrip.Usage.InputTokensDetails.CacheWriteTokens)
		require.Equal(t, 1000, roundTrip.Usage.InputTokensDetails.CacheWriteTokensDetails.TTL5mTokens)
		require.Equal(t, 2000, roundTrip.Usage.InputTokensDetails.CacheWriteTokensDetails.TTL1hTokens)

		chat := ResponsesToChatCompletions(&responses, "qwen4-max")
		wire, err = json.Marshal(chat)
		require.NoError(t, err)
		require.EqualValues(t, 3000, gjson.GetBytes(wire, "usage.prompt_tokens_details.cache_creation_input_tokens").Int())
		require.EqualValues(t, 1000, gjson.GetBytes(wire, "usage.prompt_tokens_details.cache_creation.ephemeral_5m_input_tokens").Int())
		require.EqualValues(t, 2000, gjson.GetBytes(wire, "usage.prompt_tokens_details.cache_creation.ephemeral_1h_input_tokens").Int())
		var decodedChat ChatCompletionsResponse
		require.NoError(t, json.Unmarshal(wire, &decodedChat))
		require.Equal(t, 3000, decodedChat.Usage.PromptTokensDetails.CacheWriteTokens)
		require.Equal(t, 2000, decodedChat.Usage.PromptTokensDetails.CacheWriteTokensDetails.TTL1hTokens)
	}
}

func TestQwenCacheUsageStreamConversionsRetainLifetimes(t *testing.T) {
	var start AnthropicStreamEvent
	require.NoError(t, json.Unmarshal([]byte(`{"type":"message_start","message":{"id":"msg_qwen","model":"qwen4-max","usage":{"input_tokens":2000,"cache_read_input_tokens":1000,"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":2000}}}}`), &start))
	for _, finalise := range []bool{false, true} {
		state := NewAnthropicEventToResponsesState()
		AnthropicEventToResponsesEvents(&start, state)
		AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_delta", Usage: &AnthropicUsage{OutputTokens: 37}}, state)
		var completed []ResponsesStreamEvent
		if finalise {
			completed = FinalizeAnthropicResponsesStream(state)
		} else {
			completed = AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)
		}
		require.Len(t, completed, 1)
		usage := completed[0].Response.Usage
		require.Equal(t, 6000, usage.InputTokens)
		require.Equal(t, 3000, usage.InputTokensDetails.CacheWriteTokens)
		require.Equal(t, 1000, usage.InputTokensDetails.CacheWriteTokensDetails.TTL5mTokens)
		require.Equal(t, 2000, usage.InputTokensDetails.CacheWriteTokensDetails.TTL1hTokens)

		anthropicEvents := ResponsesEventToAnthropicEvents(&completed[0], NewResponsesEventToAnthropicState())
		var terminal *AnthropicUsage
		for _, event := range anthropicEvents {
			if event.Type == "message_delta" {
				terminal = event.Usage
			}
		}
		require.NotNil(t, terminal)
		require.Equal(t, 2000, terminal.InputTokens)
		require.Equal(t, 1000, terminal.CacheCreation5mTokens)
		require.Equal(t, 2000, terminal.CacheCreation1hTokens)
		wire, err := json.Marshal(terminal)
		require.NoError(t, err)
		require.EqualValues(t, 2000, gjson.GetBytes(wire, "cache_creation.ephemeral_1h_input_tokens").Int())

		chatState := NewResponsesEventToChatState()
		chatState.IncludeUsage = true
		chunks := ResponsesEventToChatChunks(&completed[0], chatState)
		require.Len(t, chunks, 2)
		details := chunks[1].Usage.PromptTokensDetails
		require.Equal(t, 3000, details.CacheWriteTokens)
		require.Equal(t, 1000, details.CacheCreation.Ephemeral5mInputTokens)
		require.Equal(t, 2000, details.CacheCreation.Ephemeral1hInputTokens)
		require.Zero(t, details.CacheWriteTokensDetails.TTL30mTokens)
		require.Zero(t, details.CacheWriteTokensDetails.TTL12hTokens)
	}
}

func TestCacheWriteLifetimesRemainDistinctAcrossProviders(t *testing.T) {
	usage := &ResponsesUsage{InputTokens: 100, InputTokensDetails: &ResponsesInputTokensDetails{
		CacheWriteTokensDetails: &CacheWriteTokensDetails{TTL5mTokens: 10, TTL1hTokens: 20, TTL30mTokens: 3, TTL12hTokens: 4},
	}}
	converted := responsesUsageToAnthropicUsage(usage)
	require.Equal(t, 37, converted.CacheCreationInputTokens)
	require.Equal(t, 63, converted.InputTokens)
	require.Equal(t, 10, converted.CacheCreation5mTokens)
	require.Equal(t, 20, converted.CacheCreation1hTokens)
	require.Equal(t, 3, converted.CacheCreation30mTokens)
	require.Equal(t, 4, converted.CacheCreation12hTokens)
	back := AnthropicToResponsesResponse(&AnthropicResponse{Usage: converted})
	require.Equal(t, usage.InputTokens, back.Usage.InputTokens)
	require.Equal(t, usage.InputTokensDetails.CacheWriteTokensDetails, back.Usage.InputTokensDetails.CacheWriteTokensDetails)
}
