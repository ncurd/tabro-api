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

func durationTestPricing() *ChannelModelPricing {
	return &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second",
		InputAudioPricePerSecond: mediaPricePtr(.01), OutputAudioPricePerSecond: mediaPricePtr(.04), OutputVideoPricePerSecond: mediaPricePtr(.07)}
}

func TestMediaDurationBillsActualDirectionAndFreezesSelectedPrice(t *testing.T) {
	for _, tc := range []struct {
		kind, unit, body string
		seconds, price   float64
	}{
		{MediaJobKindAudioTranscription, mediaUnitInputAudioSecond, `{"usage":{"seconds":12.5,"characters":999}}`, 12.5, .01},
		{MediaJobKindAudioSpeech, mediaUnitOutputAudioSecond, `{"usage":{"audio_seconds":1.25,"characters":999}}`, 1.25, .04},
		{MediaJobKindVideoGeneration, mediaUnitOutputVideoSecond, `{"usage":{"output_video_duration":5.5,"SR":720}}`, 5.5, .07},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			svc, prices, ledger, logs, meta, account := mediaBillingFixture()
			prices.price, prices.channel, prices.mapping = nil, nil, ChannelMappingResult{}
			manual := durationTestPricing()
			svc.modelPolicies = &mediaAdminPriceStub{price: manual}
			snap, err := svc.Prepare(context.Background(), meta, account, "media-alias", "provider-media", tc.kind, "720P")
			require.NoError(t, err)
			require.Equal(t, tc.unit, snap.Price.Unit)
			job := billingTestJob(snap)
			job.Kind, job.UpstreamResponseJSON = tc.kind, []byte(tc.body)
			*manual.InputAudioPricePerSecond, *manual.OutputAudioPricePerSecond, *manual.OutputVideoPricePerSecond = 99, 99, 99
			require.NoError(t, svc.Settle(context.Background(), job))
			cmd := ledger.commands[job.PublicID]
			require.InDelta(t, tc.seconds*tc.price, cmd.TotalCost, 1e-12)
			require.InDelta(t, tc.seconds*tc.price*1.5, cmd.BalanceCost, 1e-12)
			require.Equal(t, int(tc.seconds*1000), *logs.logs[job.PublicID].DurationMs)
			if tc.unit == mediaUnitInputAudioSecond {
				require.InDelta(t, tc.seconds*tc.price, cmd.InputCost, 1e-12)
				require.Zero(t, cmd.OutputCost)
			}
		})
	}
}

func TestMediaDurationMissingDirectionIsNotFreeAndDoesNotUseCharacters(t *testing.T) {
	price := durationTestPricing()
	price.OutputAudioPricePerSecond = nil
	_, err := snapshotMediaPrice(price, MediaJobKindAudioSpeech)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
	price.OutputAudioPricePerSecond = mediaPricePtr(0)
	snapshot, err := snapshotMediaPrice(price, MediaJobKindAudioSpeech)
	require.NoError(t, err)
	require.Zero(t, *snapshot.Default)
	job := &MediaGenerationJob{Kind: MediaJobKindAudioSpeech, AudioCharacterCount: 100, UpstreamResponseJSON: []byte(`{"usage":{"characters":100}}`)}
	_, _, err = mediaBillableUnitsForPrice(job, job.Kind, *snapshot)
	require.ErrorIs(t, err, ErrMediaUsageIncomplete, "even an explicit free rate requires actual duration evidence")
	job.UpstreamResponseJSON = []byte(`{"usage":{"audio_seconds":600.0001}}`)
	_, _, err = mediaBillableUnitsForPrice(job, job.Kind, *snapshot)
	require.ErrorIs(t, err, ErrMediaUsageIncomplete)
}

func TestMediaDurationAccountStatisticsUseTheirOwnFrozenUnit(t *testing.T) {
	for _, customerDuration := range []bool{true, false} {
		svc, prices, ledger, logs, meta, account := mediaBillingFixture()
		prices.mapping, prices.channel = ChannelMappingResult{}, nil
		prices.price = durationTestPricing()
		if !customerDuration {
			prices.price = &ChannelModelPricing{BillingMode: BillingModeAudio, PerRequestPrice: mediaPricePtr(.01)}
		}
		snap, err := svc.Prepare(context.Background(), meta, account, "speech", "speech", MediaJobKindAudioSpeech, "")
		require.NoError(t, err)
		snap.AccountPrice = &MediaPriceSnapshot{Mode: BillingModeAudio, Default: mediaPricePtr(.01)}
		if !customerDuration {
			snap.AccountPrice, err = snapshotMediaPrice(durationTestPricing(), MediaJobKindAudioSpeech)
			require.NoError(t, err)
		}
		job := billingTestJob(snap)
		job.Kind, job.UpstreamResponseJSON = MediaJobKindAudioSpeech, []byte(`{"usage":{"audio_seconds":1.25,"characters":100}}`)
		require.NoError(t, svc.Settle(context.Background(), job))
		// Explicit account statistics prices retain their existing independent
		// unit price; they are not customer prices or customer rate multipliers.
		customer, accountCost := 1.25*.04, 100*.01
		if !customerDuration {
			customer, accountCost = 100*.01, 1.25*.04
		}
		require.InDelta(t, customer*1.5, ledger.commands[job.PublicID].BalanceCost, 1e-12)
		require.InDelta(t, accountCost, *logs.logs[job.PublicID].AccountStatsCost, 1e-12)
	}
}

func TestMediaDurationCentralSpeechFreezesCapAndSettlesActualSeconds(t *testing.T) {
	coordinator, store, authority, principal := gatewayBillingFixture()
	pricer, key := noChannelCreditPricer()
	key.ID = 13
	key.Group.Platform = PlatformDashScope
	key.Group.RateMultiplier = 1
	pricer.gateway.mediaModelPolicies = &mediaAdminPriceStub{price: durationTestPricing()}
	coordinator.creditPricer = pricer
	body := []byte(`{"model":"speech-public-alias","input":"你好","response_format":"wav"}`)
	execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", "/v1/audio/speech", "duration-operation", body, key)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("600"), execution.OriginalMaximumUsage["audio_seconds"])
	require.Len(t, execution.Quote.Request.MaximumUsage, 1)
	maximum, err := strconv.ParseFloat(string(execution.Quote.Request.MaximumUsage[gatewayCreditMeter]), 64)
	require.NoError(t, err)
	require.GreaterOrEqual(t, maximum, 24.0)
	require.InDelta(t, 24, maximum, 1e-9)
	require.Equal(t, 1, authority.quoteCalls)
	var frozen gatewayCreditPriceSnapshot
	require.NoError(t, json.Unmarshal(execution.GatewayPricingSnapshot, &frozen))
	require.Equal(t, mediaUnitOutputAudioSecond, frozen.MediaPrice.Unit)
	restoredBounds := map[string]bc.Decimal{"request_count": "1", "audio_characters": "4"}
	require.NoError(t, applyFrozenMediaDurationBound(restoredBounds, execution.GatewayPricingSnapshot))
	require.Equal(t, bc.Decimal("600"), restoredBounds["audio_seconds"])

	svc, prices, ledger, _, meta, account := mediaBillingFixture()
	svc.apiKeys = &APIKeyService{GatewayBilling: coordinator}
	prices.price, prices.channel, prices.mapping = nil, nil, ChannelMappingResult{}
	meta.APIKey, meta.UserID, meta.APIKeyID, meta.GroupID, meta.RequestJSON = key, key.UserID, key.ID, key.GroupID, body
	ctx := bc.WithExecution(context.Background(), execution)
	snapshot, err := svc.Prepare(ctx, meta, account, "speech-public-alias", "qwen3-tts-flash-realtime", MediaJobKindAudioSpeech, "")
	require.NoError(t, err)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	job := billingTestJob(snapshot)
	job.Kind = MediaJobKindAudioSpeech
	job.UserID, job.APIKeyID = meta.UserID, meta.APIKeyID
	job.UpstreamResponseJSON = []byte(`{"usage":{"audio_seconds":"1.23456789","characters":4}}`)
	require.NoError(t, svc.Settle(context.Background(), job))
	cmd := ledger.commands[job.PublicID]
	require.Equal(t, map[string]bc.Decimal{"request_count": "1", "audio_seconds": "1.23456789"}, cmd.ExactUsage)
	require.Zero(t, cmd.BalanceCost)
	var settled bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &settled))
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: "0.0493827156"}, settled.Usage)
}

func TestMediaDurationShadowRetainsAuthorityCharacterMeter(t *testing.T) {
	price, err := snapshotMediaPrice(durationTestPricing(), MediaJobKindAudioSpeech)
	require.NoError(t, err)
	snapshot := MediaBillingSnapshot{Price: *price, Center: &bc.ExecutionSnapshot{Mode: "shadow",
		Quote: bc.Quote{Request: bc.ReserveRequest{MaximumUsage: map[string]bc.Decimal{"audio_characters": "100", "request_count": "1"}}}}}
	job := &MediaGenerationJob{Kind: MediaJobKindAudioSpeech, BillingSnapshotJSON: snapshot.JSON(),
		UpstreamResponseJSON: []byte(`{"usage":{"characters":80,"audio_seconds":"1.25"}}`)}
	meters, err := mediaBillingMeters(job)
	require.NoError(t, err)
	require.Equal(t, map[string]bc.Decimal{"request_count": "1", "audio_characters": "80"}, meters)
	seconds, _, err := mediaBillableUnitsForPrice(job, job.Kind, *price)
	require.NoError(t, err)
	require.Equal(t, 1.25, seconds, "local pricing remains based on output audio seconds")
}

func TestMediaDurationCentralFlatVideoUsesDurationWithoutResolution(t *testing.T) {
	for _, actual := range []string{`{"usage":{"output_video_duration":5.5}}`, `{"usage":{"output_video_duration":5.5,"SR":1080}}`} {
		coordinator, store, _, principal := gatewayBillingFixture()
		pricer, key := noChannelCreditPricer()
		key.ID, key.Group.Platform, key.Group.RateMultiplier = 13, PlatformDashScope, 1
		pricer.gateway.mediaModelPolicies = &mediaAdminPriceStub{price: durationTestPricing()}
		coordinator.creditPricer = pricer
		body := []byte(`{"model":"wan-video","duration":10,"resolution":"720P","prompt":"test"}`)
		execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", "/v1/videos/generations", "duration-video", body, key)
		require.NoError(t, err)
		svc, prices, ledger, _, meta, account := mediaBillingFixture()
		svc.apiKeys = &APIKeyService{GatewayBilling: coordinator}
		prices.price, prices.channel, prices.mapping = nil, nil, ChannelMappingResult{}
		meta.APIKey, meta.UserID, meta.APIKeyID, meta.GroupID, meta.RequestJSON = key, key.UserID, key.ID, key.GroupID, body
		ctx := bc.WithExecution(context.Background(), execution)
		snapshot, err := svc.Prepare(ctx, meta, account, "wan-video", "wan-video", MediaJobKindVideoGeneration, "720P")
		require.NoError(t, err)
		require.NoError(t, bc.BeforeSupplierRequest(ctx))
		job := billingTestJob(snapshot)
		job.UserID, job.APIKeyID, job.UpstreamResponseJSON = meta.UserID, meta.APIKeyID, []byte(actual)
		require.NoError(t, svc.Settle(context.Background(), job))
		require.InDelta(t, .385, ledger.commands[job.PublicID].ActualCost, 1e-12)
	}
}

func TestMediaDurationAsyncSpeechRejectedBeforeRemoteBilling(t *testing.T) {
	coordinator, store, authority, principal := gatewayBillingFixture()
	pricer, key := noChannelCreditPricer()
	pricer.gateway.mediaModelPolicies = &mediaAdminPriceStub{price: durationTestPricing()}
	coordinator.creditPricer = pricer
	_, err := coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", "/v1/audio/speech/jobs", "unsupported", []byte(`{"model":"azure-tts","input":"hello"}`), key)
	require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
	require.Zero(t, authority.quoteCalls)
	require.Zero(t, authority.reserves)
}
