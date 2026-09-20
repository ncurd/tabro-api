package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type mediaPricingStub struct {
	price   *ChannelModelPricing
	channel *Channel
	mapping ChannelMappingResult
}

func (s *mediaPricingStub) GetChannelModelPricing(context.Context, int64, string) *ChannelModelPricing {
	return s.price
}
func (s *mediaPricingStub) GetChannelForGroup(context.Context, int64) (*Channel, error) {
	return s.channel, nil
}
func (s *mediaPricingStub) ResolveChannelMapping(context.Context, int64, string) ChannelMappingResult {
	return s.mapping
}

type mediaLedgerStub struct {
	mu       sync.Mutex
	commands map[string]*UsageBillingCommand
	applied  int
	err      error
}

func (s *mediaLedgerStub) Apply(_ context.Context, c *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	c.Normalize()
	if s.commands == nil {
		s.commands = map[string]*UsageBillingCommand{}
	}
	if prev := s.commands[c.RequestID]; prev != nil {
		if prev.RequestFingerprint != c.RequestFingerprint {
			return nil, ErrUsageBillingRequestConflict
		}
		return &UsageBillingApplyResult{}, nil
	}
	clone := *c
	s.commands[c.RequestID] = &clone
	s.applied++
	return &UsageBillingApplyResult{Applied: true}, nil
}

type mediaUsageStub struct {
	UsageLogRepository
	mu   sync.Mutex
	logs map[string]*UsageLog
	err  error
}

func (s *mediaUsageStub) Create(_ context.Context, l *UsageLog) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if s.logs == nil {
		s.logs = map[string]*UsageLog{}
	}
	if s.logs[l.RequestID] != nil {
		return false, nil
	}
	c := *l
	s.logs[l.RequestID] = &c
	return true, nil
}

func mediaBillingFixture() (*MediaBillingService, *mediaPricingStub, *mediaLedgerStub, *mediaUsageStub, MediaRequestMeta, *Account) {
	rate := 0.25
	pricing := &mediaPricingStub{price: &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: &rate}, mapping: ChannelMappingResult{ChannelID: 9}}
	ledger := &mediaLedgerStub{}
	usage := &mediaUsageStub{}
	service := &MediaBillingService{pricing: pricing, ledger: ledger, usage: usage}
	gid := int64(3)
	key := &APIKey{ID: 2, UserID: 1, GroupID: &gid, Group: &Group{ID: 3, RateMultiplier: 1.5}, Quota: 10, RateLimit1d: 5}
	meta := MediaRequestMeta{UserID: 1, APIKeyID: 2, GroupID: &gid, APIKey: key, RequestJSON: []byte(`{"model":"wan3.0-video","duration":99,"media":[{"url":"data:image/png;base64,c2VjcmV0"}]}`)}
	accountRate := 0.8
	account := &Account{ID: 4, Platform: PlatformDashScope, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Credentials: map[string]any{}, Extra: map[string]any{"quota_limit": 100.0}}
	return service, pricing, ledger, usage, meta, account
}
func billingTestJob(snapshot *MediaBillingSnapshot) *MediaGenerationJob {
	return &MediaGenerationJob{PublicID: "video-billing-test", Kind: MediaJobKindVideoGeneration, Provider: MediaProviderDashScope, Status: MediaJobStatusSucceeded, UserID: 1, APIKeyID: 2, AccountID: 4, GroupID: snapshot.GroupID, Model: "wan3.0-video", BillingSnapshotJSON: snapshot.JSON(), UpstreamResponseJSON: []byte(`{"usage":{"output_video_duration":5.5,"duration":15.5,"SR":720,"video_count":1}}`), VideoDurationSeconds: 99, VideoCount: 1, CreatedAt: time.Now()}
}

func TestMediaBillingUsesActualGeneratedSecondsAndImmutablePrices(t *testing.T) {
	svc, p, ledger, usage, meta, account := mediaBillingFixture()
	p.price.Intervals = []PricingInterval{{TierLabel: "720p", PerRequestPrice: mediaPricePtr(0.4)}}
	snap, err := svc.Prepare(context.Background(), meta, account, "wan3.0-video", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	require.NotContains(t, string(snap.JSON()), "base64")
	require.NotContains(t, string(snap.JSON()), "c2VjcmV0")
	job := billingTestJob(snap)
	p.price.PerRequestPrice = mediaPricePtr(9)
	*p.price.Intervals[0].PerRequestPrice = 8
	meta.APIKey.Group.RateMultiplier = 99
	*account.RateMultiplier = 99
	require.NoError(t, svc.Settle(context.Background(), job))
	cmd := ledger.commands[job.PublicID]
	require.InDelta(t, 2.2, cmd.TotalCost, 1e-10)
	require.InDelta(t, 3.3, cmd.BalanceCost, 1e-10)
	require.InDelta(t, 3.3, cmd.APIKeyQuotaCost, 1e-10)
	require.InDelta(t, 3.3, cmd.APIKeyRateLimitCost, 1e-10)
	require.InDelta(t, 1.76, *usage.logs[job.PublicID].AccountStatsCost, 1e-10)
	require.Equal(t, 5500, *usage.logs[job.PublicID].DurationMs)
}
func TestMediaBillingConcurrentSettlementAndLogRetryDoNotDoubleCharge(t *testing.T) {
	svc, _, ledger, usage, meta, account := mediaBillingFixture()
	snap, err := svc.Prepare(context.Background(), meta, account, "wan3.0-video", "wan3.0-video", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	job := billingTestJob(snap)
	usage.err = errors.New("log unavailable")
	require.ErrorIs(t, svc.Settle(context.Background(), job), ErrMediaBillingUnavailable)
	require.Equal(t, 1, ledger.applied)
	usage.err = nil
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.Settle(context.Background(), job) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, ledger.applied)
	require.Len(t, usage.logs, 1)
}
func TestMediaBillingFailsClosedForUnpricedAndInvalidRates(t *testing.T) {
	for _, price := range []*float64{nil, mediaPricePtr(-1), mediaPricePtr(math.NaN()), mediaPricePtr(math.Inf(1))} {
		svc, p, _, _, meta, account := mediaBillingFixture()
		p.price.PerRequestPrice = price
		_, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
		require.ErrorIs(t, err, ErrMediaPricingNotConfigured)
	}
	svc, p, ledger, _, meta, account := mediaBillingFixture()
	p.price.PerRequestPrice = mediaPricePtr(0)
	snap, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	require.NoError(t, svc.Settle(context.Background(), billingTestJob(snap)))
	require.Equal(t, 0.0, ledger.commands["video-billing-test"].BalanceCost)
}
func TestMediaBillingMissingActualDurationRemainsUncharged(t *testing.T) {
	svc, _, ledger, _, meta, account := mediaBillingFixture()
	snap, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	job := billingTestJob(snap)
	job.UpstreamResponseJSON = []byte(`{"usage":{"SR":720}}`)
	job.VideoDurationSeconds = 10
	require.ErrorIs(t, svc.Settle(context.Background(), job), ErrMediaUsageIncomplete)
	require.Zero(t, ledger.applied)
}
func TestMediaBillingSubscriptionAndPerRequest(t *testing.T) {
	svc, p, ledger, _, meta, account := mediaBillingFixture()
	p.price.BillingMode = BillingModePerRequest
	meta.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription
	meta.Subscription = &UserSubscription{ID: 5, UserID: 1, GroupID: 3}
	snap, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	job := billingTestJob(snap)
	job.UpstreamResponseJSON = []byte(`{}`)
	require.NoError(t, svc.Settle(context.Background(), job))
	cmd := ledger.commands[job.PublicID]
	require.Equal(t, 0.25, cmd.SubscriptionCost)
	require.Zero(t, cmd.BalanceCost)
	require.Equal(t, int64(5), *cmd.SubscriptionID)
}
func TestMediaBillingLedgerFailureDoesNotWriteUsage(t *testing.T) {
	svc, _, ledger, usage, meta, account := mediaBillingFixture()
	snap, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	ledger.err = fmt.Errorf("database unavailable")
	require.ErrorIs(t, svc.Settle(context.Background(), billingTestJob(snap)), ErrMediaBillingUnavailable)
	require.Empty(t, usage.logs)
}
func TestMediaGenerationRefusesUnpricedSubmissionBeforeUpstream(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(200, `{"id":"task"}`)}
	svc := NewMediaGenerationService(nil, newMediaGenerationJobRepoStub(), nil, upstream, &config.Config{}, nil)
	_, err := svc.CreateVideoJob(context.Background(), MediaRequestMeta{}, &Account{Platform: PlatformVolcengineArk}, VideoGenerationRequest{Model: "doubao-seedance-2-5-260628", Prompt: "test"})
	require.ErrorIs(t, err, ErrMediaBillingUnavailable)
	require.Nil(t, upstream.lastReq)
}
func mediaPricePtr(v float64) *float64 { return &v }

func TestMediaBillingDeletedVoiceStillSettlesOriginalEnrollment(t *testing.T) {
	svc, p, ledger, _, meta, account := mediaBillingFixture()
	p.price.BillingMode = BillingModePerRequest
	snap, err := svc.Prepare(context.Background(), meta, account, "qwen-voice-enrollment", "qwen-voice-enrollment", "voice_clone", "")
	require.NoError(t, err)
	job := billingTestJob(snap)
	job.Kind = "voice_clone"
	job.Status = MediaJobStatusCanceled
	job.AudioVoice = "enrolled-voice"
	job.UpstreamResponseJSON = []byte(`{"output":{"voice":"enrolled-voice"}}`)
	require.NoError(t, svc.Settle(context.Background(), job))
	require.Equal(t, 1, ledger.applied)
	require.InDelta(t, 0.375, ledger.commands[job.PublicID].BalanceCost, 1e-10)
}
func TestMediaBillingAccountQuotaUsesSnapshot(t *testing.T) {
	svc, _, ledger, _, meta, account := mediaBillingFixture()
	account.Extra = map[string]any{"quota_limit": 100.0}
	snap, err := svc.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	require.True(t, snap.ChargeAccountQuota)
	require.NoError(t, svc.Settle(context.Background(), billingTestJob(snap)))
	require.InDelta(t, 1.1, ledger.commands["video-billing-test"].AccountQuotaCost, 1e-10)
}
func TestMediaBillingChannelAndAccountMappingAreForwardedAndSnapshotted(t *testing.T) {
	billing, p, _, usage, meta, account := mediaBillingFixture()
	p.mapping = ChannelMappingResult{MappedModel: "channel-video", BillingModelSource: BillingModelSourceUpstream}
	account.Credentials = map[string]any{"model_mapping": map[string]any{"channel-video": "wan3.0-video-prime"}}
	repo := newMediaGenerationJobRepoStub()
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(200, `{"output":{"task_id":"task","task_status":"PENDING"}}`)}
	svc := NewMediaGenerationService(nil, repo, usage, upstream, &config.Config{}, billing)
	job, err := svc.CreateVideoJob(context.Background(), meta, account, VideoGenerationRequest{Model: "customer-alias", Prompt: "test", Resolution: "720P"})
	require.NoError(t, err)
	require.Contains(t, string(upstream.lastBody), `"model":"wan3.0-video-prime"`)
	require.Equal(t, "customer-alias", job.Model)
	require.Contains(t, string(job.BillingSnapshotJSON), `"upstream_model":"wan3.0-video-prime"`)
	require.Contains(t, string(job.BillingSnapshotJSON), `"requested_model":"customer-alias"`)
}
