package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestGatewayMaximumCreditCoversContextAndCachePriceBands(t *testing.T) {
	billing := &BillingService{}
	base := &ModelPricing{InputPricePerToken: 0.1, OutputPricePerToken: 0.2,
		OutputPricePerTokenPriority: 0.9,
		CacheReadPricePerToken:      0.05, CacheCreationPricePerToken: 0.15,
		LongContextInputThreshold: 100, LongContextInputMultiplier: 2,
		LongContextOutputMultiplier: 3}
	highInput := 0.4
	resolved := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel, BasePricing: base,
		Intervals: []PricingInterval{{InputPrice: &highInput}}}
	usage := map[string]bc.Decimal{
		"input_tokens": "100", "output_tokens": "50", "cache_read_tokens": "100",
		"cache_write_tokens": "100", "cache_write_5m_tokens": "100",
		"cache_write_1h_tokens": "100", "image_output_tokens": "0",
	}
	maximum, err := maximumGatewayTokenCost(billing, resolved, "example", usage, "default", 2)
	require.NoError(t, err)
	require.Greater(t, maximum, 0.0)
	for _, price := range []*ModelPricing{base, intervalToModelPricing(&resolved.Intervals[0], base, false)} {
		for _, longContext := range []bool{false, true} {
			actual := billing.computeTokenBreakdown(price, UsageTokens{
				InputTokens: 40, OutputTokens: 20, CacheReadTokens: 40,
				CacheCreationTokens: 20,
			}, 2, "priority", longContext)
			require.LessOrEqual(t, actual.ActualCost, maximum)
		}
	}
	credit, err := gatewayCreditDecimal(maximum, true)
	require.NoError(t, err)
	require.NotEmpty(t, credit)
}

func TestGatewayCreditAmountsRejectMissingBoundsAndInvalidNumbers(t *testing.T) {
	_, err := maximumGatewayTokenCost(&BillingService{}, &ResolvedPricing{Mode: BillingModeToken,
		BasePricing: &ModelPricing{InputPricePerToken: 1}}, "example",
		map[string]bc.Decimal{"input_tokens": "10"}, "default", 1)
	require.ErrorIs(t, err, bc.ErrConflict)
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, err := gatewayCreditDecimal(value, true)
		require.ErrorIs(t, err, bc.ErrConflict)
	}
	actual, err := gatewayCreditDecimal(0.1+0.2, false)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("0.3"), actual)
	tiny, err := gatewayCreditDecimal(0.00000000001, false)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("0.0000000001"), tiny)
}

func TestFrozenGatewayCreditPriceSurvivesLivePriceChange(t *testing.T) {
	price := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel,
		BasePricing: &ModelPricing{InputPricePerToken: .4, OutputPricePerToken: .8}}
	bound := map[string]bc.Decimal{"input_tokens": "100", "output_tokens": "50", "cache_read_tokens": "100",
		"cache_write_tokens": "100", "cache_write_5m_tokens": "100", "cache_write_1h_tokens": "100",
		"image_output_tokens": "0"}
	maximum, err := maximumGatewayTokenCost(&BillingService{}, price, "expensive-mapped", bound, "default", 1)
	require.NoError(t, err)
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "expensive-mapped", Resolved: price, RateMultiplier: 1, ServiceTier: "default"})
	require.NoError(t, err)
	// A later admin edit must not turn the reserved quote into a different bill.
	price.BasePricing.InputPricePerToken = 100
	svc := &GatewayService{billingService: &BillingService{}, resolver: &ModelPricingResolver{}}
	cost, model, _, err := svc.frozenGatewayCreditCost(context.Background(), &bc.Execution{GatewayPricingSnapshot: snapshot},
		&ForwardResult{Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 20}}, &recordUsageOpts{})
	require.NoError(t, err)
	require.Equal(t, "expensive-mapped", model)
	require.InDelta(t, 20, cost.ActualCost, 1e-9)
	require.LessOrEqual(t, cost.ActualCost, maximum)
}

func TestFrozenGatewayImagePriceAndFreeCredits(t *testing.T) {
	unit := .25
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "image-model", RateMultiplier: 1, ImageUnitPrice: &unit})
	require.NoError(t, err)
	svc := &GatewayService{billingService: &BillingService{}, resolver: &ModelPricingResolver{}}
	cost, _, _, err := svc.frozenGatewayCreditCost(context.Background(), &bc.Execution{ProductKey: "ai:image-model:images", GatewayPricingSnapshot: snapshot},
		&ForwardResult{ImageCount: 3}, &recordUsageOpts{})
	require.NoError(t, err)
	require.Equal(t, .75, cost.ActualCost)
	freeSnapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "free-model", RateMultiplier: 0,
		Resolved: &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: 1}}})
	require.NoError(t, err)
	free, _, _, err := svc.frozenGatewayCreditCost(context.Background(), &bc.Execution{GatewayPricingSnapshot: freeSnapshot},
		&ForwardResult{Usage: ClaudeUsage{InputTokens: 2}}, &recordUsageOpts{})
	require.NoError(t, err)
	require.Zero(t, free.ActualCost)
}

func TestGatewayCreditTextChecksRetainOriginalTokenCaps(t *testing.T) {
	e := &bc.Execution{Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{gatewayCreditMeter: "20"}}}}
	_, err := gatewayTextBounds(e)
	require.ErrorIs(t, err, bc.ErrState)
	e.OriginalMaximumUsage = map[string]bc.Decimal{"input_tokens": "200", "output_tokens": "10"}
	bounds, err := gatewayTextBounds(e)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("10"), bounds["output_tokens"])
}

func TestGatewayCreditImageChecksRetainOriginalCountAndTier(t *testing.T) {
	unit := .25
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "gpt-image-2", ServiceTier: "1024x1024:high", ImageUnitPrice: &unit})
	require.NoError(t, err)
	e := &bc.Execution{Mode: "central", ProductKey: "ai:gpt-image-2:images", OriginalMaximumUsage: map[string]bc.Decimal{"request_count": "1", "image_count": "2"},
		GatewayPricingSnapshot: snapshot, Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{gatewayCreditMeter: "0.5"}, ServiceTier: "default"}}}
	ctx := bc.WithExecution(context.Background(), e)
	require.NoError(t, ValidateBillingImageRequest(ctx, []byte(`{"model":"gpt-image-2","size":"1024x1024","quality":"high","n":2}`)))
	require.Error(t, ValidateBillingImageRequest(ctx, []byte(`{"model":"gpt-image-2","size":"1024x1024","quality":"high","n":3}`)))
}

func TestGatewayImagePricingDistinguishesConfiguredFreeFromMissing(t *testing.T) {
	zero := 0.0
	group := &Group{ImagePrice1K: &zero, ImagePrice2K: &zero, ImagePrice4K: &zero}
	prices, err := gatewayImageUnitPrices(&BillingService{}, "unknown-image", group)
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"1K": 0, "2K": 0, "4K": 0}, prices)
	group.ImagePrice4K = nil
	_, err = gatewayImageUnitPrices(&BillingService{}, "unknown-image", group)
	require.ErrorIs(t, err, bc.ErrState)
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"known-image": {OutputCostPerImage: .2}}}
	prices, err = gatewayImageUnitPrices(&BillingService{pricingService: pricing}, "known-image", &Group{})
	require.NoError(t, err)
	require.InDelta(t, .2, prices["1K"], 1e-9)
	require.InDelta(t, .3, prices["2K"], 1e-9)
	require.InDelta(t, .4, prices["4K"], 1e-9)
}
