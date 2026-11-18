package openai_ws_v2

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayCacheWriteLifetimesSurviveTerminalUsageAndAggregation(t *testing.T) {
	for _, eventType := range []string{"response.completed", "response.done", "response.incomplete", "response.cancelled"} {
		t.Run(eventType, func(t *testing.T) {
			state := &relayState{}
			body := []byte(fmt.Sprintf(`{"type":%q,"response":{"service_tier":"ultrafast","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cache_write_tokens":30,"cache_write_tokens_details":{"ttl_30m_tokens":10,"ttl_12h_tokens":20}}}}}`, eventType))
			for range 2 {
				usage := parseUsageAndAccumulate(state, body, eventType, nil)
				require.Equal(t, 30, usage.CacheCreationInputTokens)
				require.Equal(t, 10, usage.CacheCreation30mTokens)
				require.Equal(t, 20, usage.CacheCreation12hTokens)
			}
			result := &RelayResult{}
			enrichResult(result, state, time.Second)
			require.Equal(t, 200, result.Usage.InputTokens)
			require.Equal(t, 60, result.Usage.CacheCreationInputTokens)
			require.Equal(t, 20, result.Usage.CacheCreation30mTokens)
			require.Equal(t, 40, result.Usage.CacheCreation12hTokens)
			require.Equal(t, "ultrafast", result.Usage.ServiceTier)
		})
	}
}

func TestRelayCacheWriteDetailsPreserveLegacyTotalsAndInferMissingAggregate(t *testing.T) {
	for _, tc := range []struct {
		details               string
		total, ttl30m, ttl12h int
	}{
		{`"cache_write_tokens":30`, 30, 0, 0},
		{`"cache_write_tokens_details":{"ttl_30m_tokens":10,"ttl_12h_tokens":20}`, 30, 10, 20},
		{`"cache_write_tokens":40,"cache_write_tokens_details":{"ttl_12h_tokens":20}`, 40, 0, 20},
		{`"cache_write_tokens":20,"cache_write_tokens_details":{"ttl_30m_tokens":10,"ttl_12h_tokens":20}`, 20, 10, 20},
	} {
		t.Run(tc.details, func(t *testing.T) {
			state := &relayState{}
			body := []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{` + tc.details + `}}}}`)
			usage := parseUsageAndAccumulate(state, body, "response.completed", nil)
			require.Equal(t, tc.total, usage.CacheCreationInputTokens)
			require.Equal(t, tc.ttl30m, usage.CacheCreation30mTokens)
			require.Equal(t, tc.ttl12h, usage.CacheCreation12hTokens)
		})
	}
}

func TestRelayInvalidCacheWriteLifetimeDoesNotPartiallyAccumulateUsage(t *testing.T) {
	for _, field := range []string{"ttl_5m_tokens", "ttl_1h_tokens", "ttl_12h_tokens"} {
		for _, value := range []string{`-1`, `"20"`, `null`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				state := &relayState{}
				body := []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cache_write_tokens":30,"cache_write_tokens_details":{"` + field + `":` + value + `}}}}}`)
				failed := 0
				usage := parseUsageAndAccumulate(state, body, "response.completed", func(string, string) { failed++ })
				require.Equal(t, 1, failed)
				require.Equal(t, Usage{}, usage)
				require.Equal(t, Usage{}, state.usage)
			})
		}
	}
}

func TestRelayQwenCacheWriteLifetimesSurviveTurnsAndMissingAggregate(t *testing.T) {
	for _, aggregate := range []string{`"cache_write_tokens":30,`, ""} {
		for _, eventType := range []string{"response.completed", "response.done", "response.incomplete", "response.cancelled"} {
			t.Run(fmt.Sprintf("aggregate=%t/%s", aggregate != "", eventType), func(t *testing.T) {
				state := &relayState{}
				body := []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{` + aggregate + `"cache_write_tokens_details":{"ttl_5m_tokens":10,"ttl_1h_tokens":20}}}}}`)
				for range 2 {
					usage := parseUsageAndAccumulate(state, body, eventType, nil)
					require.Equal(t, 30, usage.CacheCreationInputTokens)
					require.Equal(t, 10, usage.CacheCreation5mTokens)
					require.Equal(t, 20, usage.CacheCreation1hTokens)
					require.Zero(t, usage.CacheCreation30mTokens)
					require.Zero(t, usage.CacheCreation12hTokens)
				}
				result := &RelayResult{}
				enrichResult(result, state, time.Second)
				require.Equal(t, 60, result.Usage.CacheCreationInputTokens)
				require.Equal(t, 20, result.Usage.CacheCreation5mTokens)
				require.Equal(t, 40, result.Usage.CacheCreation1hTokens)
			})
		}
	}
}
