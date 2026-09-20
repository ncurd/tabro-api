package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type mediaWorkerRepoStub struct{ *mediaGenerationJobRepoStub }

func (r *mediaWorkerRepoStub) ClaimMediaReconciliation(_ context.Context, now, lease time.Time, _ int) ([]*MediaGenerationJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var jobs []*MediaGenerationJob
	for _, j := range r.jobs {
		if j.UsageRecordedAt != nil || (j.NextPollAt != nil && j.NextPollAt.After(now)) || j.Status == MediaJobStatusFailed || j.Status == MediaJobStatusCanceled {
			continue
		}
		j.NextPollAt = &lease
		jobs = append(jobs, cloneMediaGenerationJobForTest(j))
	}
	return jobs, nil
}
func (r *mediaWorkerRepoStub) RescheduleMediaReconciliation(_ context.Context, id string, at *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[id].NextPollAt = at
	return nil
}

type mediaWorkerAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *mediaWorkerAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestMediaBillingWorkerSettlesWithoutClientPollingAndRetriesAfterRestart(t *testing.T) {
	billing, _, ledger, usage, meta, account := mediaBillingFixture()
	snap, err := billing.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	repo := &mediaWorkerRepoStub{newMediaGenerationJobRepoStub()}
	job := billingTestJob(snap)
	job.Status = MediaJobStatusRunning
	job.UpstreamTaskID = "task"
	job.UpstreamResponseJSON = nil
	require.NoError(t, repo.Create(context.Background(), job))
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"output":{"task_status":"SUCCEEDED","task_id":"task","video_url":"https://example.com/video.mp4"},"usage":{"output_video_duration":5.5,"SR":720,"video_count":1}}`)}
	svc := NewMediaGenerationService(&mediaWorkerAccountRepo{account: account}, repo, usage, upstream, &config.Config{}, billing)
	usage.err = errors.New("temporary logging failure")
	require.NoError(t, svc.ReconcileVideoJobs(context.Background()))
	require.Equal(t, 1, ledger.applied)
	pending, err := repo.GetByPublicID(context.Background(), job.PublicID)
	require.NoError(t, err)
	require.Equal(t, MediaJobStatusSucceeded, pending.Status)
	require.Nil(t, pending.UsageRecordedAt)
	usage.err = nil
	require.NoError(t, repo.RescheduleMediaReconciliation(context.Background(), job.PublicID, nil))
	upstream.lastReq = nil
	restarted := NewMediaGenerationService(&mediaWorkerAccountRepo{account: account}, repo, usage, upstream, &config.Config{}, billing)
	require.NoError(t, restarted.ReconcileVideoJobs(context.Background()))
	require.Equal(t, 1, ledger.applied)
	require.Nil(t, upstream.lastReq)
	settled, err := repo.GetByPublicID(context.Background(), job.PublicID)
	require.NoError(t, err)
	require.NotNil(t, settled.UsageRecordedAt)
}
func TestMediaBillingWorkerRetriesSuccessfulVoiceEnrollmentWithoutReforwarding(t *testing.T) {
	billing, p, ledger, usage, meta, account := mediaBillingFixture()
	p.price.BillingMode = BillingModePerRequest
	snap, err := billing.Prepare(context.Background(), meta, account, "qwen-voice-enrollment", "qwen-voice-enrollment", "voice_clone", "")
	require.NoError(t, err)
	repo := &mediaWorkerRepoStub{newMediaGenerationJobRepoStub()}
	job := billingTestJob(snap)
	job.Kind = "voice_clone"
	job.Provider = "dashscope"
	job.UpstreamTaskID = ""
	require.NoError(t, repo.Create(context.Background(), job))
	svc := NewMediaGenerationService(nil, repo, usage, nil, &config.Config{}, billing)
	require.NoError(t, svc.ReconcileVideoJobs(context.Background()))
	require.Equal(t, 1, ledger.applied)
	saved, err := repo.GetByPublicID(context.Background(), job.PublicID)
	require.NoError(t, err)
	require.NotNil(t, saved.UsageRecordedAt)
}
func TestMediaBillingIncompleteSuccessCanFetchUsageLater(t *testing.T) {
	billing, _, ledger, usage, meta, account := mediaBillingFixture()
	snap, err := billing.Prepare(context.Background(), meta, account, "wan", "wan", MediaJobKindVideoGeneration, "720P")
	require.NoError(t, err)
	repo := newMediaGenerationJobRepoStub()
	job := billingTestJob(snap)
	job.UpstreamTaskID = "task"
	job.UpstreamResponseJSON = []byte(`{"usage":{"SR":720}}`)
	require.True(t, MediaJobNeedsUsageRefresh(job))
	require.NoError(t, repo.Create(context.Background(), job))
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"output":{"task_status":"SUCCEEDED","video_url":"https://example.com/video.mp4"},"usage":{"output_video_duration":5.5,"SR":720,"video_count":1}}`)}
	svc := NewMediaGenerationService(&mediaWorkerAccountRepo{account: account}, repo, usage, upstream, &config.Config{}, billing)
	updated, err := svc.RefreshVideoJob(context.Background(), job, nil)
	require.NoError(t, err)
	require.Equal(t, 1, ledger.applied)
	require.NotNil(t, updated.UsageRecordedAt)
	require.False(t, MediaJobNeedsUsageRefresh(updated))
}

type mediaPersistFailureRepo struct {
	*mediaGenerationJobRepoStub
	createErr      error
	updateFailures int
}

func (r *mediaPersistFailureRepo) Create(ctx context.Context, j *MediaGenerationJob) error {
	if r.createErr != nil {
		return r.createErr
	}
	return r.mediaGenerationJobRepoStub.Create(ctx, j)
}
func (r *mediaPersistFailureRepo) UpdateFromUpstream(ctx context.Context, id string, u MediaGenerationJobUpdate) (*MediaGenerationJob, error) {
	if r.updateFailures > 0 {
		r.updateFailures--
		return nil, errors.New("temporary DB failure")
	}
	return r.mediaGenerationJobRepoStub.UpdateFromUpstream(ctx, id, u)
}

type mediaSubmissionObserver struct {
	HTTPUpstream
	observe func()
}

func (u *mediaSubmissionObserver) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.observe()
	return u.HTTPUpstream.Do(req, proxy, id, concurrency)
}
func TestMediaBillingSubmissionIsPersistedBeforeUpstreamAndRetriesOnlyPersistence(t *testing.T) {
	billing, _, _, usage, meta, account := mediaBillingFixture()
	repo := &mediaPersistFailureRepo{mediaGenerationJobRepoStub: newMediaGenerationJobRepoStub(), updateFailures: 1}
	calls := 0
	recorder := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(200, `{"output":{"task_id":"accepted","task_status":"PENDING"}}`)}
	upstream := &mediaSubmissionObserver{HTTPUpstream: recorder, observe: func() {
		calls++
		require.Len(t, repo.created, 1)
		require.NotEmpty(t, repo.created[0].BillingSnapshotJSON)
	}}
	svc := NewMediaGenerationService(nil, repo, usage, upstream, &config.Config{}, billing)
	job, err := svc.CreateVideoJob(context.Background(), meta, account, VideoGenerationRequest{Model: "wan3.0-video", Prompt: "test", Resolution: "720P"})
	require.NoError(t, err)
	require.Equal(t, "accepted", job.UpstreamTaskID)
	require.Equal(t, 1, calls)
	repo.createErr = errors.New("DB unavailable")
	_, err = svc.CreateVideoJob(context.Background(), meta, account, VideoGenerationRequest{Model: "wan3.0-video", Prompt: "test", Resolution: "720P"})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
