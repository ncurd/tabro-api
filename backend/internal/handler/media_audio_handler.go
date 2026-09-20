package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type mediaAudioServiceAPI interface {
	TranscribeAudio(context.Context, service.MediaRequestMeta, *service.Account, service.AudioTranscriptionRequest) (*service.AudioTranscriptionResult, error)
	CreateClonedVoice(context.Context, service.MediaRequestMeta, *service.Account, service.VoiceCloneRequest) (*service.VoiceResource, error)
	GetClonedVoice(context.Context, service.MediaRequestMeta, string) (*service.VoiceResource, error)
	ListClonedVoices(context.Context, service.MediaRequestMeta, int, int) ([]*service.VoiceResource, error)
	DeleteClonedVoice(context.Context, service.MediaRequestMeta, *service.Account, string) (*service.VoiceResource, error)
	SynthesizeClonedVoice(context.Context, service.MediaRequestMeta, *service.Account, service.ClonedSpeechRequest) (*service.ClonedSpeechResult, error)
}

func (h *MediaGenerationHandler) AudioTranscriptions(c *gin.Context) {
	var req service.AudioTranscriptionRequest
	body, key, subject, ok := h.readAudioJSON(c, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	account, release, ok := h.selectAccount(c, key, subject, req.Model, service.PlatformDashScope)
	if !ok {
		return
	}
	defer release()
	result, err := h.audioService.TranscribeAudio(c.Request.Context(), mediaMeta(c, key, subject, body), account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *MediaGenerationHandler) CreateClonedVoice(c *gin.Context) {
	var req service.VoiceCloneRequest
	body, key, subject, ok := h.readAudioJSON(c, &req)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	account, release, ok := h.selectAccount(c, key, subject, req.Model, service.PlatformDashScope)
	if !ok {
		return
	}
	defer release()
	result, err := h.audioService.CreateClonedVoice(c.Request.Context(), mediaMeta(c, key, subject, body), account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *MediaGenerationHandler) ListClonedVoices(c *gin.Context) {
	key, subject, ok := mediaAuthContext(c)
	if !ok || !h.audioConfigured(c) {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "limit must be between 1 and 100")
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "offset must be a non-negative integer")
		return
	}
	result, err := h.audioService.ListClonedVoices(c.Request.Context(), mediaMeta(c, key, subject, nil), limit, offset)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	if result == nil {
		result = []*service.VoiceResource{}
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": result, "limit": limit, "offset": offset})
}

func (h *MediaGenerationHandler) GetClonedVoice(c *gin.Context) {
	key, subject, ok := mediaAuthContext(c)
	if !ok || !h.audioConfigured(c) {
		return
	}
	result, err := h.audioService.GetClonedVoice(c.Request.Context(), mediaMeta(c, key, subject, nil), c.Param("id"))
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *MediaGenerationHandler) DeleteClonedVoice(c *gin.Context) {
	key, subject, ok := mediaAuthContext(c)
	if !ok || !h.audioConfigured(c) {
		return
	}
	meta := mediaMeta(c, key, subject, nil)
	voice, err := h.audioService.GetClonedVoice(c.Request.Context(), meta, c.Param("id"))
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	if voice.Status == "deleted" {
		c.JSON(http.StatusOK, voice)
		return
	}
	account, release, ok := h.pinnedVoiceAccount(c, key, subject, voice, false)
	if !ok {
		return
	}
	defer release()
	result, err := h.audioService.DeleteClonedVoice(c.Request.Context(), meta, account, voice.ID)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *MediaGenerationHandler) clonedAudioSpeech(c *gin.Context, body []byte, key *service.APIKey, subject middleware2.AuthSubject) {
	if !h.audioConfigured(c) {
		return
	}
	var req service.ClonedSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	if !strings.HasPrefix(req.Voice, "voice_") {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "voice must be a local voice ID returned by /v1/audio/voices")
		return
	}
	meta := mediaMeta(c, key, subject, body)
	voice, err := h.audioService.GetClonedVoice(c.Request.Context(), meta, req.Voice)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	account, release, ok := h.pinnedVoiceAccount(c, key, subject, voice, true)
	if !ok {
		return
	}
	defer release()
	result, err := h.audioService.SynthesizeClonedVoice(c.Request.Context(), meta, account, req)
	if err != nil {
		h.writeMediaServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *MediaGenerationHandler) audioConfigured(c *gin.Context) bool {
	if h.audioService == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Audio service is not configured")
		return false
	}
	return true
}

func (h *MediaGenerationHandler) readAudioJSON(c *gin.Context, req any) ([]byte, *service.APIKey, middleware2.AuthSubject, bool) {
	body, key, subject, ok := readMediaBody(c)
	if !ok {
		return nil, nil, subject, false
	}
	if !h.audioConfigured(c) {
		return nil, nil, subject, false
	}
	if err := json.Unmarshal(body, req); err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "Use a JSON request with audio_url or audio_base64; file uploads are not supported")
		return nil, nil, subject, false
	}
	return body, key, subject, true
}

// Provider voices belong to the credential that enrolled them. Never fall back to
// a different account, even when the normal scheduler would prefer that account.
func (h *MediaGenerationHandler) pinnedVoiceAccount(c *gin.Context, key *service.APIKey, subject middleware2.AuthSubject, voice *service.VoiceResource, billable bool) (*service.Account, func(), bool) {
	if h.mediaService == nil || voice == nil {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Voice account is unavailable")
		return nil, nil, false
	}
	account, err := h.mediaService.GetAccountByID(c.Request.Context(), voice.AccountID)
	if err != nil || account == nil || account.Platform != service.PlatformDashScope {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Voice account is unavailable")
		return nil, nil, false
	}
	allowed := false
	if key.GroupID != nil {
		for _, id := range account.GroupIDs {
			if id == *key.GroupID {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		mediaError(c, http.StatusForbidden, "permission_error", "Voice account no longer belongs to this group")
		return nil, nil, false
	}
	if billable && (!account.IsSchedulable() || account.IsQuotaExceeded()) {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Voice account is not available for synthesis")
		return nil, nil, false
	}
	if billable && !h.checkBillingEligibility(c, key) {
		return nil, nil, false
	}
	var userRelease, accountRelease func()
	release := func() {
		if accountRelease != nil {
			accountRelease()
		}
		if userRelease != nil {
			userRelease()
		}
	}
	if h.concurrencyHelper != nil {
		var acquired bool
		userRelease, acquired, err = h.concurrencyHelper.TryAcquireUserSlot(c.Request.Context(), subject.UserID, subject.Concurrency)
		if err != nil || !acquired {
			mediaError(c, http.StatusTooManyRequests, "rate_limit_error", "User concurrency limit reached")
			return nil, nil, false
		}
		userRelease = wrapReleaseOnDone(c.Request.Context(), userRelease)
		accountRelease, acquired, err = h.concurrencyHelper.TryAcquireAccountSlot(c.Request.Context(), account.ID, account.Concurrency)
		if err != nil || !acquired {
			release()
			mediaError(c, http.StatusTooManyRequests, "rate_limit_error", "Voice account concurrency limit reached")
			return nil, nil, false
		}
		accountRelease = wrapReleaseOnDone(c.Request.Context(), accountRelease)
	}
	return account, release, true
}
