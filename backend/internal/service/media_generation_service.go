package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
)

type MediaGenerationService struct {
	accountRepo  AccountRepository
	jobRepo      MediaGenerationJobRepository
	usageRepo    UsageLogRepository
	httpUpstream HTTPUpstream
	cfg          *config.Config
	mediaBilling *MediaBillingService
	workerMu     sync.Mutex
	workerCancel context.CancelFunc
	workerDone   chan struct{}
}

type MediaRequestMeta struct {
	UserID              int64
	APIKeyID            int64
	GroupID             *int64
	RequestJSON         []byte
	APIKey              *APIKey
	Subscription        *UserSubscription
	billingSnapshotJSON []byte
	preparedJob         *MediaGenerationJob
	upstreamModel       string
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
	mediaBilling *MediaBillingService,
) *MediaGenerationService {
	return &MediaGenerationService{
		accountRepo:  accountRepo,
		jobRepo:      jobRepo,
		usageRepo:    usageRepo,
		httpUpstream: httpUpstream,
		cfg:          cfg,
		mediaBilling: mediaBilling,
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
		if err := s.recordMediaUsage(ctx, updated); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func (s *MediaGenerationService) CreateVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error) {
	if account == nil {
		return nil, fmt.Errorf("video generation account is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if s.jobRepo == nil {
		return nil, fmt.Errorf("media generation job repository is not configured")
	}
	provider := ""
	switch account.Platform {
	case PlatformDashScope:
		provider = MediaProviderDashScope
	case PlatformVolcengineArk:
		provider = MediaProviderVolcengineArk
	default:
		return nil, fmt.Errorf("unsupported video generation platform: %s", account.Platform)
	}
	var mappingErr error
	meta.upstreamModel, mappingErr = s.resolveMediaUpstreamModel(ctx, meta, account, req.Model)
	if mappingErr != nil {
		return nil, mappingErr
	}
	if s.cfg == nil || s.cfg.RunMode != config.RunModeSimple {
		snapshot, err := s.mediaBilling.Prepare(ctx, meta, account, req.Model, meta.upstreamModel, MediaJobKindVideoGeneration, req.Resolution)
		if err != nil {
			return nil, err
		}
		meta.billingSnapshotJSON = snapshot.JSON()
	}
	job := s.newJob(meta, account, req.Model, MediaJobKindVideoGeneration, provider, account.Platform)
	// Persist identity and immutable prices before submitting any billable provider work.
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, err
	}
	meta.preparedJob = job
	var result *MediaGenerationJob
	var err error
	if account.Platform == PlatformDashScope {
		result, err = s.createDashScopeVideoJob(ctx, meta, account, req)
	} else {
		result, err = s.createArkVideoJob(ctx, meta, account, req)
	}
	if err != nil && job.UpstreamTaskID == "" {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, MediaGenerationJobUpdate{Status: MediaJobStatusFailed, ErrorCode: "submission_failed", ErrorMessage: err.Error(), CompletedAt: completedAtForStatus(MediaJobStatusFailed)})
	}
	return result, err
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
	if isCompletedMediaJob(job.Status) {
		if job.Status != MediaJobStatusSucceeded {
			return job, nil
		}
		if err := s.recordMediaUsage(ctx, job); err != nil {
			if !errors.Is(err, ErrMediaUsageIncomplete) || !MediaJobNeedsUsageRefresh(job) {
				return nil, err
			}
			if account == nil {
				var accountErr error
				account, accountErr = s.GetAccountByID(ctx, job.AccountID)
				if accountErr != nil {
					return nil, accountErr
				}
			}
		} else {
			return job, nil
		}
	}
	if account == nil {
		return nil, fmt.Errorf("video generation account is required")
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
	requestedModel := req.Model
	req.Model = firstNonEmpty(meta.upstreamModel, account.GetMappedModel(req.Model))
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
	job := meta.preparedJob
	job.Model = requestedModel
	job.UpstreamTaskID = gjson.GetBytes(respBody, "output.task_id").String()
	job.UpstreamStatus = gjson.GetBytes(respBody, "output.task_status").String()
	job.UpstreamRequestID = gjson.GetBytes(respBody, "request_id").String()
	if job.UpstreamTaskID == "" {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: respBody, ResponseHeaders: headers}
	}
	job.Status = mapDashScopeStatus(job.UpstreamStatus)
	job.VideoDurationSeconds = max(req.Duration, 0)
	job.VideoResolution = req.Resolution
	job.VideoRatio = req.Ratio
	job.UpstreamResponseJSON = append([]byte(nil), respBody...)
	return s.persistVideoSubmission(ctx, job)
}

func (s *MediaGenerationService) refreshDashScopeVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dashScopeBaseURL(account)+"/api/v1/tasks/"+url.PathEscape(job.UpstreamTaskID), nil)
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
	if upstreamStatus == "" {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, ResponseHeaders: headers}
	}
	mediaStatus := mapDashScopeStatus(upstreamStatus)
	completedAt := completedAtForStatus(mediaStatus)
	resolution := gjson.GetBytes(body, "usage.resolution").String()
	if resolution == "" {
		if sr := gjson.GetBytes(body, "usage.SR").String(); sr != "" {
			resolution = strings.TrimSuffix(strings.ToUpper(sr), "P") + "P"
		}
	}
	updated, err := s.jobRepo.UpdateFromUpstream(ctx, job.PublicID, MediaGenerationJobUpdate{
		Status:               mediaStatus,
		UpstreamStatus:       upstreamStatus,
		UpstreamRequestID:    gjson.GetBytes(body, "request_id").String(),
		UpstreamResponseJSON: body,
		ResultURL:            gjson.GetBytes(body, "output.video_url").String(),
		ResultContentType:    "video/mp4",
		VideoDurationSeconds: dashScopeVideoDuration(body),
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
		if err := s.recordMediaUsage(ctx, updated); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func (s *MediaGenerationService) createArkVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error) {
	requestedModel := req.Model
	req.Model = firstNonEmpty(meta.upstreamModel, account.GetMappedModel(req.Model))
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
	job := meta.preparedJob
	job.Model = requestedModel
	job.UpstreamTaskID = firstNonEmpty(gjson.GetBytes(respBody, "id").String(), gjson.GetBytes(respBody, "task_id").String())
	job.UpstreamStatus = firstNonEmpty(gjson.GetBytes(respBody, "status").String(), gjson.GetBytes(respBody, "task_status").String())
	job.UpstreamRequestID = firstNonEmpty(headers.Get("x-request-id"), gjson.GetBytes(respBody, "request_id").String())
	if job.UpstreamTaskID == "" {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: respBody, ResponseHeaders: headers}
	}
	job.Status = mapArkStatus(job.UpstreamStatus)
	job.VideoDurationSeconds = max(req.Duration, 0)
	job.VideoResolution = req.Resolution
	job.VideoRatio = req.Ratio
	job.UpstreamResponseJSON = append([]byte(nil), respBody...)
	return s.persistVideoSubmission(ctx, job)
}

func (s *MediaGenerationService) refreshArkVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error) {
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, arkBaseURL(account)+"/contents/generations/tasks/"+url.PathEscape(job.UpstreamTaskID), nil)
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
	if upstreamStatus == "" {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, ResponseHeaders: headers}
	}
	mediaStatus := mapArkStatus(upstreamStatus)
	errorCode := gjson.GetBytes(body, "error.code").String()
	errorMessage := gjson.GetBytes(body, "error.message").String()
	if strings.EqualFold(upstreamStatus, "expired") {
		errorCode = firstNonEmpty(errorCode, "task_expired")
		errorMessage = firstNonEmpty(errorMessage, "Video generation task expired")
	}
	completedAt := completedAtForStatus(mediaStatus)
	updated, err := s.jobRepo.UpdateFromUpstream(ctx, job.PublicID, MediaGenerationJobUpdate{
		Status:               mediaStatus,
		UpstreamStatus:       upstreamStatus,
		UpstreamRequestID:    firstNonEmpty(headers.Get("x-request-id"), gjson.GetBytes(body, "request_id").String()),
		UpstreamResponseJSON: body,
		ResultURL:            firstNonEmpty(gjson.GetBytes(body, "content.video_url").String(), gjson.GetBytes(body, "video_url").String()),
		ResultContentType:    arkVideoContentType(body),
		VideoDurationSeconds: videoDurationFromResponse(body),
		VideoResolution:      firstNonEmpty(gjson.GetBytes(body, "resolution").String(), gjson.GetBytes(body, "usage.resolution").String()),
		VideoRatio:           gjson.GetBytes(body, "ratio").String(),
		VideoCount:           1,
		ErrorCode:            errorCode,
		ErrorMessage:         errorMessage,
		CompletedAt:          completedAt,
	})
	if err != nil {
		return nil, err
	}
	if updated != nil && updated.Status == MediaJobStatusSucceeded {
		if err := s.recordMediaUsage(ctx, updated); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func (s *MediaGenerationService) recordMediaUsage(ctx context.Context, job *MediaGenerationJob) error {
	if s == nil || s.jobRepo == nil || job == nil {
		return nil
	}
	if job.UsageRecordedAt != nil {
		return nil
	}
	if len(job.BillingSnapshotJSON) > 0 {
		if err := s.mediaBilling.Settle(ctx, job); err != nil {
			return err
		}
		_, err := s.jobRepo.MarkUsageRecorded(ctx, job.PublicID, time.Now().UTC())
		if err == nil {
			now := time.Now().UTC()
			job.UsageRecordedAt = &now
		}
		return err
	}

	if s.usageRepo == nil {
		return nil
	}
	billingMode := string(BillingModeVideo)
	if job.Kind == MediaJobKindAudioSpeech {
		billingMode = string(BillingModeAudio)
	}
	durationMs := job.VideoDurationSeconds * 1000
	mediaType := firstNonEmpty(job.VideoResolution, job.AudioFormat)
	_, err := s.usageRepo.Create(ctx, &UsageLog{
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
	if err != nil {
		return err
	}
	// Usage creation is idempotent by (request_id, api_key_id). Mark only after it
	// succeeds so a database error can be retried on the next job query.
	_, err = s.jobRepo.MarkUsageRecorded(ctx, job.PublicID, time.Now().UTC())
	if err == nil {
		now := time.Now().UTC()
		job.UsageRecordedAt = &now
	}

	return err
}

func (s *MediaGenerationService) newJob(meta MediaRequestMeta, account *Account, model, kind, provider, platform string) *MediaGenerationJob {
	now := time.Now().UTC()
	prefix := "vidjob"
	if kind == MediaJobKindAudioSpeech {
		prefix = "audjob"
	}
	return &MediaGenerationJob{
		PublicID:            newMediaPublicID(prefix),
		Kind:                kind,
		Provider:            provider,
		Platform:            platform,
		Status:              MediaJobStatusQueued,
		UserID:              meta.UserID,
		APIKeyID:            meta.APIKeyID,
		GroupID:             meta.GroupID,
		AccountID:           account.ID,
		Model:               model,
		RequestJSON:         append([]byte(nil), meta.RequestJSON...),
		BillingSnapshotJSON: append([]byte(nil), meta.billingSnapshotJSON...),
		NextPollAt:          mediaInitialPollAt(kind, now),
		CreatedAt:           now,
		UpdatedAt:           now,
		SubmittedAt:         &now,
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
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("media generation upstream request URL is required")
	}
	var allowlist config.URLAllowlistConfig
	if s.cfg != nil {
		allowlist = s.cfg.Security.URLAllowlist
	}
	var validationErr error
	if allowlist.Enabled {
		_, validationErr = urlvalidator.ValidateHTTPSURL(req.URL.String(), urlvalidator.ValidationOptions{
			AllowedHosts:     allowlist.UpstreamHosts,
			RequireAllowlist: true,
			AllowPrivate:     allowlist.AllowPrivateHosts,
		})
	} else {
		_, validationErr = urlvalidator.ValidateURLFormat(req.URL.String(), allowlist.AllowInsecureHTTP)
	}
	if validationErr != nil {
		return nil, fmt.Errorf("invalid media upstream URL: %w", validationErr)
	}
	concurrency := 1
	if account != nil && account.Concurrency > 0 {
		concurrency = account.Concurrency
	}
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	proxyURL := ""
	if account != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxyURL, accountID, concurrency)
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
	case "", "PENDING":
		return MediaJobStatusQueued
	case "RUNNING":
		return MediaJobStatusRunning
	case "SUCCEEDED":
		return MediaJobStatusSucceeded
	case "FAILED":
		return MediaJobStatusFailed
	case "CANCELED", "CANCELLED":
		return MediaJobStatusCanceled
	case "UNKNOWN":
		return MediaJobStatusUnknown
	default:
		return MediaJobStatusUnknown
	}
}

func mapArkStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "queued", "pending":
		return MediaJobStatusQueued
	case "running":
		return MediaJobStatusRunning
	case "succeeded", "success":
		return MediaJobStatusSucceeded
	case "failed", "expired":
		return MediaJobStatusFailed
	case "canceled", "cancelled":
		return MediaJobStatusCanceled
	default:
		return MediaJobStatusUnknown
	}
}

func mapAzureBatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success":
		return MediaJobStatusSucceeded
	case "failed", "expired":
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

func isCompletedMediaJob(status string) bool {
	return status == MediaJobStatusSucceeded || status == MediaJobStatusFailed || status == MediaJobStatusCanceled
}

func videoDurationFromResponse(body []byte) int {
	if duration := gjson.GetBytes(body, "duration"); duration.Exists() {
		return int(duration.Int())
	}
	if duration := gjson.GetBytes(body, "usage.duration"); duration.Exists() {
		return int(duration.Int())
	}
	if fps := gjson.GetBytes(body, "framespersecond").Int(); fps > 0 {
		return int((gjson.GetBytes(body, "frames").Int() + fps - 1) / fps)
	}
	return 0
}

func arkVideoContentType(body []byte) string {
	if strings.EqualFold(gjson.GetBytes(body, "output_format").String(), "mov") {
		return "video/quicktime"
	}
	return "video/mp4"
}

func dashScopeVideoDuration(body []byte) int {
	if duration := gjson.GetBytes(body, "usage.output_video_duration"); duration.Exists() {
		return int(duration.Int())
	}
	return int(gjson.GetBytes(body, "usage.duration").Int())
}

func mediaInitialPollAt(kind string, now time.Time) *time.Time {
	if kind != MediaJobKindVideoGeneration {
		return nil
	}
	at := now.Add(15 * time.Second)
	return &at
}

// Provider creation is not repeatable. Retry only storing its returned task ID.
func (s *MediaGenerationService) persistVideoSubmission(ctx context.Context, job *MediaGenerationJob) (*MediaGenerationJob, error) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		updated, err := s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, MediaGenerationJobUpdate{Status: job.Status, UpstreamTaskID: job.UpstreamTaskID, UpstreamStatus: job.UpstreamStatus, UpstreamRequestID: job.UpstreamRequestID, UpstreamResponseJSON: job.UpstreamResponseJSON, VideoDurationSeconds: job.VideoDurationSeconds, VideoResolution: job.VideoResolution, VideoRatio: job.VideoRatio})
		if err == nil && updated != nil {
			return updated, nil
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("persisted media job was not found")
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
			select {
			case <-persistCtx.Done():
				timer.Stop()
				attempt = 3
			case <-timer.C:
			}
		}
	}
	slog.Error("accepted media task requires persistence recovery", "job_id", job.PublicID, "upstream_task_id", job.UpstreamTaskID, "error", lastErr)
	return nil, fmt.Errorf("%w: accepted task %s (upstream %s) could not be persisted: %v", ErrMediaBillingUnavailable, job.PublicID, job.UpstreamTaskID, lastErr)
}

func (s *MediaGenerationService) resolveMediaUpstreamModel(ctx context.Context, meta MediaRequestMeta, account *Account, requested string) (string, error) {
	mapped := requested
	if s.mediaBilling != nil && s.mediaBilling.pricing != nil && meta.GroupID != nil {
		mapping := s.mediaBilling.pricing.ResolveChannelMapping(ctx, *meta.GroupID, requested)
		mapped = firstNonEmpty(mapping.MappedModel, requested)
	}
	if !account.IsModelSupported(mapped) {
		return "", &InvalidMediaRequestError{Message: "selected account does not support mapped media model " + mapped}
	}
	return account.GetMappedModel(mapped), nil
}
