package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestGatewayBillingMediaBoundsAndExactMeters(t *testing.T) {
	cases := []struct{ path, body, meter, bound, tier, product string }{
		{"/v1/videos/generations", `{"model":"wan3.0-video","prompt":"x"}`, "video_seconds", "5", "720P", "ai:wan3.0-video"},
		{"/videos/generations", `{"model":"doubao-seedance-2-0-260128","duration":-1,"resolution":"1080p"}`, "video_seconds", "15", "1080P", "ai:doubao-seedance-2-0-260128"},
		{"/audio/transcriptions", `{"model":"qwen3-asr-flash"}`, "audio_seconds", "300", "default", "ai:qwen3-asr-flash"},
		{"/v1/audio/speech", `{"model":"qwen3-tts-vc-2026-01-22","input":"你好😀"}`, "audio_characters", "3", "default", "ai:qwen3-tts-vc-2026-01-22"},
		{"/v1/audio/voices", `{"model":"qwen3-tts-vc-2026-01-22"}`, "request_count", "1", "default", "ai:qwen-voice-enrollment"},
	}
	for _, tt := range cases {
		t.Run(tt.path+tt.product, func(t *testing.T) {
			q, ok, err := mediaBillingQuoteRequest(tt.path, "op", []byte(tt.body))
			require.True(t, ok)
			require.NoError(t, err)
			require.Equal(t, bc.Decimal(tt.bound), q.MaximumUsage[tt.meter])
			require.Equal(t, tt.tier, q.ServiceTier)
			require.Equal(t, tt.product, q.ProductKey)
		})
	}
	for _, body := range []string{`{"model":"unknown","duration":-1}`, `{"model":"wan","duration":-2}`, `{"model":"wan","duration":3.5}`} {
		_, _, err := mediaBillingQuoteRequest("/videos/generations", "op", []byte(body))
		require.Error(t, err)
	}
	usage, err := mediaBillingMeters(&MediaGenerationJob{Kind: MediaJobKindVideoGeneration, Provider: MediaProviderDashScope, UpstreamResponseJSON: []byte(`{"usage":{"output_video_duration":0.100000000000000001}}`)})
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("0.100000000000000001"), usage["video_seconds"])
	_, err = mediaBillingMeters(&MediaGenerationJob{Kind: MediaJobKindVideoGeneration, Provider: MediaProviderDashScope, VideoDurationSeconds: 5, UpstreamResponseJSON: []byte(`{}`)})
	require.ErrorIs(t, err, ErrMediaUsageIncomplete)
}

func TestGatewayBillingMediaRestoresFrozenOperationAndNeverDebitsLocally(t *testing.T) {
	coordinator, store, authority, principal := gatewayBillingFixture()
	authority.quote.Request.ServiceTier = "720P"
	svc, prices, ledger, usage, meta, account := mediaBillingFixture()
	svc.apiKeys = &APIKeyService{GatewayBilling: coordinator}
	meta.RequestJSON = []byte(`{"model":"wan3.0-video","duration":10,"resolution":"720P","prompt":"x"}`)
	execution, err := coordinator.Prepare(context.Background(), *store.route, principal, "transient-secret", "/v1/videos/generations", "media-operation", meta.RequestJSON)
	require.NoError(t, err)
	ctx := bc.WithExecution(context.Background(), execution)
	snap, err := svc.Prepare(ctx, meta, account, "wan3.0-video", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	require.NotContains(t, string(snap.JSON()), "transient-secret")
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	job := billingTestJob(snap)
	// Simulate a fresh worker: all caller context is gone, including its token.
	prices.price.PerRequestPrice = mediaPricePtr(99)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.Settle(context.Background(), job) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, ledger.applied)
	cmd := ledger.commands[job.PublicID]
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	require.Zero(t, cmd.APIKeyRateLimitCost)
	require.Positive(t, cmd.AccountQuotaCost)
	require.NotNil(t, cmd.CentralEvent)
	var request bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &request))
	require.Equal(t, bc.Decimal("5.5"), request.Usage["video_seconds"])
	require.Equal(t, int64(2), request.ExpectedVersion)
	require.Len(t, usage.logs, 1)
	// Incomplete and mismatched evidence may never be replaced with the request's
	// requested duration or a different price tier.
	changed := *job
	changed.PublicID = "invalid"
	changed.UpstreamResponseJSON = []byte(`{"usage":{"output_video_duration":5.5,"SR":1080}}`)
	require.ErrorIs(t, svc.Settle(context.Background(), &changed), ErrMediaUsageIncomplete)
	require.Equal(t, 1, ledger.applied)
}

func TestGatewayPricedMediaRestoresFrozenPriceForAsyncSettlement(t *testing.T) {
	coordinator, store, _, principal := gatewayBillingFixture()
	svc, prices, ledger, _, meta, account := mediaBillingFixture()
	svc.apiKeys = &APIKeyService{GatewayBilling: coordinator}
	meta.RequestJSON = []byte(`{"model":"wan3.0-video","duration":10,"resolution":"720P","prompt":"x"}`)
	execution, err := coordinator.Prepare(context.Background(), *store.route, principal, "proof", "/v1/videos/generations", "media-priced", meta.RequestJSON)
	require.NoError(t, err)
	execution.Quote.Request.MaximumUsage = map[string]bc.Decimal{gatewayCreditMeter: "3.75"}
	execution.Quote.Request.ServiceTier = "default"
	execution.OriginalMaximumUsage = map[string]bc.Decimal{"request_count": "1", "video_seconds": "10"}
	rate := .25
	execution.GatewayPricingSnapshot, err = json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "wan3.0-video", RateMultiplier: 1.5,
		MediaKind: MediaJobKindVideoGeneration, MediaTier: "720P", MediaPrice: &MediaPriceSnapshot{Mode: BillingModeVideo, Default: &rate}})
	require.NoError(t, err)
	prices.price.PerRequestPrice = mediaPricePtr(99)
	ctx := bc.WithExecution(context.Background(), execution)
	snapshot, err := svc.Prepare(ctx, meta, account, "wan3.0-video", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	unit, err := snapshot.Price.UnitPrice("720P")
	require.NoError(t, err)
	require.Equal(t, rate, unit)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	require.NoError(t, svc.Settle(context.Background(), billingTestJob(snapshot)))
	var settled bc.SettleRequest
	require.NoError(t, json.Unmarshal(ledger.commands["video-billing-test"].CentralEvent.Body, &settled))
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: "2.0625"}, settled.Usage)
}
