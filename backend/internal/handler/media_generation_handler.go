package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type mediaGenerationServiceAPI interface {
	ForwardAzureSpeech(context.Context, *service.Account, service.AzureSpeechRequest) (*service.MediaSyncAudioResult, []byte, http.Header, error)
	CreateAudioSpeechJob(context.Context, service.MediaRequestMeta, *service.Account, service.AzureSpeechRequest) (*service.MediaGenerationJob, error)
	RefreshAudioSpeechJob(context.Context, *service.MediaGenerationJob, *service.Account) (*service.MediaGenerationJob, error)
	CreateVideoJob(context.Context, service.MediaRequestMeta, *service.Account, service.VideoGenerationRequest) (*service.MediaGenerationJob, error)
	RefreshVideoJob(context.Context, *service.MediaGenerationJob, *service.Account) (*service.MediaGenerationJob, error)
	GetJobByPublicID(context.Context, string) (*service.MediaGenerationJob, error)
	GetAccountByID(context.Context, int64) (*service.Account, error)
}

type mediaAccountSelector interface {
	SelectAccountWithLoadAwareness(context.Context, *int64, string, string, map[int64]struct{}, string, int64) (*service.AccountSelectionResult, error)
}

type MediaGenerationHandler struct {
	mediaService        mediaGenerationServiceAPI
	accountSelector     mediaAccountSelector
	billingCacheService *service.BillingCacheService
}

func NewMediaGenerationHandler(mediaService *service.MediaGenerationService, gatewayService *service.GatewayService, billingCacheService *service.BillingCacheService) *MediaGenerationHandler {
	return &MediaGenerationHandler{mediaService: mediaService, accountSelector: gatewayService, billingCacheService: billingCacheService}
}

func (h *MediaGenerationHandler) AudioSpeech(c *gin.Context) {
	body, req, apiKey, subject, ok := h.parseAudioRequest(c)
	if !ok {
		return
	}
	account, ok := h.selectAccount(c, apiKey, subject, req.Model, service.PlatformAzureSpeech)
	if !ok {
		return
	}
	if h.mediaService == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Media generation service is not configured")
		return
	}
	_ = body
	result, audio, headers, err := h.mediaService.ForwardAzureSpeech(c.Request.Context(), account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	contentType := headers.Get("Content-Type")
	if result != nil && result.ContentType != "" {
		contentType = result.ContentType
	}
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	c.Data(http.StatusOK, contentType, audio)
}

func (h *MediaGenerationHandler) CreateAudioSpeechJob(c *gin.Context) {
	body, req, apiKey, subject, ok := h.parseAudioRequest(c)
	if !ok {
		return
	}
	account, ok := h.selectAccount(c, apiKey, subject, req.Model, service.PlatformAzureSpeech)
	if !ok {
		return
	}
	if h.mediaService == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Media generation service is not configured")
		return
	}
	job, err := h.mediaService.CreateAudioSpeechJob(c.Request.Context(), mediaMeta(apiKey, subject, body), account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, mediaJobResponse(job))
}

func (h *MediaGenerationHandler) GetAudioSpeechJob(c *gin.Context) {
	h.getMediaJob(c, service.MediaJobKindAudioSpeech)
}

func (h *MediaGenerationHandler) CreateVideoGeneration(c *gin.Context) {
	body, req, apiKey, subject, ok := h.parseVideoRequest(c)
	if !ok {
		return
	}
	expectedPlatform := expectedVideoPlatform(req.Model)
	account, ok := h.selectAccount(c, apiKey, subject, req.Model, expectedPlatform)
	if !ok {
		return
	}
	if h.mediaService == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Media generation service is not configured")
		return
	}
	job, err := h.mediaService.CreateVideoJob(c.Request.Context(), mediaMeta(apiKey, subject, body), account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, mediaJobResponse(job))
}

func (h *MediaGenerationHandler) GetVideoGeneration(c *gin.Context) {
	h.getMediaJob(c, service.MediaJobKindVideoGeneration)
}

func (h *MediaGenerationHandler) getMediaJob(c *gin.Context, kind string) {
	apiKey, subject, ok := mediaAuthContext(c)
	if !ok {
		return
	}
	if h.mediaService == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Media generation service is not configured")
		return
	}
	job, err := h.mediaService.GetJobByPublicID(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	if job == nil || job.Kind != kind || (job.APIKeyID != apiKey.ID && job.UserID != subject.UserID) {
		mediaError(c, http.StatusNotFound, "not_found_error", "Media generation job not found")
		return
	}
	account, err := h.mediaService.GetAccountByID(c.Request.Context(), job.AccountID)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	if kind == service.MediaJobKindAudioSpeech {
		job, err = h.mediaService.RefreshAudioSpeechJob(c.Request.Context(), job, account)
	} else {
		job, err = h.mediaService.RefreshVideoJob(c.Request.Context(), job, account)
	}
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, mediaJobResponse(job))
}

func (h *MediaGenerationHandler) parseAudioRequest(c *gin.Context) ([]byte, service.AzureSpeechRequest, *service.APIKey, middleware2.AuthSubject, bool) {
	body, apiKey, subject, ok := readMediaBody(c)
	if !ok {
		return nil, service.AzureSpeechRequest{}, nil, middleware2.AuthSubject{}, false
	}
	var req service.AzureSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	req.Model = strings.TrimSpace(req.Model)
	req.Input = strings.TrimSpace(req.Input)
	if req.Model == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	if req.Input == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "input is required")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	return body, req, apiKey, subject, true
}

func (h *MediaGenerationHandler) parseVideoRequest(c *gin.Context) ([]byte, service.VideoGenerationRequest, *service.APIKey, middleware2.AuthSubject, bool) {
	body, apiKey, subject, ok := readMediaBody(c)
	if !ok {
		return nil, service.VideoGenerationRequest{}, nil, middleware2.AuthSubject{}, false
	}
	var req service.VideoGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Model == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	if req.Prompt == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return nil, req, nil, middleware2.AuthSubject{}, false
	}
	return body, req, apiKey, subject, true
}

func readMediaBody(c *gin.Context) ([]byte, *service.APIKey, middleware2.AuthSubject, bool) {
	apiKey, subject, ok := mediaAuthContext(c)
	if !ok {
		return nil, nil, middleware2.AuthSubject{}, false
	}
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return nil, nil, middleware2.AuthSubject{}, false
	}
	if len(body) == 0 {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return nil, nil, middleware2.AuthSubject{}, false
	}
	return body, apiKey, subject, true
}

func mediaAuthContext(c *gin.Context) (*service.APIKey, middleware2.AuthSubject, bool) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "API key is required")
		return nil, middleware2.AuthSubject{}, false
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "User authentication context is required")
		return nil, middleware2.AuthSubject{}, false
	}
	return apiKey, subject, true
}

func (h *MediaGenerationHandler) selectAccount(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, model string, expectedPlatform string) (*service.Account, bool) {
	if h.accountSelector == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Account scheduler is not configured")
		return nil, false
	}
	selection, err := h.accountSelector.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, "", model, nil, "", subject.UserID)
	if err != nil || selection == nil || selection.Account == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "No available account for media generation")
		return nil, false
	}
	account := selection.Account
	if expectedPlatform != "" && account.Platform != expectedPlatform {
		if selection.Acquired && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "selected account platform does not support requested media model")
		return nil, false
	}
	return account, true
}

func mediaMeta(apiKey *service.APIKey, subject middleware2.AuthSubject, body []byte) service.MediaRequestMeta {
	return service.MediaRequestMeta{UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.GroupID, RequestJSON: append([]byte(nil), body...)}
}

func expectedVideoPlatform(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "happyhorse"):
		return service.PlatformDashScope
	case strings.Contains(model, "seedance"):
		return service.PlatformVolcengineArk
	default:
		return ""
	}
}

func mediaJobResponse(job *service.MediaGenerationJob) gin.H {
	if job == nil {
		return gin.H{"object": "media.generation.job", "status": service.MediaJobStatusUnknown}
	}
	object := "media.generation.job"
	if job.Kind == service.MediaJobKindVideoGeneration {
		object = "video.generation.job"
	} else if job.Kind == service.MediaJobKindAudioSpeech {
		object = "audio.speech.job"
	}
	resp := gin.H{
		"id":               job.PublicID,
		"object":           object,
		"status":           job.Status,
		"provider":         job.Provider,
		"model":            job.Model,
		"created_at":       job.CreatedAt.Unix(),
		"upstream_task_id": job.UpstreamTaskID,
	}
	if job.ResultURL != "" {
		resp["url"] = job.ResultURL
	}
	if job.ErrorCode != "" || job.ErrorMessage != "" {
		resp["error"] = gin.H{"code": job.ErrorCode, "message": job.ErrorMessage}
	}
	if job.ExpiresAt != nil {
		resp["expires_at"] = job.ExpiresAt.Unix()
	}
	return resp
}

func (h *MediaGenerationHandler) writeMediaServiceError(c *gin.Context, err error) {
	var failoverErr *service.UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		status := failoverErr.StatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		mediaError(c, status, "upstream_error", string(failoverErr.ResponseBody))
		return
	}
	mediaError(c, http.StatusInternalServerError, "api_error", err.Error())
}

func mediaError(c *gin.Context, status int, typ string, message string) {
	if message == "" {
		message = http.StatusText(status)
	}
	c.JSON(status, gin.H{"error": gin.H{"type": typ, "message": message}})
}
