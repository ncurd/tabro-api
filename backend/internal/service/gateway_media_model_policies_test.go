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

type mediaAdminPriceStub struct {
	price *ChannelModelPricing
	err   error
	calls int
}

func (s *mediaAdminPriceStub) GetPricing(context.Context, string) (*ChannelModelPricing, error) {
	s.calls++
	return s.price, s.err
}

func TestAdministratorSpeechPriceMatchesCentralAndLocalBillingAndIsFrozen(t *testing.T) {
	for _, tc := range []struct{ path, body, model, kind, unit string }{
		{"/v1/audio/transcriptions/stream", `{"model":"paraformer-realtime-v2"}`, "paraformer-realtime-v2", MediaJobKindAudioTranscription, "second"},
		{"/v1/audio/speech", `{"model":"qwen3-tts-flash-realtime","input":"你好"}`, "qwen3-tts-flash-realtime", MediaJobKindAudioSpeech, "character"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			coordinator, store, authority, principal := gatewayBillingFixture()
			pricer, key := noChannelCreditPricer()
			key.ID = 13
			key.Group.Platform = PlatformDashScope
			policies := &mediaAdminPriceStub{price: &ChannelModelPricing{
				BillingMode: BillingModeAudio, PriceUnit: tc.unit, PerRequestPrice: mediaPricePtr(.25),
			}}
			pricer.gateway.mediaModelPolicies = policies
			coordinator.creditPricer = pricer
			execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal,
				"private-proof", tc.path, "admin-speech-price", []byte(tc.body), key)
			require.NoError(t, err)
			require.Equal(t, 1, authority.quoteCalls)
			require.Equal(t, 1, policies.calls)
			var frozen gatewayCreditPriceSnapshot
			require.NoError(t, json.Unmarshal(execution.GatewayPricingSnapshot, &frozen))
			require.NotNil(t, frozen.MediaPrice)
			require.Equal(t, .25, *frozen.MediaPrice.Default)
			require.Equal(t, key.Group.ID, frozen.EffectiveGroupID)
			require.NotContains(t, string(execution.GatewayPricingSnapshot), "private-proof")

			media, channel, _, _, meta, account := mediaBillingFixture()
			media.modelPolicies = policies
			channel.price, channel.channel = nil, nil
			channel.mapping = ChannelMappingResult{}
			meta.APIKey, meta.UserID, meta.APIKeyID, meta.GroupID = key, key.UserID, key.ID, key.GroupID
			meta.RequestJSON = []byte(tc.body)
			localSnapshot, err := media.Prepare(context.Background(), meta, account, tc.model, tc.model, tc.kind, "")
			require.NoError(t, err)
			require.Equal(t, .25, *localSnapshot.Price.Default)
			require.Equal(t, 2, policies.calls)

			// A concurrent administrator edit affects the next operation, never
			// the charge contract frozen before this supplier request.
			*policies.price.PerRequestPrice = 99
			centralSnapshot, err := media.Prepare(bc.WithExecution(context.Background(), execution), meta, account, tc.model, tc.model, tc.kind, "")
			require.NoError(t, err)
			require.Equal(t, .25, *centralSnapshot.Price.Default)
			require.Equal(t, .25, *localSnapshot.Price.Default)
			require.Equal(t, 2, policies.calls, "restoring a reserved operation must not reload current prices")
			nextSnapshot, err := media.Prepare(context.Background(), meta, account, tc.model, tc.model, tc.kind, "")
			require.NoError(t, err)
			require.Equal(t, 99.0, *nextSnapshot.Price.Default)
		})
	}
}

func TestAdministratorMediaFallbackHonorsChannelPriceAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	policies := &mediaAdminPriceStub{price: &ChannelModelPricing{BillingMode: BillingModeAudio, PriceUnit: "second", PerRequestPrice: mediaPricePtr(99)}}
	channel := &ChannelModelPricing{BillingMode: BillingModeAudio, PerRequestPrice: mediaPricePtr(0)}
	price, err := resolveMediaModelPrice(ctx, channel, policies, "listen", MediaJobKindAudioTranscription)
	require.NoError(t, err)
	require.Zero(t, *price.Default)
	require.Zero(t, policies.calls, "an explicit channel zero price must remain authoritative")

	channel.BillingMode = BillingModeToken
	_, err = resolveMediaModelPrice(ctx, channel, policies, "listen", MediaJobKindAudioTranscription)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
	require.Zero(t, policies.calls, "invalid channel configuration must not silently use a different price")

	policies.err = errors.New("policy storage unavailable")
	_, err = resolveMediaModelPrice(ctx, nil, policies, "listen", MediaJobKindAudioTranscription)
	require.ErrorIs(t, err, ErrMediaBillingUnavailable)
	require.ErrorIs(t, err, policies.err)
	media, prices, _, _, meta, account := mediaBillingFixture()
	prices.price = nil
	media.modelPolicies = policies
	_, err = media.Prepare(ctx, meta, account, "listen", "listen", MediaJobKindAudioTranscription, "")
	require.ErrorIs(t, err, ErrMediaBillingUnavailable)
	require.NotErrorIs(t, err, ErrMediaPricingNotConfigured, "a failed policy read is not a missing configured price")
	policies.err, policies.price = nil, nil
	_, err = resolveMediaModelPrice(ctx, nil, policies, "listen", MediaJobKindAudioTranscription)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)

	policies.price = &ChannelModelPricing{BillingMode: BillingModeAudio, PriceUnit: "character", PerRequestPrice: mediaPricePtr(.25)}
	_, err = resolveMediaModelPrice(ctx, nil, policies, "listen", MediaJobKindAudioTranscription)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured, "ASR seconds cannot use TTS character prices")
	policies.price.PriceUnit = "second"
	price, err = resolveMediaModelPrice(ctx, nil, policies, "listen", MediaJobKindAudioTranscription)
	require.NoError(t, err)
	require.Equal(t, .25, *price.Default)

	policies.price = &ChannelModelPricing{BillingMode: BillingModePerRequest, PriceUnit: "request", PerRequestPrice: mediaPricePtr(0)}
	price, err = resolveMediaModelPrice(ctx, nil, policies, "speech", MediaJobKindAudioSpeech)
	require.NoError(t, err)
	require.Zero(t, *price.Default)
	policies.price.PriceUnit = "character"
	_, err = resolveMediaModelPrice(ctx, nil, policies, "speech", MediaJobKindAudioSpeech)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
}
