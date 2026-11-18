//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func gpt62CacheCreditQuote(t *testing.T) (*gatewayCreditPriceCalculator, bc.Decimal, json.RawMessage) {
	t.Helper()
	pricer, key := noChannelCreditPricer()
	// Fix the supplier's promotional multiplier so this cache regression does
	// not depend on the calendar date when the tests run.
	pricer.gateway.billingService.fallbackPrices["gpt-6.2-astra"].UltrafastMultiplier = 3
	maximum, snapshot, err := pricer.MaximumCredit(context.Background(), key, "/v1/responses", bc.QuoteRequest{
		ProductKey: "ai:gpt-6.2-astra", ServiceTier: "default",
		MaximumUsage: map[string]bc.Decimal{
			"input_tokens": "600", "output_tokens": "20", "cache_read_tokens": "40",
			"cache_write_tokens": "300", "cache_write_5m_tokens": "0", "cache_write_1h_tokens": "0",
			"image_output_tokens": "0", "image_count": "0", "request_count": "1",
		},
	})
	require.NoError(t, err)
	return pricer, maximum, snapshot
}

func TestGPT62MaximumCreditIncludesTwelveHourCacheWriteEnvelope(t *testing.T) {
	pricer, maximum, snapshot := gpt62CacheCreditQuote(t)
	bound, err := strconv.ParseFloat(string(maximum), 64)
	require.NoError(t, err)
	// All aggregate writes may be the more expensive 12-hour writes, and the
	// upstream can select Ultrafast despite a default-tier request.
	expected := (600*8e-6 + 20*40e-6 + 40*.4e-6 + 300*20e-6) * 3 * 2
	require.InDelta(t, expected, bound, 1e-9)
	for _, writes := range []struct {
		name        string
		short, long int
	}{
		{name: "mixed TTLs", short: 100, long: 200},
		{name: "all twelve hour", long: 300},
	} {
		t.Run(writes.name, func(t *testing.T) {
			cost, model, rate, err := pricer.gateway.frozenGatewayCreditCost(context.Background(),
				&bc.Execution{GatewayPricingSnapshot: snapshot}, &ForwardResult{Usage: ClaudeUsage{
					InputTokens: 600, OutputTokens: 20, CacheReadInputTokens: 40,
					CacheCreationInputTokens: 300, CacheCreation30mTokens: writes.short, CacheCreation12hTokens: writes.long,
					Speed: "ultrafast",
				}}, &recordUsageOpts{})
			require.NoError(t, err)
			require.Equal(t, "gpt-6.2-astra", model)
			require.Equal(t, 2.0, rate)
			require.InDelta(t, (float64(writes.short)*10e-6+float64(writes.long)*20e-6)*3, cost.CacheCreationCost, 1e-12)
			require.LessOrEqual(t, cost.ActualCost, bound)
		})
	}
}

func TestGPT62FrozenCreditCostChargesMixedCacheTTLsOnce(t *testing.T) {
	pricer, _, snapshot := gpt62CacheCreditQuote(t)
	for _, tc := range []struct {
		name      string
		aggregate int
		cacheCost float64
	}{
		{name: "complete TTL breakdown", aggregate: 300, cacheCost: 100*10e-6 + 200*20e-6},
		// Unclassified writes use the normal 30-minute rate, rather than
		// charging the aggregate again in addition to its detailed buckets.
		{name: "partial TTL breakdown", aggregate: 350, cacheCost: 150*10e-6 + 200*20e-6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost, _, _, err := pricer.gateway.frozenGatewayCreditCost(context.Background(),
				&bc.Execution{GatewayPricingSnapshot: snapshot}, &ForwardResult{Usage: ClaudeUsage{
					InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 20,
					CacheCreationInputTokens: tc.aggregate, CacheCreation30mTokens: 100, CacheCreation12hTokens: 200,
				}}, &recordUsageOpts{})
			require.NoError(t, err)
			expectedTotal := 10*8e-6 + 5*40e-6 + 20*.4e-6 + tc.cacheCost
			require.InDelta(t, tc.cacheCost, cost.CacheCreationCost, 1e-12)
			require.InDelta(t, expectedTotal, cost.TotalCost, 1e-12)
			require.InDelta(t, expectedTotal*2, cost.ActualCost, 1e-12)
		})
	}
}

func TestGPT62CentralSettlementUsesMixedCacheCostAndRetainsAggregateWriteBound(t *testing.T) {
	coordinator, repo, authority, principal := gatewayBillingFixture()
	pricer, key := noChannelCreditPricer()
	coordinator.creditPricer = pricer
	execution, err := coordinator.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "gpt62-cache",
		[]byte(`{"model":"gpt-6.2-astra","max_output_tokens":20}`), key)
	require.NoError(t, err)
	require.Equal(t, gatewayCreditProductKey, authority.quoted.ProductKey)
	ctx := bc.WithExecution(context.Background(), execution)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	usage := ClaudeUsage{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 20,
		CacheCreationInputTokens: 300, CacheCreation30mTokens: 100, CacheCreation12hTokens: 200}
	cost, _, _, err := pricer.gateway.frozenGatewayCreditCost(ctx, execution, &ForwardResult{Usage: usage}, &recordUsageOpts{})
	require.NoError(t, err)
	expectedTotal := 10*8e-6 + 5*40e-6 + 20*.4e-6 + 100*10e-6 + 200*20e-6
	require.InDelta(t, expectedTotal*key.Group.RateMultiplier, cost.ActualCost, 1e-12)
	cmd := &UsageBillingCommand{RequestID: "gpt62-cache", InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadInputTokens, CacheCreationTokens: usage.CacheCreationInputTokens, ActualCost: cost.ActualCost}
	meters, err := BillingCenterUsage(cmd)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("300"), meters["cache_write_tokens"])
	require.Equal(t, bc.Decimal("0"), meters["cache_write_5m_tokens"])
	require.Equal(t, bc.Decimal("0"), meters["cache_write_1h_tokens"])
	require.NoError(t, attachCentralUsage(ctx, cmd))
	require.NotNil(t, cmd.CentralEvent)
	var settlement bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &settlement))
	actual, err := gatewayCreditDecimal(cost.ActualCost, false)
	require.NoError(t, err)
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: actual}, settlement.Usage,
		"the central authority settles the computed credits once, without charging cache token meters again")
	require.True(t, settlement.UsageComplete)

	writeBound, err := strconv.Atoi(string(execution.OriginalMaximumUsage["cache_write_tokens"]))
	require.NoError(t, err)
	tooManyWrites := &UsageBillingCommand{RequestID: "gpt62-cache-over-bound", CacheCreationTokens: writeBound + 1, ActualCost: cost.ActualCost}
	require.ErrorIs(t, attachCentralUsage(ctx, tooManyWrites), bc.ErrState)
	require.Nil(t, tooManyWrites.CentralEvent, "cheap credits cannot bypass the original aggregate cache-write bound")
}
