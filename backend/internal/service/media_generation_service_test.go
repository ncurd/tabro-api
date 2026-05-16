package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMediaGenerationServiceForwardAzureSpeech(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": {"audio/mpeg"},
				"X-Requestid":  {"azure-request-1"},
			},
			Body: io.NopCloser(bytes.NewReader([]byte("audio-bytes"))),
		},
	}
	svc := NewMediaGenerationService(nil, nil, nil, upstream, &config.Config{})
	account := &Account{
		ID:          42,
		Platform:    PlatformAzureSpeech,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"subscription_key": "sub-key", "region": "eastus"},
	}

	result, body, headers, err := svc.ForwardAzureSpeech(context.Background(), account, AzureSpeechRequest{
		Model:          "tts-1",
		Input:          "hello <world>",
		Voice:          "en-US-JennyNeural",
		ResponseFormat: "mp3",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []byte("audio-bytes"), body)
	require.Equal(t, "azure-request-1", result.RequestID)
	require.Equal(t, "audio/mpeg", result.ContentType)
	require.Equal(t, "audio/mpeg", headers.Get("Content-Type"))
	require.Equal(t, "https://eastus.tts.speech.microsoft.com/cognitiveservices/v1", upstream.lastReq.URL.String())
	require.Equal(t, "sub-key", upstream.lastReq.Header.Get("Ocp-Apim-Subscription-Key"))
	require.Equal(t, "audio-24khz-48kbitrate-mono-mp3", upstream.lastReq.Header.Get("X-Microsoft-OutputFormat"))
	require.Equal(t, "application/ssml+xml", upstream.lastReq.Header.Get("Content-Type"))
	require.Contains(t, string(upstream.lastBody), `<voice name="en-US-JennyNeural">`)
	require.Contains(t, string(upstream.lastBody), `hello &lt;world&gt;`)
}

func TestMediaGenerationServiceCreateDashScopeVideoJob(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{
		resp: jsonResponse(http.StatusOK, `{"request_id":"req-1","output":{"task_id":"task-123","task_status":"PENDING"}}`),
	}
	jobRepo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, jobRepo, nil, upstream, &config.Config{})
	account := &Account{
		ID:          55,
		Platform:    PlatformDashScope,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "dash-key", "base_url": "https://dash.example.com"},
	}

	job, err := svc.CreateVideoJob(context.Background(), MediaRequestMeta{
		UserID:      1,
		APIKeyID:    2,
		GroupID:     int64PtrForMediaGenerationTest(3),
		RequestJSON: []byte(`{"model":"happyhorse-1.0-r2v","prompt":"draw"}`),
	}, account, VideoGenerationRequest{Model: "happyhorse-1.0-r2v", Prompt: "draw"})

	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, int64(55), job.AccountID)
	require.Equal(t, MediaProviderDashScope, job.Provider)
	require.Equal(t, PlatformDashScope, job.Platform)
	require.Equal(t, "task-123", job.UpstreamTaskID)
	require.Equal(t, "req-1", job.UpstreamRequestID)
	require.Equal(t, MediaJobStatusQueued, job.Status)
	require.Len(t, jobRepo.created, 1)
	require.Equal(t, job.PublicID, jobRepo.created[0].PublicID)
	require.Equal(t, "https://dash.example.com/api/v1/services/aigc/video-generation/video-synthesis", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer dash-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "enable", upstream.lastReq.Header.Get("X-DashScope-Async"))
}

func TestMediaGenerationServiceQueryDashScopeVideoJobRecordsUsageOnce(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{
		resp: jsonResponse(http.StatusOK, `{"request_id":"req-query","output":{"task_id":"task-123","task_status":"SUCCEEDED","video_url":"https://cdn.example.com/video.mp4"},"usage":{"duration":6,"resolution":"720P","ratio":"16:9","video_count":1}}`),
	}
	jobRepo := newMediaGenerationJobRepoStub()
	usageRepo := &mediaGenerationUsageRepoStub{}
	svc := NewMediaGenerationService(nil, jobRepo, usageRepo, upstream, &config.Config{})
	job := &MediaGenerationJob{
		PublicID:       "vidjob_test",
		Kind:           MediaJobKindVideoGeneration,
		Provider:       MediaProviderDashScope,
		Platform:       PlatformDashScope,
		Status:         MediaJobStatusRunning,
		UpstreamTaskID: "task-123",
		UserID:         1,
		APIKeyID:       2,
		AccountID:      55,
		Model:          "happyhorse-1.0-r2v",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, jobRepo.Create(context.Background(), job))
	account := &Account{ID: 55, Platform: PlatformDashScope, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "dash-key"}}

	first, err := svc.RefreshVideoJob(context.Background(), job, account)
	require.NoError(t, err)
	second, err := svc.RefreshVideoJob(context.Background(), first, account)
	require.NoError(t, err)

	require.Equal(t, MediaJobStatusSucceeded, second.Status)
	require.Equal(t, "https://cdn.example.com/video.mp4", second.ResultURL)
	require.Equal(t, 1, usageRepo.createCalls)
	require.Equal(t, "video", derefStringForMediaGenerationTest(usageRepo.lastLog.BillingMode))
	require.Equal(t, "vidjob_test", usageRepo.lastLog.RequestID)
	require.Equal(t, 6, usageRepo.lastLog.DurationMsValueSeconds())
	require.Equal(t, 1, usageRepo.lastLog.ImageCount)
	require.Equal(t, "720P", derefStringForMediaGenerationTest(usageRepo.lastLog.MediaType))
}

func TestMediaGenerationServiceCreateArkVideoJob(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{
		resp: jsonResponse(http.StatusOK, `{"id":"ark-task-1","status":"queued"}`),
	}
	jobRepo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, jobRepo, nil, upstream, &config.Config{})
	account := &Account{
		ID:          77,
		Platform:    PlatformVolcengineArk,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "ark-key"},
	}

	job, err := svc.CreateVideoJob(context.Background(), MediaRequestMeta{
		UserID:      10,
		APIKeyID:    20,
		RequestJSON: []byte(`{"model":"doubao-seedance-2-0-260128","prompt":"draw"}`),
	}, account, VideoGenerationRequest{
		Model:  "doubao-seedance-2-0-260128",
		Prompt: "draw",
		Media:  []VideoGenerationMedia{{Type: "reference_image", URL: "https://example.com/ref.png"}},
	})

	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, "ark-task-1", job.UpstreamTaskID)
	require.Equal(t, "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer ark-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "text", gjson.GetBytes(upstream.lastBody, "content.0.type").String())
	require.Equal(t, "draw", gjson.GetBytes(upstream.lastBody, "content.0.text").String())
	require.Equal(t, "image_url", gjson.GetBytes(upstream.lastBody, "content.1.type").String())
	require.Equal(t, "https://example.com/ref.png", gjson.GetBytes(upstream.lastBody, "content.1.image_url.url").String())
}

type mediaGenerationHTTPUpstreamRecorder struct {
	resp       *http.Response
	err        error
	lastReq    *http.Request
	lastBody   []byte
	respBody   []byte
	respStatus int
	respHeader http.Header
}

func (u *mediaGenerationHTTPUpstreamRecorder) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	if req.Body != nil {
		u.lastBody, _ = io.ReadAll(req.Body)
	}
	if u.resp == nil {
		return nil, u.err
	}
	if u.respBody == nil && u.resp.Body != nil {
		u.respBody, _ = io.ReadAll(u.resp.Body)
		u.respStatus = u.resp.StatusCode
		u.respHeader = u.resp.Header.Clone()
	}
	return &http.Response{
		StatusCode: u.respStatus,
		Header:     u.respHeader.Clone(),
		Body:       io.NopCloser(bytes.NewReader(u.respBody)),
	}, u.err
}

func (u *mediaGenerationHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type mediaGenerationJobRepoStub struct {
	mu      sync.Mutex
	jobs    map[string]*MediaGenerationJob
	created []*MediaGenerationJob
}

func newMediaGenerationJobRepoStub() *mediaGenerationJobRepoStub {
	return &mediaGenerationJobRepoStub{jobs: map[string]*MediaGenerationJob{}}
}

func (r *mediaGenerationJobRepoStub) Create(_ context.Context, job *MediaGenerationJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := cloneMediaGenerationJobForTest(job)
	r.jobs[job.PublicID] = cloned
	r.created = append(r.created, cloneMediaGenerationJobForTest(job))
	return nil
}

func (r *mediaGenerationJobRepoStub) GetByPublicID(_ context.Context, publicID string) (*MediaGenerationJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMediaGenerationJobForTest(r.jobs[publicID]), nil
}

func (r *mediaGenerationJobRepoStub) UpdateFromUpstream(_ context.Context, publicID string, update MediaGenerationJobUpdate) (*MediaGenerationJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[publicID]
	if job == nil {
		return nil, nil
	}
	if update.Status != "" {
		job.Status = update.Status
	}
	if update.UpstreamStatus != "" {
		job.UpstreamStatus = update.UpstreamStatus
	}
	if update.UpstreamRequestID != "" {
		job.UpstreamRequestID = update.UpstreamRequestID
	}
	if update.UpstreamResponseJSON != nil {
		job.UpstreamResponseJSON = append([]byte(nil), update.UpstreamResponseJSON...)
	}
	if update.ResultURL != "" {
		job.ResultURL = update.ResultURL
	}
	if update.ResultContentType != "" {
		job.ResultContentType = update.ResultContentType
	}
	if update.VideoDurationSeconds != 0 {
		job.VideoDurationSeconds = update.VideoDurationSeconds
	}
	if update.VideoResolution != "" {
		job.VideoResolution = update.VideoResolution
	}
	if update.VideoRatio != "" {
		job.VideoRatio = update.VideoRatio
	}
	if update.VideoCount != 0 {
		job.VideoCount = update.VideoCount
	}
	if update.CompletedAt != nil {
		job.CompletedAt = update.CompletedAt
	}
	job.UpdatedAt = time.Now().UTC()
	return cloneMediaGenerationJobForTest(job), nil
}

func (r *mediaGenerationJobRepoStub) MarkUsageRecorded(_ context.Context, publicID string, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[publicID]
	if job == nil {
		return false, nil
	}
	if job.UsageRecordedAt != nil {
		return false, nil
	}
	recordedAt := at
	job.UsageRecordedAt = &recordedAt
	return true, nil
}

type mediaGenerationUsageRepoStub struct {
	UsageLogRepository
	createCalls int
	lastLog     *UsageLog
}

func (r *mediaGenerationUsageRepoStub) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.createCalls++
	cloned := *log
	r.lastLog = &cloned
	return true, nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func cloneMediaGenerationJobForTest(job *MediaGenerationJob) *MediaGenerationJob {
	if job == nil {
		return nil
	}
	cloned := *job
	cloned.RequestJSON = append([]byte(nil), job.RequestJSON...)
	cloned.UpstreamResponseJSON = append([]byte(nil), job.UpstreamResponseJSON...)
	return &cloned
}

func int64PtrForMediaGenerationTest(v int64) *int64 { return &v }

func derefStringForMediaGenerationTest(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (u *UsageLog) DurationMsValueSeconds() int {
	if u == nil || u.DurationMs == nil {
		return 0
	}
	return *u.DurationMs / 1000
}
