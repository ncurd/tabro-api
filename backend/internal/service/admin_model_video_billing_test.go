//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func adminVideoResolutionPricing() *AdminModelPrice {
	return &AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{
		{Resolution: "480p", UnitPrice: mediaPricePtr(.02)},
		{Resolution: "720P", UnitPrice: mediaPricePtr(0)},
		{Resolution: "1080P", UnitPrice: mediaPricePtr(.08)},
	}}
}

func saveAdminVideoPrice(t *testing.T, policies *AdminModelPolicyService, model string, price *AdminModelPrice) {
	t.Helper()
	enabled := true
	_, err := policies.Update(context.Background(), AdminModelPolicyUpdate{Model: model, Enabled: &enabled, Pricing: price})
	require.NoError(t, err)
}

func TestAdminVideoResolutionPricesReachResolverAndUserPricing(t *testing.T) {
	ctx := context.Background()
	policies, _ := newAdminPolicyFixture()
	saveAdminVideoPrice(t, policies, "video-public", adminVideoResolutionPricing())

	runtime, err := policies.GetPricing(ctx, "video-public")
	require.NoError(t, err)
	require.Equal(t, BillingModeVideo, runtime.BillingMode)
	require.Equal(t, "second", runtime.PriceUnit)
	require.Nil(t, runtime.PerRequestPrice, "resolution-only prices must not create a free default")
	require.Equal(t, []PricingInterval{
		{TierLabel: "480P", PerRequestPrice: mediaPricePtr(.02)},
		{TierLabel: "720P", PerRequestPrice: mediaPricePtr(0)},
		{TierLabel: "1080P", PerRequestPrice: mediaPricePtr(.08)},
	}, runtime.Intervals)

	pricer, _ := noChannelCreditPricer()
	resolver := pricer.gateway.resolver
	resolver.SetAdminModelPolicies(policies)
	resolved := resolver.Resolve(ctx, PricingInput{Model: "video-public"})
	require.NoError(t, resolved.Error)
	require.Equal(t, PricingSourceManual, resolved.Source)
	require.Equal(t, BillingModeVideo, resolved.Mode)
	require.False(t, resolved.DefaultPriceConfigured)
	require.Equal(t, runtime.Intervals, resolved.RequestTiers)

	raw, err := json.Marshal(resolved)
	require.NoError(t, err)
	var frozen ResolvedPricing
	require.NoError(t, json.Unmarshal(raw, &frozen))
	require.Equal(t, resolved.RequestTiers, frozen.RequestTiers)
	require.False(t, frozen.DefaultPriceConfigured)

	page := NewModelPricingPageService(nil, nil, resolver, nil)
	row := page.resolveModelPricing(ctx, 1, "video-public", 3)
	require.NoError(t, row.Error)
	require.True(t, row.PricingAvailable)
	require.Equal(t, BillingModeVideo, row.BillingMode)
	require.Equal(t, "second", row.PriceUnit)
	require.Nil(t, row.UnitPrice)
	require.Equal(t, []AvailableModelPricingTier{
		{Label: "480P", UnitPrice: .02 * 3},
		{Label: "720P", UnitPrice: 0},
		{Label: "1080P", UnitPrice: .08 * 3},
	}, row.Tiers)
}

func TestAdminVideoResolutionBillingUsesActualSecondsAndFrozenPolicy(t *testing.T) {
	for _, tc := range []struct {
		resolution string
		sr         int
		price      float64
	}{{"480P", 480, .02}, {"720P", 720, 0}, {"1080P", 1080, .08}} {
		t.Run(tc.resolution, func(t *testing.T) {
			ctx := context.Background()
			policies, _ := newAdminPolicyFixture()
			saveAdminVideoPrice(t, policies, "video-public", adminVideoResolutionPricing())
			svc, channel, ledger, logs, meta, account := mediaBillingFixture()
			channel.price, channel.channel, channel.mapping = nil, nil, ChannelMappingResult{}
			svc.SetAdminModelPolicies(policies)
			snapshot, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, tc.resolution)
			require.NoError(t, err)
			require.Nil(t, snapshot.Price.Default)
			unit, err := snapshot.Price.UnitPrice(tc.resolution)
			require.NoError(t, err)
			require.Equal(t, tc.price, unit)

			// A new policy must affect the next request, while the already
			// persisted job keeps its original resolution rates and multipliers.
			saveAdminVideoPrice(t, policies, "video-public", &AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: mediaPricePtr(99)})
			nextSnapshot, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, tc.resolution)
			require.NoError(t, err)
			require.Equal(t, 99.0, *nextSnapshot.Price.Default)
			job := billingTestJob(snapshot)
			job.UpstreamResponseJSON = []byte(fmt.Sprintf(`{"usage":{"output_video_duration":2.5,"duration":99,"SR":%d}}`, tc.sr))
			require.NoError(t, svc.Settle(ctx, job))
			cmd := ledger.commands[job.PublicID]
			require.InDelta(t, tc.price*2.5, cmd.TotalCost, 1e-12)
			require.InDelta(t, tc.price*2.5*1.5, cmd.BalanceCost, 1e-12)
			require.InDelta(t, tc.price*2.5*.8, *logs.logs[job.PublicID].AccountStatsCost, 1e-12)
			require.Equal(t, tc.resolution, *logs.logs[job.PublicID].BillingTier)
			require.Equal(t, 2500, *logs.logs[job.PublicID].DurationMs)
		})
	}
}

func TestAdminVideoMissingResolutionFailsClosedAndExplicitDefaultFallsBack(t *testing.T) {
	ctx := context.Background()
	policies, _ := newAdminPolicyFixture()
	price := &AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{
		{Resolution: "720P", UnitPrice: mediaPricePtr(.1)},
	}}
	saveAdminVideoPrice(t, policies, "video-public", price)
	svc, channel, ledger, _, meta, account := mediaBillingFixture()
	channel.price, channel.channel, channel.mapping = nil, nil, ChannelMappingResult{}
	svc.SetAdminModelPolicies(policies)
	_, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "1080P")
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
	require.Zero(t, ledger.applied)

	snapshot, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	job := billingTestJob(snapshot)
	job.UpstreamResponseJSON = []byte(`{"usage":{"output_video_duration":5.5,"SR":1080}}`)
	require.ErrorIs(t, svc.Settle(ctx, job), ErrMediaUsageIncomplete)
	require.Zero(t, ledger.applied, "an unpriced actual resolution must never be recorded as free")

	price.UnitPrice = mediaPricePtr(.3)
	saveAdminVideoPrice(t, policies, "video-public", price)
	snapshot, err = svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "1080P")
	require.NoError(t, err)
	job = billingTestJob(snapshot)
	job.UpstreamResponseJSON = []byte(`{"usage":{"output_video_duration":5.5,"SR":1080}}`)
	require.NoError(t, svc.Settle(ctx, job))
	require.InDelta(t, .3*5.5, ledger.commands[job.PublicID].TotalCost, 1e-12)
}

func TestAdminVideoResolutionChannelPriceRemainsAuthoritative(t *testing.T) {
	ctx := context.Background()
	policies, _ := newAdminPolicyFixture()
	saveAdminVideoPrice(t, policies, "video-public", adminVideoResolutionPricing())
	channelPrice := ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"video-public"}, BillingMode: BillingModeVideo,
		Intervals: []PricingInterval{{TierLabel: "720P", PerRequestPrice: mediaPricePtr(.2)}}}
	resolver := newResolverWithChannel(t, []ChannelModelPricing{channelPrice})
	resolver.SetAdminModelPolicies(policies)
	resolved := resolver.Resolve(ctx, PricingInput{Model: "video-public", GroupID: groupIDPtr()})
	require.NoError(t, resolved.Error)
	require.Equal(t, PricingSourceChannel, resolved.Source)
	require.Equal(t, channelPrice.Intervals, resolved.RequestTiers)
	require.False(t, resolved.DefaultPriceConfigured)

	svc, channel, _, _, meta, account := mediaBillingFixture()
	channel.price = &channelPrice
	svc.SetAdminModelPolicies(policies)
	_, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "480P")
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured, "an incomplete channel rule must not inherit administrator resolution prices")
	snapshot, err := svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	unit, err := snapshot.Price.UnitPrice("720P")
	require.NoError(t, err)
	require.Equal(t, .2, unit, "the channel tier overrides the administrator's explicit zero")
	channel.price = &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: mediaPricePtr(0)}
	snapshot, err = svc.Prepare(ctx, meta, account, "video-public", "wan3.0-video", MediaJobKindVideoGeneration, "1080P")
	require.NoError(t, err)
	unit, err = snapshot.Price.UnitPrice("1080P")
	require.NoError(t, err)
	require.Zero(t, unit, "an explicit free channel price overrides a paid administrator tier")
}

func TestAdminVideoResolutionMaximumCreditsUseTheSelectedFrozenRate(t *testing.T) {
	for _, tc := range []struct {
		resolution string
		price      float64
	}{{"480P", .02}, {"720P", 0}, {"1080P", .08}} {
		t.Run(tc.resolution, func(t *testing.T) {
			ctx := context.Background()
			policies, _ := newAdminPolicyFixture()
			saveAdminVideoPrice(t, policies, "video-public", adminVideoResolutionPricing())
			pricer, key := noChannelCreditPricer()
			pricer.gateway.SetAdminModelPolicies(policies)
			quote := bc.QuoteRequest{ProductKey: "ai:video-public", ServiceTier: tc.resolution,
				MaximumUsage: map[string]bc.Decimal{"request_count": "1", "video_seconds": "10"}}
			maximum, raw, err := pricer.gateway.maximumMediaCredit(ctx, "/v1/videos/generations", quote, "video-public", *key.GroupID, 3)
			require.NoError(t, err)
			expected, err := gatewayCreditDecimal(tc.price*10*3, true)
			require.NoError(t, err)
			require.Equal(t, expected, maximum)
			var frozen gatewayCreditPriceSnapshot
			require.NoError(t, json.Unmarshal(raw, &frozen))
			require.Equal(t, tc.resolution, frozen.MediaTier)
			require.Equal(t, 3.0, frozen.RateMultiplier)
			require.NotNil(t, frozen.MediaPrice)
			require.Nil(t, frozen.MediaPrice.Default)

			saveAdminVideoPrice(t, policies, "video-public", &AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: mediaPricePtr(99)})
			unit, err := frozen.MediaPrice.UnitPrice(tc.resolution)
			require.NoError(t, err)
			require.Equal(t, tc.price, unit)
		})
	}
}
