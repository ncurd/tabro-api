package service

import (
	"bytes"
	"context"
	"fmt"
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
	svc := NewMediaGenerationService(nil, nil, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
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
	svc := NewMediaGenerationService(nil, jobRepo, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
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
	svc := NewMediaGenerationService(nil, jobRepo, usageRepo, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
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
	svc := NewMediaGenerationService(nil, jobRepo, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
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
	if update.AudioVoice != "" {
		job.AudioVoice = update.AudioVoice
	}
	if update.UpstreamTaskID != "" {
		job.UpstreamTaskID = update.UpstreamTaskID
	}
	if update.ErrorCode != "" {
		job.ErrorCode = update.ErrorCode
	}
	if update.ErrorMessage != "" {
		job.ErrorMessage = update.ErrorMessage
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
	err         error
	lastLog     *UsageLog
}

func (r *mediaGenerationUsageRepoStub) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.createCalls++
	cloned := *log
	r.lastLog = &cloned
	return r.err == nil, r.err
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
	cloned.BillingSnapshotJSON = append([]byte(nil), job.BillingSnapshotJSON...)
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

func TestMediaGenerationServiceCreateVideoUsesAccountModelMapping(t *testing.T) {
	for _, tc := range []struct{ platform, model, mapped, response string }{
		{PlatformDashScope, "wan3-alias", "wan3.0-video-prime", `{"output":{"task_id":"wan-task","task_status":"PENDING"}}`},
		{PlatformVolcengineArk, "seedance-2.5", "ep-deployment-id", `{"id":"ark-task"}`},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, tc.response)}
			repo := newMediaGenerationJobRepoStub()
			svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
			account := &Account{ID: 5, Platform: tc.platform, Credentials: map[string]any{"api_key": "key", "model_mapping": map[string]any{tc.model: tc.mapped}}}
			job, err := svc.CreateVideoJob(context.Background(), MediaRequestMeta{}, account, VideoGenerationRequest{Model: tc.model, Prompt: "test"})
			require.NoError(t, err)
			require.Equal(t, tc.model, job.Model)
			require.Equal(t, tc.mapped, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, MediaJobStatusQueued, job.Status)
		})
	}
}

func TestMediaGenerationServiceRejectsVideoResponseWithoutTaskID(t *testing.T) {
	for _, platform := range []string{PlatformDashScope, PlatformVolcengineArk} {
		t.Run(platform, func(t *testing.T) {
			repo := newMediaGenerationJobRepoStub()
			svc := NewMediaGenerationService(nil, repo, nil, &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"message":"invalid upstream response"}`)}, &config.Config{RunMode: config.RunModeSimple}, nil)
			_, err := svc.CreateVideoJob(context.Background(), MediaRequestMeta{}, &Account{Platform: platform}, VideoGenerationRequest{Model: "model", Prompt: "test"})
			require.Error(t, err)
			require.Len(t, repo.created, 1)
			persisted, getErr := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
			require.NoError(t, getErr)
			require.Equal(t, MediaJobStatusFailed, persisted.Status)
			require.Empty(t, persisted.UpstreamTaskID)
		})
	}
}

func TestMediaGenerationServiceRefreshArkReadsNativeMetadataAndCachesCompletion(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"id":"ark-task","status":"succeeded","duration":12,"resolution":"720p","ratio":"9:16","output_format":"mov","content":{"video_url":"https://cdn.example.com/video.mov","last_frame_url":"https://cdn.example.com/last.png"},"usage":{"completion_tokens":1234}}`)}
	repo := newMediaGenerationJobRepoStub()
	usage := &mediaGenerationUsageRepoStub{}
	svc := NewMediaGenerationService(nil, repo, usage, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
	job := &MediaGenerationJob{PublicID: "job", AccountID: 5, Kind: MediaJobKindVideoGeneration, Provider: MediaProviderVolcengineArk, UpstreamTaskID: "ark-task", Status: MediaJobStatusRunning}
	require.NoError(t, repo.Create(context.Background(), job))
	job, err := svc.RefreshVideoJob(context.Background(), job, &Account{ID: 5, Platform: PlatformVolcengineArk})
	require.NoError(t, err)
	require.Equal(t, 12, job.VideoDurationSeconds)
	require.Equal(t, "720p", job.VideoResolution)
	require.Equal(t, "video/quicktime", job.ResultContentType)
	require.Equal(t, "https://cdn.example.com/video.mov", job.ResultURL)
	require.NotNil(t, job.CompletedAt)
	upstream.lastReq = nil
	_, err = svc.RefreshVideoJob(context.Background(), job, nil)
	require.NoError(t, err)
	require.Nil(t, upstream.lastReq)
	require.Equal(t, 1, usage.createCalls)
}

func TestMediaGenerationServiceUsageFailureCanBeRetried(t *testing.T) {
	repo := newMediaGenerationJobRepoStub()
	usage := &mediaGenerationUsageRepoStub{err: fmt.Errorf("temporary usage failure")}
	svc := NewMediaGenerationService(nil, repo, usage, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
	job := &MediaGenerationJob{PublicID: "job", Kind: MediaJobKindVideoGeneration, Status: MediaJobStatusSucceeded}
	require.NoError(t, repo.Create(context.Background(), job))
	require.Error(t, svc.recordMediaUsage(context.Background(), job))
	persisted, err := repo.GetByPublicID(context.Background(), job.PublicID)
	require.NoError(t, err)
	require.Nil(t, persisted.UsageRecordedAt)
	usage.err = nil
	require.NoError(t, svc.recordMediaUsage(context.Background(), job))
	persisted, err = repo.GetByPublicID(context.Background(), job.PublicID)
	require.NoError(t, err)
	require.NotNil(t, persisted.UsageRecordedAt)
	require.Equal(t, 2, usage.createCalls)
}

func TestMediaGenerationVideoStatusMapping(t *testing.T) {
	require.Equal(t, MediaJobStatusQueued, mapArkStatus(""))
	require.Equal(t, MediaJobStatusFailed, mapArkStatus("expired"))
	require.Equal(t, MediaJobStatusCanceled, mapArkStatus("cancelled"))
	require.Equal(t, MediaJobStatusCanceled, mapDashScopeStatus("CANCELLED"))
	require.Equal(t, MediaJobStatusUnknown, mapArkStatus("new-status"))
}

func TestMediaGenerationServiceRefreshWan3NativeUsage(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"output":{"task_id":"wan-task","task_status":"SUCCEEDED","video_url":"https://cdn.example.com/video.mp4"},"usage":{"duration":14,"input_video_duration":4,"output_video_duration":10,"SR":720,"ratio":"16:9","video_count":1}}`)}
	repo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
	job := &MediaGenerationJob{PublicID: "job", Kind: MediaJobKindVideoGeneration, Provider: MediaProviderDashScope, UpstreamTaskID: "wan-task", Status: MediaJobStatusRunning}
	require.NoError(t, repo.Create(context.Background(), job))
	job, err := svc.RefreshVideoJob(context.Background(), job, &Account{Platform: PlatformDashScope, Credentials: map[string]any{"base_url": "https://workspace.cn-beijing.maas.aliyuncs.com"}})
	require.NoError(t, err)
	require.Equal(t, "https://workspace.cn-beijing.maas.aliyuncs.com/api/v1/tasks/wan-task", upstream.lastReq.URL.String())
	require.Equal(t, 10, job.VideoDurationSeconds)
	require.Equal(t, "720P", job.VideoResolution)
}

func TestMediaGenerationServiceEnforcesUpstreamURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		target    string
		policy    config.URLAllowlistConfig
		nilConfig bool
		wantError string
	}{
		{name: "HTTP rejected by default", target: "http://media.example.com/task", wantError: "invalid url scheme"},
		{name: "HTTP explicitly allowed without allowlist", target: "http://media.example.com/task", policy: config.URLAllowlistConfig{AllowInsecureHTTP: true}},
		{name: "host outside allowlist", target: "https://other.example.com/task", policy: config.URLAllowlistConfig{Enabled: true, UpstreamHosts: []string{"media.example.com"}}, wantError: "host is not allowed"},
		{name: "allowed host preserves path and query", target: "https://media.example.com/tasks/task%2Fid?api-version=2024-04-01", policy: config.URLAllowlistConfig{Enabled: true, UpstreamHosts: []string{"media.example.com"}}},
		{name: "allowlist still requires HTTPS", target: "http://media.example.com/task", policy: config.URLAllowlistConfig{Enabled: true, AllowInsecureHTTP: true, UpstreamHosts: []string{"media.example.com"}}, wantError: "invalid url scheme"},
		{name: "empty enabled allowlist", target: "https://media.example.com/task", policy: config.URLAllowlistConfig{Enabled: true}, wantError: "allowlist is not configured"},
		{name: "private host rejected", target: "https://127.0.0.1/task", policy: config.URLAllowlistConfig{Enabled: true, UpstreamHosts: []string{"127.0.0.1"}}, wantError: "host is not allowed"},
		{name: "private host explicitly allowed", target: "https://127.0.0.1/task", policy: config.URLAllowlistConfig{Enabled: true, AllowPrivateHosts: true, UpstreamHosts: []string{"127.0.0.1"}}},
		{name: "nil config permits HTTPS", target: "https://media.example.com/task", nilConfig: true},
		{name: "nil config rejects HTTP", target: "http://media.example.com/task", nilConfig: true, wantError: "invalid url scheme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{}`)}
			var cfg *config.Config
			if !tc.nilConfig {
				cfg = &config.Config{}
				cfg.Security.URLAllowlist = tc.policy
			}
			svc := NewMediaGenerationService(nil, nil, nil, upstream, cfg, nil)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, nil)
			require.NoError(t, err)
			resp, err := svc.doUpstream(req, &Account{ID: 42})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, resp)
				require.Nil(t, upstream.lastReq, "rejected URLs must not reach the HTTP client")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, resp)
			defer resp.Body.Close()
			require.Equal(t, tc.target, upstream.lastReq.URL.String())
		})
	}
}
