//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestGatewayManualPerRequestTextPriceWorksWithoutAutomaticBasePrice(t *testing.T) {
	pricer, key := noChannelCreditPricer()
	pricer.gateway.billingService.fallbackPrices = map[string]*ModelPricing{}
	policies, _ := newAdminPolicyFixture()
	enabled := true
	_, err := policies.Update(context.Background(), AdminModelPolicyUpdate{Model: "gpt-6-sol", Enabled: &enabled,
		Pricing: &AdminModelPrice{BillingMode: BillingModePerRequest, PriceUnit: "request", UnitPrice: mediaPricePtr(.75)}})
	require.NoError(t, err)
	pricer.gateway.resolver.SetAdminModelPolicies(policies)
	quote, err := textBillingQuoteRequest("/v1/responses", "manual-per-request", []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`))
	require.NoError(t, err)
	maximum, raw, err := pricer.MaximumCredit(context.Background(), key, "/v1/responses", quote)
	require.NoError(t, err)
	expected, err := gatewayCreditDecimal(.75*key.Group.RateMultiplier, true)
	require.NoError(t, err)
	require.Equal(t, expected, maximum)
	var snapshot gatewayCreditPriceSnapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, PricingSourceManual, snapshot.Resolved.Source)
	require.Nil(t, snapshot.Resolved.BasePricing)
	// The administrator's default is a complete per-request price, not an
	// invitation to reload an unrelated token catalogue at settlement time.
	cost, _, _, err := pricer.gateway.frozenGatewayCreditCost(context.Background(),
		&bc.Execution{GatewayPricingSnapshot: raw}, &ForwardResult{Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 20}}, &recordUsageOpts{})
	require.NoError(t, err)
	require.InDelta(t, 1.5, cost.ActualCost, 1e-12)
}

func TestGatewayManualImageAndPerRequestPricesAreFrozenIncludingExplicitFree(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
		for _, unit := range []float64{.25, 0} {
			t.Run(string(mode)+"/"+stringMustJSONForManualPrice(unit), func(t *testing.T) {
				pricer, key := noChannelCreditPricer()
				policies, _ := newAdminPolicyFixture()
				enabled := true
				priceUnit := "image"
				if mode == BillingModePerRequest {
					priceUnit = "request"
				}
				_, err := policies.Update(context.Background(), AdminModelPolicyUpdate{Model: "manual-image", Enabled: &enabled,
					Pricing: &AdminModelPrice{BillingMode: mode, PriceUnit: priceUnit, UnitPrice: mediaPricePtr(unit)}})
				require.NoError(t, err)
				pricer.gateway.resolver.SetAdminModelPolicies(policies)
				quote := bc.QuoteRequest{ProductKey: "ai:manual-image:images", ServiceTier: "default", MaximumUsage: map[string]bc.Decimal{"image_count": "2", "request_count": "1"}}
				maximum, raw, err := pricer.MaximumCredit(context.Background(), key, "/v1/images/generations", quote)
				require.NoError(t, err)
				quantity := 2.0
				if mode == BillingModePerRequest {
					quantity = 1
				}
				expected, err := gatewayCreditDecimal(unit*quantity*key.Group.RateMultiplier, true)
				require.NoError(t, err)
				require.Equal(t, expected, maximum)
				var snapshot gatewayCreditPriceSnapshot
				require.NoError(t, json.Unmarshal(raw, &snapshot))
				require.NotNil(t, snapshot.ImageUnitPrice)
				require.Equal(t, unit, *snapshot.ImageUnitPrice)
				require.Equal(t, mode, snapshot.ImageBillingMode)
				require.Nil(t, snapshot.ImageUnitPrices, "a manual base price must not fall back to group image defaults")
				_, err = policies.Update(context.Background(), AdminModelPolicyUpdate{Model: "manual-image", Enabled: &enabled,
					Pricing: &AdminModelPrice{BillingMode: mode, PriceUnit: priceUnit, UnitPrice: mediaPricePtr(99)}})
				require.NoError(t, err)
				cost, _, _, err := pricer.gateway.frozenGatewayCreditCost(context.Background(), &bc.Execution{ProductKey: "ai:manual-image:images", GatewayPricingSnapshot: raw},
					&ForwardResult{ImageCount: 2, ImageSize: "1K"}, &recordUsageOpts{})
				require.NoError(t, err)
				require.InDelta(t, unit*quantity*key.Group.RateMultiplier, cost.ActualCost, 1e-12)
				require.Equal(t, string(mode), cost.BillingMode)
			})
		}
	}
}

func TestGatewayImagePolicyReadFailureCannotUseLegacyGroupPrice(t *testing.T) {
	pricer, key := noChannelCreditPricer()
	policies, store := newAdminPolicyFixture()
	store.fail = errors.New("model policy read failed")
	pricer.gateway.resolver.SetAdminModelPolicies(policies)
	key.Group.ImagePrice1K, key.Group.ImagePrice2K, key.Group.ImagePrice4K = mediaPricePtr(.5), mediaPricePtr(.75), mediaPricePtr(1)
	_, _, err := pricer.MaximumCredit(context.Background(), key, "/v1/images/generations", bc.QuoteRequest{
		ProductKey: "ai:manual-image:images", ServiceTier: "default", MaximumUsage: map[string]bc.Decimal{"image_count": "1", "request_count": "1"},
	})
	require.ErrorIs(t, err, bc.ErrState)
	require.ErrorIs(t, err, store.fail)
}

func TestGatewayIncompletePerRequestPriceIsNotMistakenForConfiguredFree(t *testing.T) {
	usage := map[string]bc.Decimal{"input_tokens": "10", "output_tokens": "20", "request_count": "1", "cache_read_tokens": "0", "cache_write_tokens": "0", "cache_write_5m_tokens": "0", "cache_write_1h_tokens": "0", "image_output_tokens": "0"}
	resolved := &ResolvedPricing{Mode: BillingModePerRequest, Source: PricingSourceManual,
		RequestTiers: []PricingInterval{{InputPrice: mediaPricePtr(1)}}}
	_, err := maximumGatewayTokenCost(&BillingService{}, resolved, "model", usage, "default", 1)
	require.ErrorIs(t, err, bc.ErrState)
	resolved.DefaultPriceConfigured = true
	price, err := maximumGatewayTokenCost(&BillingService{}, resolved, "model", usage, "default", 1)
	require.NoError(t, err)
	require.Zero(t, price)
	resolved.Error = errors.New("policy unavailable")
	_, err = maximumGatewayTokenCost(&BillingService{}, resolved, "model", usage, "default", 1)
	require.ErrorIs(t, err, bc.ErrState)
	require.ErrorIs(t, err, resolved.Error)
}

func TestGatewayFrozenManualPriceDoesNotReapplyProviderLongContextSplit(t *testing.T) {
	pricing := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceManual, BasePricing: &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2}}
	raw, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "manual-model", RateMultiplier: 2, Resolved: pricing})
	require.NoError(t, err)
	svc := &GatewayService{billingService: &BillingService{}, resolver: &ModelPricingResolver{}}
	cost, _, _, err := svc.frozenGatewayCreditCost(context.Background(), &bc.Execution{GatewayPricingSnapshot: raw},
		&ForwardResult{Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 10}}, &recordUsageOpts{LongContextThreshold: 20, LongContextMultiplier: 2})
	require.NoError(t, err)
	require.InDelta(t, 24, cost.ActualCost, 1e-12, "the administrator's frozen rate must not receive an extra provider-derived split")
}

func stringMustJSONForManualPrice(value float64) string {
	b, _ := json.Marshal(value)
	return string(b)
}
