//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminVideoResolutionPricesPersistAndPreserveExplicitFree(t *testing.T) {
	policies, store := newAdminPolicyFixture()
	ctx := context.Background()
	enabled := false
	price := &AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{
		{Resolution: "480P", UnitPrice: testPtrFloat64(0)},
		{Resolution: " 720p ", UnitPrice: testPtrFloat64(0.12)},
		{Resolution: "1080P", UnitPrice: testPtrFloat64(0.24)},
	}}
	row, err := policies.Update(ctx, AdminModelPolicyUpdate{Model: "wan3.0-video", Enabled: &enabled, Pricing: price})
	require.NoError(t, err)
	require.Equal(t, price, row.Pricing)
	require.False(t, row.Enabled)
	require.Equal(t, "manual", row.Source)

	replica := NewAdminModelPolicyService(store, nil, nil, nil, nil)
	runtime, err := replica.GetPricing(ctx, "wan3.0-video")
	require.NoError(t, err)
	require.Equal(t, BillingModeVideo, runtime.BillingMode)
	require.Equal(t, "second", runtime.PriceUnit)
	require.Nil(t, runtime.PerRequestPrice, "tiers alone do not configure a free default price")
	require.Len(t, runtime.Intervals, 3)
	for i, resolution := range []string{"480P", "720P", "1080P"} {
		require.Equal(t, resolution, runtime.Intervals[i].TierLabel)
		require.Equal(t, price.ResolutionPrices[i].UnitPrice, runtime.Intervals[i].PerRequestPrice)
	}
	row, err = replica.ResetPrice(ctx, "wan3.0-video")
	require.NoError(t, err)
	require.Nil(t, row.Pricing)
	require.False(t, row.Enabled)
}

func TestAdminVideoResolutionPricesValidateLabelsAndAmounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price AdminModelPrice
	}{
		{"unpriced", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second"}},
		{"empty label", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: " ", UnitPrice: testPtrFloat64(1)}}}},
		{"control label", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P\n1080P", UnitPrice: testPtrFloat64(1)}}}},
		{"oversized label", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: strings.Repeat("P", 65), UnitPrice: testPtrFloat64(1)}}}},
		{"duplicate", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(1)}, {Resolution: " 720p ", UnitPrice: testPtrFloat64(2)}}}},
		{"missing amount", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: testPtrFloat64(1), ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P"}}}},
		{"negative", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "480P", UnitPrice: testPtrFloat64(-1)}}}},
		{"infinite", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(math.Inf(1))}}}},
		{"NaN", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "1080P", UnitPrice: testPtrFloat64(math.NaN())}}}},
		{"wrong unit", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "request", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(1)}}}},
		{"token prices mixed", AdminModelPrice{BillingMode: BillingModeVideo, PriceUnit: "second", InputPricePerMillion: testPtrFloat64(1), ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(1)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, validateAdminModelPrice("video-alias", &tc.price), ErrAdminModelPolicyInvalid)
		})
	}
	for _, mode := range []BillingMode{BillingModeToken, BillingModeDuration, BillingModeAudio, BillingModeImage, BillingModePerRequest} {
		t.Run("non-video/"+string(mode), func(t *testing.T) {
			price := &AdminModelPrice{BillingMode: mode, PriceUnit: "second", UnitPrice: testPtrFloat64(1), ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(1)}}}
			require.ErrorContains(t, validateAdminModelPrice("model", price), "resolution prices require video")
		})
	}
	// An omitted or JSON null price must not decode as an explicit free tier.
	for _, amount := range []string{"", `,"unit_price":null`} {
		var price AdminModelPrice
		require.NoError(t, json.Unmarshal([]byte(`{"billing_mode":"video","price_unit":"second","resolution_prices":[{"resolution":"720P"`+amount+`}]}`), &price))
		require.ErrorIs(t, validateAdminModelPrice("video-alias", &price), ErrAdminModelPolicyInvalid)
	}
	for _, price := range []*AdminModelPrice{
		{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: testPtrFloat64(0.2)},
		{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: testPtrFloat64(0)},
		{BillingMode: BillingModeVideo, PriceUnit: "second", ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "720P", UnitPrice: testPtrFloat64(0)}}},
		{BillingMode: BillingModeVideo, PriceUnit: "second", UnitPrice: testPtrFloat64(0.2), ResolutionPrices: []AdminModelResolutionPrice{{Resolution: "1080P", UnitPrice: testPtrFloat64(0.4)}}},
	} {
		require.NoError(t, validateAdminModelPrice("video-alias", price))
	}
}
