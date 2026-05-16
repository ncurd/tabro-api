package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

type MediaGenerationService struct {
	accountRepo  AccountRepository
	jobRepo      MediaGenerationJobRepository
	usageRepo    UsageLogRepository
	httpUpstream HTTPUpstream
	cfg          *config.Config
}

type MediaRequestMeta struct {
	UserID      int64
	APIKeyID    int64
	GroupID     *int64
	RequestJSON []byte
}

type MediaSyncAudioResult struct {
	RequestID   string
	ContentType string
}

func NewMediaGenerationService(
	accountRepo AccountRepository,
	jobRepo MediaGenerationJobRepository,
	usageRepo UsageLogRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *MediaGenerationService {
	return &MediaGenerationService{
		accountRepo:  accountRepo,
		jobRepo:      jobRepo,
		usageRepo:    usageRepo,
		httpUpstream: httpUpstream,
		cfg:          cfg,
	}
}

func (s *MediaGenerationService) ForwardAzureSpeech(ctx context.Context, account *Account, req AzureSpeechRequest) (*MediaSyncAudioResult, []byte, http.Header, error) {
	if account == nil {
		return nil, nil, nil, fmt.Errorf("azure speech account is required")
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, azureSpeechTTSEndpoint(account), strings.NewReader(buildAzureSpeechSSML(req)))
	if err != nil {
		return nil, nil, nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/ssml+xml")
	upstreamReq.Header.Set("Ocp-Apim-Subscription-Key", account.GetCredential("subscription_key"))
	upstreamReq.Header.Set("X-Microsoft-OutputFormat", mapAzureSpeechOutputFormat(req.ResponseFormat))

	resp, err := s.doUpstream(upstreamReq, account)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, nil, nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, resp.Header, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: body, ResponseHeaders: resp.Header}
	}

	contentType := resp.Header.Get("Content-Type")
	return &MediaSyncAudioResult{
		RequestID:   firstNonEmpty(resp.Header.Get("x-requestid"), resp.Header.Get("x-ms-requestid"), resp.Header.Get("x-request-id")),
		ContentType: contentType,
	}, body, resp.Header, nil
}

func (s *MediaGenerationService) CreateAudioSpeechJob(ctx context.Context, meta MediaRequestMeta, account *Account, req AzureSpeechRequest) (*MediaGenerationJob, error) {
	body := []byte(fmt.Sprintf(`{"inputKind":"SSML","inputs":[{"content":"%s"}]}`, escapeJSONString(buildAzureSpeechSSML(req))))
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPut, azureSpeechBatchEndpoint(account)+"/"+newMediaPublicID("azbatch")+"?api-version=2024-04-01", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Ocp-Apim-Subscription-Key", account.GetCredential("subscription_key"))

	respBody, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: respBody, ResponseHeaders: headers}
	}
	job := s.newJob(meta, account, req.Model, MediaJobKindAudioSpeech, MediaProviderAzureSpeech, PlatformAzureSpeech)
	job.UpstreamTaskID = gjson.GetBytes(respBody, "id").String()
	if job.UpstreamTaskID == "" {
		job.UpstreamTaskID = gjson.GetBytes(respBody, "self").String()
	}
	job.UpstreamRequestID = firstNonEmpty(headers.Get("x-requestid"), headers.Get("x-ms-requestid"))
	job.UpstreamStatus = gjson.GetBytes(respBody, "status").String()
	job.AudioVoice = req.Voice
	job.AudioFormat = req.ResponseFormat
	job.AudioCharacterCount = len([]rune(req.Input))
	job.UpstreamResponseJSON = append([]byte(nil), respBody...)
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *MediaGenerationService) RefreshAudioSpeechJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	if job == nil {
		return nil, fmt.Errorf("media generation job is required")
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, azureSpeechBatchEndpoint(account)+"/"+job.UpstreamTaskID+"?api-version=2024-04-01", nil)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Ocp-Apim-Subscription-Key", account.GetCredential("subscription_key"))
	body, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers}
	}
	mediaStatus := mapAzureBatchStatus(gjson.GetBytes(body, "status").String())
	completedAt := completedAtForStatus(mediaStatus)
	updated, err := s.jobRepo.UpdateFromUpstream(ctx, job.PublicID, MediaGenerationJobUpdate{
		Status:               mediaStatus,
		UpstreamStatus:       gjson.GetBytes(body, "status").String(),
		UpstreamRequestID:    firstNonEmpty(headers.Get("x-requestid"), headers.Get("x-ms-requestid")),
		UpstreamResponseJSON: body,
		ResultURL:            firstNonEmpty(gjson.GetBytes(body, "outputs.result").String(), gjson.GetBytes(body, "outputs.0.result").String()),
		ResultContentType:    "audio/mpeg",
		CompletedAt:          completedAt,
	})
	if err != nil {
		return nil, err
	}
	if updated != nil && updated.Status == MediaJobStatusSucceeded {
		_ = s.recordMediaUsage(ctx, updated)
	}
	return updated, nil
}

func (s *MediaGenerationService) CreateVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error) {
	if account == nil {
		return nil, fmt.Errorf("video generation account is required")
	}
	switch account.Platform {
	case PlatformDashScope:
		return s.createDashScopeVideoJob(ctx, meta, account, req)
	case PlatformVolcengineArk:
		return s.createArkVideoJob(ctx, meta, account, req)
	default:
		return nil, fmt.Errorf("unsupported video generation platform: %s", account.Platform)
	}
}

func (s *MediaGenerationService) GetJobByPublicID(ctx context.Context, publicID string) (*MediaGenerationJob, error) {
	if s == nil || s.jobRepo == nil {
		return nil, fmt.Errorf("media generation job repository is not configured")
	}
	return s.jobRepo.GetByPublicID(ctx, publicID)
}

func (s *MediaGenerationService) GetAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, fmt.Errorf("account repository is not configured")
	}
	return s.accountRepo.GetByID(ctx, accountID)
}

func (s *MediaGenerationService) RefreshVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	if job == nil {
		return nil, fmt.Errorf("media generation job is required")
	}
	switch job.Provider {
	case MediaProviderDashScope:
		return s.refreshDashScopeVideoJob(ctx, job, account)
	case MediaProviderVolcengineArk:
		return s.refreshArkVideoJob(ctx, job, account)
	default:
		return nil, fmt.Errorf("unsupported video generation provider: %s", job.Provider)
	}
}

func (s *MediaGenerationService) createDashScopeVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error) {
	body, err := buildDashScopeVideoRequest(req)
	if err != nil {
		return nil, err
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, dashScopeBaseURL(account)+"/api/v1/services/aigc/video-generation/video-synthesis", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	upstreamReq.Header.Set("X-DashScope-Async", "enable")

	respBody, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: respBody, ResponseHeaders: headers}
	}
	job := s.newJob(meta, account, req.Model, MediaJobKindVideoGeneration, MediaProviderDashScope, PlatformDashScope)
	job.UpstreamTaskID = gjson.GetBytes(respBody, "output.task_id").String()
	job.UpstreamStatus = gjson.GetBytes(respBody, "output.task_status").String()
	job.UpstreamRequestID = gjson.GetBytes(respBody, "request_id").String()
	job.Status = mapDashScopeStatus(job.UpstreamStatus)
	job.UpstreamResponseJSON = append([]byte(nil), respBody...)
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *MediaGenerationService) refreshDashScopeVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dashScopeBaseURL(account)+"/api/v1/tasks/"+job.UpstreamTaskID, nil)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	body, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers}
	}
	upstreamStatus := gjson.GetBytes(body, "output.task_status").String()
	mediaStatus := mapDashScopeStatus(upstreamStatus)
	completedAt := completedAtForStatus(mediaStatus)
	resolution := firstNonEmpty(gjson.GetBytes(body, "usage.resolution").String(), gjson.GetBytes(body, "usage.SR").String())
	updated, err := s.jobRepo.UpdateFromUpstream(ctx, job.PublicID, MediaGenerationJobUpdate{
		Status:               mediaStatus,
		UpstreamStatus:       upstreamStatus,
		UpstreamRequestID:    gjson.GetBytes(body, "request_id").String(),
		UpstreamResponseJSON: body,
		ResultURL:            gjson.GetBytes(body, "output.video_url").String(),
		ResultContentType:    "video/mp4",
		VideoDurationSeconds: int(gjson.GetBytes(body, "usage.duration").Int()),
		VideoResolution:      resolution,
		VideoRatio:           gjson.GetBytes(body, "usage.ratio").String(),
		VideoCount:           int(gjson.GetBytes(body, "usage.video_count").Int()),
		ErrorCode:            gjson.GetBytes(body, "output.code").String(),
		ErrorMessage:         gjson.GetBytes(body, "output.message").String(),
		CompletedAt:          completedAt,
	})
	if err != nil {
		return nil, err
	}
	if updated != nil && updated.Status == MediaJobStatusSucceeded {
		_ = s.recordMediaUsage(ctx, updated)
	}
	return updated, nil
}

func (s *MediaGenerationService) createArkVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error) {
	body, err := buildArkVideoRequest(req)
	if err != nil {
		return nil, err
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, arkBaseURL(account)+"/contents/generations/tasks", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	respBody, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: respBody, ResponseHeaders: headers}
	}
	job := s.newJob(meta, account, req.Model, MediaJobKindVideoGeneration, MediaProviderVolcengineArk, PlatformVolcengineArk)
	job.UpstreamTaskID = firstNonEmpty(gjson.GetBytes(respBody, "id").String(), gjson.GetBytes(respBody, "task_id").String())
	job.UpstreamStatus = firstNonEmpty(gjson.GetBytes(respBody, "status").String(), gjson.GetBytes(respBody, "task_status").String())
	job.UpstreamRequestID = firstNonEmpty(headers.Get("x-request-id"), gjson.GetBytes(respBody, "request_id").String())
	job.Status = mapArkStatus(job.UpstreamStatus)
	job.UpstreamResponseJSON = append([]byte(nil), respBody...)
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *MediaGenerationService) refreshArkVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, arkBaseURL(account)+"/contents/generations/tasks/"+job.UpstreamTaskID, nil)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	body, headers, status, err := s.doJSONUpstream(upstreamReq, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers}
	}
	upstreamStatus := firstNonEmpty(gjson.GetBytes(body, "status").String(), gjson.GetBytes(body, "task_status").String())
	mediaStatus := mapArkStatus(upstreamStatus)
	completedAt := completedAtForStatus(mediaStatus)
	updated, err := s.jobRepo.UpdateFromUpstream(ctx, job.PublicID, MediaGenerationJobUpdate{
		Status:               mediaStatus,
		UpstreamStatus:       upstreamStatus,
		UpstreamRequestID:    firstNonEmpty(headers.Get("x-request-id"), gjson.GetBytes(body, "request_id").String()),
		UpstreamResponseJSON: body,
		ResultURL:            firstNonEmpty(gjson.GetBytes(body, "content.video_url").String(), gjson.GetBytes(body, "video_url").String()),
		ResultContentType:    "video/mp4",
		VideoDurationSeconds: int(gjson.GetBytes(body, "usage.duration").Int()),
		VideoResolution:      gjson.GetBytes(body, "usage.resolution").String(),
		VideoRatio:           gjson.GetBytes(body, "ratio").String(),
		VideoCount:           1,
		ErrorCode:            gjson.GetBytes(body, "error.code").String(),
		ErrorMessage:         gjson.GetBytes(body, "error.message").String(),
		CompletedAt:          completedAt,
	})
	if err != nil {
		return nil, err
	}
	if updated != nil && updated.Status == MediaJobStatusSucceeded {
		_ = s.recordMediaUsage(ctx, updated)
	}
	return updated, nil
}

func (s *MediaGenerationService) recordMediaUsage(ctx context.Context, job *MediaGenerationJob) error {
	if s == nil || s.jobRepo == nil || s.usageRepo == nil || job == nil {
		return nil
	}
	recorded, err := s.jobRepo.MarkUsageRecorded(ctx, job.PublicID, time.Now().UTC())
	if err != nil || !recorded {
		return err
	}
	billingMode := string(BillingModeVideo)
	if job.Kind == MediaJobKindAudioSpeech {
		billingMode = string(BillingModeAudio)
	}
	durationMs := job.VideoDurationSeconds * 1000
	mediaType := firstNonEmpty(job.VideoResolution, job.AudioFormat)
	_, err = s.usageRepo.Create(ctx, &UsageLog{
		UserID:         job.UserID,
		APIKeyID:       job.APIKeyID,
		AccountID:      job.AccountID,
		GroupID:        job.GroupID,
		RequestID:      job.PublicID,
		Model:          job.Model,
		BillingMode:    &billingMode,
		RequestType:    RequestTypeSync,
		DurationMs:     &durationMs,
		ImageCount:     job.VideoCount,
		MediaType:      &mediaType,
		CreatedAt:      time.Now().UTC(),
		RateMultiplier: 1,
	})
	return err
}

func (s *MediaGenerationService) newJob(meta MediaRequestMeta, account *Account, model, kind, provider, platform string) *MediaGenerationJob {
	now := time.Now().UTC()
	prefix := "vidjob"
	if kind == MediaJobKindAudioSpeech {
		prefix = "audjob"
	}
	return &MediaGenerationJob{
		PublicID:    newMediaPublicID(prefix),
		Kind:        kind,
		Provider:    provider,
		Platform:    platform,
		Status:      MediaJobStatusQueued,
		UserID:      meta.UserID,
		APIKeyID:    meta.APIKeyID,
		GroupID:     meta.GroupID,
		AccountID:   account.ID,
		Model:       model,
		RequestJSON: append([]byte(nil), meta.RequestJSON...),
		CreatedAt:   now,
		UpdatedAt:   now,
		SubmittedAt: &now,
	}
}

func (s *MediaGenerationService) doJSONUpstream(req *http.Request, account *Account) ([]byte, http.Header, int, error) {
	resp, err := s.doUpstream(req, account)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.Header, resp.StatusCode, readErr
	}
	return body, resp.Header, resp.StatusCode, nil
}

func (s *MediaGenerationService) doUpstream(req *http.Request, account *Account) (*http.Response, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, fmt.Errorf("media generation upstream client is not configured")
	}
	concurrency := 1
	if account != nil && account.Concurrency > 0 {
		concurrency = account.Concurrency
	}
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	return s.httpUpstream.Do(req, "", accountID, concurrency)
}

func azureSpeechTTSEndpoint(account *Account) string {
	if endpoint := strings.TrimSpace(account.GetCredential("tts_endpoint")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	region := strings.TrimSpace(account.GetCredential("region"))
	return fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", region)
}

func azureSpeechBatchEndpoint(account *Account) string {
	if endpoint := strings.TrimSpace(account.GetCredential("batch_endpoint")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	region := strings.TrimSpace(account.GetCredential("region"))
	return fmt.Sprintf("https://%s.api.cognitive.microsoft.com/texttospeech/batchsyntheses", region)
}

func dashScopeBaseURL(account *Account) string {
	if baseURL := strings.TrimSpace(account.GetCredential("base_url")); baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return "https://dashscope.aliyuncs.com"
}

func arkBaseURL(account *Account) string {
	if baseURL := strings.TrimSpace(account.GetCredential("base_url")); baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return "https://ark.cn-beijing.volces.com/api/v3"
}

func mapDashScopeStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PENDING":
		return MediaJobStatusQueued
	case "RUNNING":
		return MediaJobStatusRunning
	case "SUCCEEDED":
		return MediaJobStatusSucceeded
	case "FAILED":
		return MediaJobStatusFailed
	case "CANCELED":
		return MediaJobStatusCanceled
	case "UNKNOWN":
		return MediaJobStatusUnknown
	default:
		return MediaJobStatusRunning
	}
}

func mapArkStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return MediaJobStatusQueued
	case "running":
		return MediaJobStatusRunning
	case "succeeded", "success":
		return MediaJobStatusSucceeded
	case "failed":
		return MediaJobStatusFailed
	case "canceled", "cancelled":
		return MediaJobStatusCanceled
	default:
		return MediaJobStatusRunning
	}
}

func mapAzureBatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success":
		return MediaJobStatusSucceeded
	case "failed":
		return MediaJobStatusFailed
	case "notstarted", "running":
		return MediaJobStatusRunning
	default:
		return MediaJobStatusRunning
	}
}

func completedAtForStatus(status string) *time.Time {
	switch status {
	case MediaJobStatusSucceeded, MediaJobStatusFailed, MediaJobStatusCanceled:
		now := time.Now().UTC()
		return &now
	default:
		return nil
	}
}

func newMediaPublicID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func escapeJSONString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return replacer.Replace(value)
}
