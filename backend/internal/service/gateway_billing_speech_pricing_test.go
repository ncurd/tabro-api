//go:build unit

package service

import (
	"context"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestGatewayCreditsMissingSpeechPriceIsActionableBeforeAuthorityOrSupplierDispatch(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/audio/transcriptions/stream", `{"model":"paraformer-realtime-v2"}`},
		{"/v1/audio/speech", `{"model":"qwen3-tts-flash-realtime","input":"你好"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			coordinator, store, authority, principal := gatewayBillingFixture()
			pricer, key := noChannelCreditPricer()
			key.Group.Platform = PlatformDashScope
			coordinator.creditPricer = pricer
			execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal,
				"private-subject-proof", tc.path, "speech-missing-price", []byte(tc.body), key)
			require.Nil(t, execution)
			require.ErrorIs(t, err, bc.ErrState)
			require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
			failure := DescribeRealtimeSpeechFailure(realtimeSpeechBillingError(err))
			require.Equal(t, "speech_pricing_not_configured", failure.Code)
			require.Equal(t, "billing", failure.Stage)
			require.NotContains(t, failure.Message, "private-subject-proof")
			require.Zero(t, authority.quoteCalls, "missing prices must not create remote billing work")
			require.Zero(t, authority.reserves)
			require.Zero(t, authority.dispatches)
			require.Empty(t, store.op.OperationID)
		})
	}
}

func TestInvalidSpeechPriceKeepsPricingCauseForAdmissionAndLocalBilling(t *testing.T) {
	for _, price := range []*ChannelModelPricing{
		{BillingMode: BillingModeToken},
		{BillingMode: BillingModeAudio},
		{BillingMode: BillingModeAudio, PerRequestPrice: mediaPricePtr(-1)},
		{BillingMode: BillingModeAudio, Intervals: []PricingInterval{{TierLabel: "", PerRequestPrice: mediaPricePtr(1)}}},
		{BillingMode: BillingModeAudio, Intervals: []PricingInterval{{TierLabel: "standard", PerRequestPrice: mediaPricePtr(1)}, {TierLabel: "STANDARD", PerRequestPrice: mediaPricePtr(2)}}},
	} {
		_, err := snapshotMediaPrice(price, MediaJobKindAudioTranscription)
		require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
		require.Equal(t, "speech_pricing_not_configured", DescribeRealtimeSpeechFailure(realtimeSpeechBillingError(err)).Code)
	}
	free, err := snapshotMediaPrice(&ChannelModelPricing{BillingMode: BillingModeAudio, PerRequestPrice: mediaPricePtr(0)}, MediaJobKindAudioTranscription)
	require.NoError(t, err)
	unit, err := free.UnitPrice("")
	require.NoError(t, err)
	require.Zero(t, unit, "an explicit free price remains distinct from an absent price")
}
