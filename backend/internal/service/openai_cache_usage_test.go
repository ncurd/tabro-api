//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheWriteLifetimesSurviveResponsesJSONSSEAndWS(t *testing.T) {
	for _, aggregate := range []string{`"cache_write_tokens":7000,`, ""} {
		t.Run(fmt.Sprintf("aggregate=%t", aggregate != ""), func(t *testing.T) {
			body := []byte(`{"id":"resp_cache","status":"completed","usage":{"input_tokens":12000,"output_tokens":11,"input_tokens_details":{"cached_tokens":4000,` + aggregate + `"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`)
			event := []byte(`{"type":"response.completed","response":` + string(body) + `}`)
			parsers := map[string]func() OpenAIUsage{
				"HTTP JSON": func() OpenAIUsage {
					usage, ok := extractOpenAIUsageFromJSONBytes(body)
					require.True(t, ok)
					return usage
				},
				"HTTP SSE": func() OpenAIUsage {
					var usage OpenAIUsage
					(&OpenAIGatewayService{}).parseSSEUsageBytes(event, &usage)
					return usage
				},
				"WS completed event": func() OpenAIUsage {
					var usage OpenAIUsage
					parseOpenAIWSResponseUsageFromCompletedEvent(event, &usage)
					return usage
				},
				"WS buffered response": func() OpenAIUsage {
					var usage OpenAIUsage
					populateOpenAIUsageFromResponseJSON(body, &usage)
					return usage
				},
				"typed response": func() OpenAIUsage {
					var response apicompat.ResponsesResponse
					require.NoError(t, json.Unmarshal(body, &response))
					return openAIUsageFromResponsesResponse(&response)
				},
			}
			for name, parse := range parsers {
				t.Run(name, func(t *testing.T) {
					usage := parse()
					require.Equal(t, 12000, usage.InputTokens)
					require.Equal(t, 11, usage.OutputTokens)
					require.Equal(t, 4000, usage.CacheReadInputTokens)
					require.Equal(t, 7000, usage.CacheCreationInputTokens)
					require.Equal(t, 3000, usage.CacheCreation30mTokens)
					require.Equal(t, 4000, usage.CacheCreation12hTokens)
					billed := openAIUsageBillingTokens(usage)
					require.Equal(t, 1000, billed.InputTokens, "write details must not also be billed as ordinary input")
					require.Equal(t, 7000, billed.CacheCreationTokens)
					require.Equal(t, 3000, billed.CacheCreation30mTokens)
					require.Equal(t, 4000, billed.CacheCreation12hTokens)
				})
			}
		})
	}
}

func TestOpenAICacheWriteCompatibilityBridgesPreserveLifetimes(t *testing.T) {
	response := &apicompat.ResponsesResponse{ID: "resp_cache", Model: "gpt-6.2-sol", Status: "completed", Usage: &apicompat.ResponsesUsage{
		InputTokens: 12000, OutputTokens: 11, TotalTokens: 12011,
		InputTokensDetails: &apicompat.ResponsesInputTokensDetails{CachedTokens: 4000, CacheWriteTokens: 7000,
			CacheWriteTokensDetails: &apicompat.CacheWriteTokensDetails{TTL30mTokens: 3000, TTL12hTokens: 4000}},
	}}
	anthropic := apicompat.ResponsesToAnthropic(response, response.Model)
	require.Equal(t, 1000, anthropic.Usage.InputTokens)
	require.Equal(t, 3000, anthropic.Usage.CacheCreation30mTokens)
	require.Equal(t, 4000, anthropic.Usage.CacheCreation12hTokens)
	var claude ClaudeUsage
	mergeAnthropicUsage(&claude, anthropic.Usage)
	require.Equal(t, 7000, claude.CacheCreationInputTokens)
	require.Equal(t, 3000, claude.CacheCreation30mTokens)
	require.Equal(t, 4000, claude.CacheCreation12hTokens)

	roundTrip := apicompat.AnthropicToResponsesResponse(anthropic)
	require.Equal(t, response.Usage, roundTrip.Usage)
	chat := apicompat.ResponsesToChatCompletions(roundTrip, response.Model)
	require.Equal(t, response.Usage.InputTokensDetails.CacheWriteTokensDetails, chat.Usage.PromptTokensDetails.CacheWriteTokensDetails)
	chatUsage := openAIUsageFromChatCompletionsResponse(chat)
	require.Equal(t, 7000, chatUsage.CacheCreationInputTokens)
	require.Equal(t, 3000, chatUsage.CacheCreation30mTokens)
	require.Equal(t, 4000, chatUsage.CacheCreation12hTokens)
	converted := chatCompletionsResponseToResponsesResponse(chat)
	require.Equal(t, response.Usage, converted.Usage)

	// The Anthropic and Chat streaming adapters must retain the same detail
	// object when the terminal Responses event supplies final token counts.
	event := &apicompat.ResponsesStreamEvent{Type: "response.completed", Response: response}
	anthropicEvents := apicompat.ResponsesEventToAnthropicEvents(event, apicompat.NewResponsesEventToAnthropicState())
	var streamed apicompat.AnthropicUsage
	for _, event := range anthropicEvents {
		if event.Usage != nil {
			streamed = *event.Usage
		}
	}
	require.Equal(t, anthropic.Usage, streamed)
	chatState := apicompat.NewResponsesEventToChatState()
	chatState.IncludeUsage = true
	chatEvents := apicompat.ResponsesEventToChatChunks(event, chatState)
	var chatStreamUsage *apicompat.ChatUsage
	for _, event := range chatEvents {
		if event.Usage != nil {
			chatStreamUsage = event.Usage
		}
	}
	require.Equal(t, chat.Usage, chatStreamUsage)
}

func TestOpenAIChatSSECacheWriteLifetimesAndLegacyAggregate(t *testing.T) {
	body := `data: {"id":"chat_cache","object":"chat.completion.chunk","model":"gpt-6.2-sol","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12000,"completion_tokens":11,"total_tokens":12011,"prompt_tokens_details":{"cached_tokens":4000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}` + "\n\ndata: [DONE]\n\n"
	chat, ok := extractChatCompletionsResponseFromSSEBody(body, "gpt-6.2-sol")
	require.True(t, ok)
	usage := openAIUsageFromChatCompletionsResponse(chat)
	require.Equal(t, 7000, usage.CacheCreationInputTokens)
	require.Equal(t, 3000, usage.CacheCreation30mTokens)
	require.Equal(t, 4000, usage.CacheCreation12hTokens)

	legacy, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":20,"output_tokens":1,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":7}}}`))
	require.True(t, ok)
	require.Equal(t, 7, legacy.CacheCreationInputTokens)
	require.Zero(t, legacy.CacheCreation30mTokens)
	require.Zero(t, legacy.CacheCreation12hTokens)
}

func TestOpenAIRecordUsageBillsActualCacheWriteLifetimesAndKeepsAggregateLogs(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":12000,"output_tokens":11,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`))
	require.True(t, ok)
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	ledger := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "resp_cache_lifetimes", Model: "gpt-6.2-sol", Usage: usage, Duration: time.Second},
		APIKey: &APIKey{ID: 1, UserID: 2, GroupID: i64p(3), Group: &Group{ID: 3, RateMultiplier: 1}},
		User:   &User{ID: 2}, Account: &Account{ID: 4},
	})
	require.NoError(t, err)
	require.NotNil(t, logs.lastLog)
	require.NotNil(t, ledger.lastCmd)
	// Standard Sol rates: ordinary $2/M, reads $0.10/M, 30m writes $2.50/M,
	// and 12h writes $5/M. The two write components replace aggregate writes.
	expected := (1000*2.0 + 4000*.1 + 3000*2.5 + 4000*5 + 11*10) / 1e6
	require.InDelta(t, expected, ledger.lastCmd.ActualCost, 1e-12)
	require.Equal(t, 7000, ledger.lastCmd.CacheCreationTokens)
	require.Equal(t, 7000, logs.lastLog.CacheCreationTokens)
	require.Equal(t, 1000, logs.lastLog.InputTokens)
}

func TestQwenCacheWriteLifetimesSurviveResponsesAndOfficialChatUsage(t *testing.T) {
	for _, aggregate := range []string{`"cache_write_tokens":7000,`, ""} {
		t.Run(fmt.Sprintf("aggregate=%t", aggregate != ""), func(t *testing.T) {
			body := []byte(`{"id":"resp_qwen_cache","status":"completed","usage":{"input_tokens":12000,"output_tokens":11,"input_tokens_details":{"cached_tokens":4000,` + aggregate + `"cache_write_tokens_details":{"ttl_5m_tokens":3000,"ttl_1h_tokens":4000}}}}`)
			event := []byte(`{"type":"response.completed","response":` + string(body) + `}`)
			parsers := map[string]func() OpenAIUsage{
				"HTTP JSON": func() OpenAIUsage {
					usage, ok := extractOpenAIUsageFromJSONBytes(body)
					require.True(t, ok)
					return usage
				},
				"HTTP SSE": func() OpenAIUsage {
					var usage OpenAIUsage
					(&OpenAIGatewayService{}).parseSSEUsageBytes(event, &usage)
					return usage
				},
				"WS terminal event": func() OpenAIUsage {
					var usage OpenAIUsage
					parseOpenAIWSResponseUsageFromCompletedEvent(event, &usage)
					return usage
				},
				"WS buffered response": func() OpenAIUsage {
					var usage OpenAIUsage
					populateOpenAIUsageFromResponseJSON(body, &usage)
					return usage
				},
				"typed response": func() OpenAIUsage {
					var response apicompat.ResponsesResponse
					require.NoError(t, json.Unmarshal(body, &response))
					return openAIUsageFromResponsesResponse(&response)
				},
			}
			for name, parse := range parsers {
				t.Run(name, func(t *testing.T) {
					usage := parse()
					require.Equal(t, 7000, usage.CacheCreationInputTokens)
					require.Equal(t, 3000, usage.CacheCreation5mTokens)
					require.Equal(t, 4000, usage.CacheCreation1hTokens)
					billed := openAIUsageBillingTokens(usage)
					require.Equal(t, 1000, billed.InputTokens)
					require.Equal(t, 3000, billed.CacheCreation5mTokens)
					require.Equal(t, 4000, billed.CacheCreation1hTokens)
				})
			}
		})
	}

	// The official Chat schema uses cache_creation with ephemeral TTL fields
	// and may repeat the aggregate under cache_creation_input_tokens.
	for _, aggregate := range []string{
		`"cache_write_tokens":7000,"cache_creation_input_tokens":7000,`,
		`"cache_creation_input_tokens":7000,`, "",
	} {
		body := []byte(`{"id":"chat_qwen_cache","model":"qwen4-max","choices":[],"usage":{"prompt_tokens":12000,"completion_tokens":11,"total_tokens":12011,"prompt_tokens_details":{"cached_tokens":4000,` + aggregate + `"cache_creation":{"ephemeral_5m_input_tokens":3000,"ephemeral_1h_input_tokens":4000}}}}`)
		var chat apicompat.ChatCompletionsResponse
		require.NoError(t, json.Unmarshal(body, &chat))
		usage := openAIUsageFromChatCompletionsResponse(&chat)
		require.Equal(t, 7000, usage.CacheCreationInputTokens, "aggregate aliases must not be added twice")
		require.Equal(t, 3000, usage.CacheCreation5mTokens)
		require.Equal(t, 4000, usage.CacheCreation1hTokens)
		converted := chatCompletionsResponseToResponsesResponse(&chat)
		require.Equal(t, usage, openAIUsageFromResponsesResponse(converted))
		anthropic := apicompat.ResponsesToAnthropic(converted, "qwen4-max")
		var claude ClaudeUsage
		mergeAnthropicUsage(&claude, anthropic.Usage)
		require.Equal(t, 1000, claude.InputTokens)
		require.Equal(t, 7000, claude.CacheCreationInputTokens)
		require.Equal(t, 3000, claude.CacheCreation5mTokens)
		require.Equal(t, 4000, claude.CacheCreation1hTokens)
	}
}

func TestOpenAIQwenCacheUsageUsesFrozenTTLRatesAndDisjointCentralMeters(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":12000,"output_tokens":11,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_5m_tokens":3000,"ttl_1h_tokens":4000}}}}`))
	require.True(t, ok)
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "qwen4-max", RateMultiplier: 2, ServiceTier: "default",
		Resolved: &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceFallback, BasePricing: &ModelPricing{
			InputPricePerToken: 1.5e-6, OutputPricePerToken: 4.5e-6, CacheReadPricePerToken: .15e-6,
			CacheCreationPricePerToken: 1.875e-6, CacheCreation5mPrice: 1.875e-6, CacheCreation1hPrice: 3e-6, SupportsCacheBreakdown: true,
		}}})
	require.NoError(t, err)
	store := &gatewayBillingStoreStub{op: bc.Operation{Key: bc.Key{OperationID: "qwen-frozen-cache"}, State: bc.Dispatched, ReservationID: "qwen-reservation", RemoteVersion: 2}}
	execution := &bc.Execution{Mode: "central", ProductKey: "ai:qwen4-max", GatewayPricingSnapshot: snapshot,
		Key: bc.Key{ProducerClientID: "gateway", OriginAppID: "test", OperationID: "qwen-frozen-cache"},
		OriginalMaximumUsage: map[string]bc.Decimal{
			"input_tokens": "12000", "output_tokens": "11", "cache_read_tokens": "4000", "cache_write_tokens": "7000",
			"cache_write_5m_tokens": "3000", "cache_write_1h_tokens": "4000", "image_output_tokens": "0", "request_count": "1", "image_count": "0",
		},
		Quote:       bc.Quote{Request: bc.ReserveRequest{ChargeMode: bc.ChargeModeActualUsage, MaximumUsage: map[string]bc.Decimal{gatewayCreditMeter: "0"}}},
		Coordinator: bc.Coordinator{Store: store},
	}
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	ledger := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	err = svc.RecordUsage(bc.WithExecution(context.Background(), execution), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "qwen-frozen-cache", Model: "qwen4-max", Usage: usage, Duration: time.Second},
		APIKey: &APIKey{ID: 1, UserID: 2, GroupID: i64p(3), Group: &Group{ID: 3, RateMultiplier: 99}},
		User:   &User{ID: 2}, Account: &Account{ID: 4},
	})
	require.NoError(t, err)
	require.NotNil(t, logs.lastLog)
	require.NotNil(t, ledger.lastCmd)
	expected := (1000*1.5 + 4000*.15 + 3000*1.875 + 4000*3 + 11*4.5) / 1e6 * 2
	require.InDelta(t, expected, ledger.lastCmd.ActualCost, 1e-12, "use the frozen multiplier and separate TTL prices, without ordinary-input or aggregate-write duplication")
	require.Equal(t, 1000, logs.lastLog.InputTokens)
	require.Equal(t, 7000, logs.lastLog.CacheCreationTokens)
	require.Equal(t, 3000, logs.lastLog.CacheCreation5mTokens)
	require.Equal(t, 4000, logs.lastLog.CacheCreation1hTokens)
	require.Equal(t, 3000, ledger.lastCmd.CacheCreation5mTokens)
	require.Equal(t, 4000, ledger.lastCmd.CacheCreation1hTokens)
	require.NotNil(t, ledger.lastCmd.CentralEvent)
	var settlement bc.SettleRequest
	require.NoError(t, json.Unmarshal(ledger.lastCmd.CentralEvent.Body, &settlement))
	actual, err := gatewayCreditDecimal(ledger.lastCmd.ActualCost, false)
	require.NoError(t, err)
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: actual}, settlement.Usage)
}
