package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestWan3DefaultResolutionUsesEffectiveModelAndFrozenPrice(t *testing.T) {
	for _, tc := range []struct {
		name, requested, channelModel, upstreamModel string
		accountMapping                               map[string]any
	}{
		{name: "standard", requested: "wan3.0-video", upstreamModel: "wan3.0-video"},
		{name: "prime", requested: "wan3.0-video-prime", upstreamModel: "wan3.0-video-prime"},
		{name: "account alias", requested: "customer-video", upstreamModel: "wan3.0-video", accountMapping: map[string]any{"customer-video": "wan3.0-video"}},
		{name: "channel and account alias", requested: "customer-video", channelModel: "channel-video", upstreamModel: "wan3.0-video-prime", accountMapping: map[string]any{"channel-video": "wan3.0-video-prime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			billing, prices, ledger, usage, meta, account := mediaBillingFixture()
			prices.price = &ChannelModelPricing{BillingMode: BillingModeVideo, Intervals: []PricingInterval{{TierLabel: "1080P", PerRequestPrice: mediaPricePtr(0.4)}}}
			prices.mapping.MappedModel = tc.channelModel
			account.Credentials = map[string]any{"api_key": "test-key", "base_url": "https://workspace.cn-beijing.maas.aliyuncs.com", "model_mapping": tc.accountMapping}
			req := VideoGenerationRequest{Model: tc.requested, Prompt: "draw"}
			meta.RequestJSON, _ = json.Marshal(req)
			repo := newMediaGenerationJobRepoStub()
			upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"output":{"task_id":"wan-task","task_status":"PENDING"}}`)}
			svc := NewMediaGenerationService(nil, repo, usage, upstream, &config.Config{}, billing)
			job, err := svc.CreateVideoJob(context.Background(), meta, account, req)
			require.NoError(t, err, "only the upstream default 1080P tier is configured")
			require.Equal(t, tc.upstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "1080P", gjson.GetBytes(upstream.lastBody, "parameters.resolution").String())
			require.Equal(t, "1080P", job.VideoResolution)
			require.InDelta(t, 0.4, gjson.GetBytes(job.BillingSnapshotJSON, "price.tiers.1080P").Float(), 1e-10)
			// Settlement must retain the submitted tier and immutable rate even
			// if the channel price changes before the provider finishes.
			*prices.price.Intervals[0].PerRequestPrice = 99
			upstream.resp = jsonResponse(http.StatusOK, `{"output":{"task_id":"wan-task","task_status":"SUCCEEDED","video_url":"https://example.com/video.mp4"},"usage":{"output_video_duration":5,"SR":1080,"video_count":1}}`)
			upstream.respBody = nil
			job, err = svc.RefreshVideoJob(context.Background(), job, account)
			require.NoError(t, err)
			require.NotNil(t, job.UsageRecordedAt)
			require.Equal(t, "1080P", *usage.logs[job.PublicID].BillingTier)
			require.InDelta(t, 2.0, ledger.commands[job.PublicID].TotalCost, 1e-10)
		})
	}
}

func TestWan3ResolutionDefaultPreservesCentralExplicitAndOtherModels(t *testing.T) {
	for _, tc := range []struct {
		name, model, mapped, platform, resolution, expected string
		central                                             bool
	}{
		{name: "central wan", model: "wan3.0-video", platform: PlatformDashScope, central: true, expected: "720P"},
		{name: "central mapped wan", model: "customer-video", mapped: "wan3.0-video-prime", platform: PlatformDashScope, central: true, expected: "720P"},
		{name: "explicit wan", model: "wan3.0-video", platform: PlatformDashScope, resolution: "480P", expected: "480P"},
		{name: "happyhorse", model: "happyhorse-1.0-r2v", platform: PlatformDashScope},
		{name: "mapped away from wan", model: "wan3.0-video", mapped: "happyhorse-1.0-r2v", platform: PlatformDashScope},
		{name: "ark", model: "doubao-seedance-2-5-260628", platform: PlatformVolcengineArk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.central {
				ctx = bc.WithExecution(ctx, &bc.Execution{Mode: "central"})
			}
			response := `{"output":{"task_id":"task","task_status":"PENDING"}}`
			if tc.platform == PlatformVolcengineArk {
				response = `{"id":"task","status":"queued"}`
			}
			upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, response)}
			account := &Account{Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			if tc.mapped != "" {
				account.Credentials["model_mapping"] = map[string]any{tc.model: tc.mapped}
			}
			svc := NewMediaGenerationService(nil, newMediaGenerationJobRepoStub(), nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
			job, err := svc.CreateVideoJob(ctx, MediaRequestMeta{}, account, VideoGenerationRequest{Model: tc.model, Prompt: "draw", Resolution: tc.resolution})
			require.NoError(t, err)
			path := "parameters.resolution"
			if tc.platform == PlatformVolcengineArk {
				path = "resolution"
			}
			require.Equal(t, tc.expected, gjson.GetBytes(upstream.lastBody, path).String())
			require.Equal(t, tc.expected, job.VideoResolution)
			if tc.expected == "" {
				require.False(t, gjson.GetBytes(upstream.lastBody, path).Exists())
			}
		})
	}
}
