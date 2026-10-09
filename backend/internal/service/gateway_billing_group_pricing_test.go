//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func noChannelCreditPricer() (*gatewayCreditPriceCalculator, *APIKey) {
	channels := &ChannelService{}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	channels.cache.Store(cache)
	billing := NewBillingService(nil, nil)
	gateway := &GatewayService{billingService: billing, channelService: channels, resolver: NewModelPricingResolver(channels, billing)}
	group := &Group{ID: 4, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: 2}
	return &gatewayCreditPriceCalculator{gateway: gateway}, &APIKey{UserID: 3, GroupID: &group.ID, Group: group}
}

func TestGatewayCreditsNoChannelUsesFrozenCataloguePrice(t *testing.T) {
	p, key := noChannelCreditPricer()
	quote, err := textBillingQuoteRequest("/v1/responses", "catalogue", []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), oidcBillingMultiplierContextKey{}, 10.0)
	maximum, raw, err := p.MaximumCredit(ctx, key, "/v1/responses", quote)
	require.NoError(t, err)
	require.NotEmpty(t, maximum)
	var snapshot gatewayCreditPriceSnapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, key.Group.ID, snapshot.EffectiveGroupID)
	require.Equal(t, PlatformOpenAI, snapshot.EffectivePlatform)
	require.Equal(t, "gpt-6-sol", snapshot.BillingModel)
	require.Equal(t, 20.0, snapshot.RateMultiplier)
	require.NotNil(t, snapshot.Resolved.BasePricing)
	key.Group.RateMultiplier = 999
	p.gateway.billingService.fallbackPrices["gpt-6-sol"].InputPricePerToken = 999
	cost, model, rate, err := p.gateway.frozenGatewayCreditCost(context.Background(), &bc.Execution{GatewayPricingSnapshot: raw},
		&ForwardResult{Model: "gpt-6-luna", UpstreamModel: "different-upstream", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 20}}, &recordUsageOpts{})
	require.NoError(t, err)
	require.Equal(t, "gpt-6-sol", model)
	require.Equal(t, 20.0, rate)
	require.InDelta(t, (snapshot.Resolved.BasePricing.InputPricePerToken*10+snapshot.Resolved.BasePricing.OutputPricePerToken*20)*rate, cost.ActualCost, 1e-12)
}

func TestGatewayCreditsNoChannelStillRejectsUnknownPriceAndChannelFailures(t *testing.T) {
	p, key := noChannelCreditPricer()
	quote, err := textBillingQuoteRequest("/v1/responses", "catalogue", []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`))
	require.NoError(t, err)
	quote.ProductKey = "ai:unknown-credit-model"
	_, _, err = p.MaximumCredit(context.Background(), key, "/v1/responses", quote)
	require.ErrorIs(t, err, bc.ErrState)
	quote.ProductKey = "ai:gpt-6-sol"
	reads := 0
	p.gateway.channelService = NewChannelService(&mockChannelRepository{listAllFn: func(context.Context) ([]Channel, error) {
		reads++
		return nil, errors.New("database unavailable")
	}}, nil)
	for i := 0; i < 2; i++ {
		_, _, err = p.MaximumCredit(context.Background(), key, "/v1/responses", quote)
		require.ErrorIs(t, err, bc.ErrState)
	}
	require.Equal(t, 1, reads, "the short error cache must not become an absent channel")
	p, key = noChannelCreditPricer()
	cache, err := p.gateway.channelService.loadCache(context.Background())
	require.NoError(t, err)
	cache.channelByGroupID[key.Group.ID] = &Channel{ID: 1, Status: StatusActive, BillingModelSource: BillingModelSourceUpstream}
	_, _, err = p.MaximumCredit(context.Background(), key, "/v1/responses", quote)
	require.ErrorIs(t, err, bc.ErrState)
}

func TestGatewayPricingIdentityProtectsGroupAndPlatform(t *testing.T) {
	_, key := noChannelCreditPricer()
	raw, err := json.Marshal(gatewayCreditPriceSnapshot{EffectiveGroupID: key.Group.ID, EffectivePlatform: PlatformOpenAI})
	require.NoError(t, err)
	require.NoError(t, validateGatewayPricingIdentity(raw, key))
	key.Group.Platform = PlatformAnthropic
	require.ErrorIs(t, validateGatewayPricingIdentity(raw, key), bc.ErrConflict)
	key.Group.Platform = PlatformOpenAI
	key.Group.ID = 9
	require.ErrorIs(t, validateGatewayPricingIdentity(raw, key), bc.ErrConflict)
	require.ErrorIs(t, validateGatewayPricingIdentity(raw, nil), bc.ErrConflict)
	require.NoError(t, validateGatewayPricingIdentity(json.RawMessage(`{"billing_model":"legacy","rate_multiplier":1}`), nil))
}

func TestGatewayBillingRetryRejectsChangedEffectiveGroupBeforeReserve(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	p, key := noChannelCreditPricer()
	s.creditPricer = p
	body := []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`)
	_, err := s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "same-group", body, key)
	require.NoError(t, err)
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "same-group", body, key)
	require.NoError(t, err)
	key.Group.Platform = PlatformAnthropic
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "same-group", body, key)
	require.ErrorIs(t, err, bc.ErrConflict)
	key.Group.Platform = PlatformOpenAI
	key.Group.ID = 9
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "same-group", body, key)
	require.ErrorIs(t, err, bc.ErrConflict)
	require.Equal(t, 1, authority.quoteCalls)
	require.Equal(t, 1, authority.reserves)
}
