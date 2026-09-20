package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

const qwenMultimodalPath = "/api/v1/services/aigc/multimodal-generation/generation"
const qwenVoiceCustomizationPath = "/api/v1/services/audio/tts/customization"

func (s *MediaGenerationService) TranscribeAudio(ctx context.Context, meta MediaRequestMeta, account *Account, req AudioTranscriptionRequest) (*AudioTranscriptionResult, error) {
	if err := validateQwenAudioAccount(account, req.Model); err != nil {
		return nil, err
	}
	model, err := s.resolveMediaUpstreamModel(ctx, meta, account, req.Model)
	if err != nil {
		return nil, err
	}
	if model != "qwen3-asr-flash" && model != "qwen3-asr-flash-2026-02-10" && model != "qwen3-asr-flash-2025-09-08" {
		return nil, &InvalidMediaRequestError{Message: "transcriptions require a supported synchronous Qwen3-ASR-Flash model"}
	}
	audio, err := normalizeAudioInput(req.AudioURL, req.AudioBase64, req.MIMEType, false)
	if err != nil {
		return nil, err
	}
	messages := make([]map[string]any, 0, 2)
	if req.Prompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": []map[string]string{{"text": req.Prompt}}})
	}
	messages = append(messages, map[string]any{"role": "user", "content": []map[string]string{{"audio": audio}}})
	options := map[string]any{}
	if req.Language != "" {
		options["language"] = req.Language
	}
	if req.EnableITN != nil {
		options["enable_itn"] = *req.EnableITN
	}
	snapshot, err := s.prepareQwenAudioBilling(ctx, meta, account, req.Model, model, MediaJobKindAudioTranscription)
	if err != nil {
		return nil, err
	}
	job, err := s.beginQwenAudioJob(ctx, meta, account, req.Model, MediaJobKindAudioTranscription, nil, snapshot, 0, "")
	if err != nil {
		return nil, err
	}
	body, err := s.callQwenAudio(ctx, account, qwenMultimodalPath, map[string]any{
		"model": model, "input": map[string]any{"messages": messages}, "parameters": map[string]any{"asr_options": options},
	})
	if err != nil {
		s.failQwenAudioJob(ctx, job)
		return nil, err
	}
	content := gjson.GetBytes(body, "output.choices.0.message.content")
	if !content.IsArray() {
		s.failQwenAudioJob(ctx, job)
		return nil, invalidQwenAudioResponse(body)
	}
	var text strings.Builder
	found := false
	for _, part := range content.Array() {
		if value := part.Get("text"); value.Type == gjson.String {
			found = true
			text.WriteString(value.String())
		}
	}
	if !found {
		s.failQwenAudioJob(ctx, job)
		return nil, invalidQwenAudioResponse(body)
	}
	if err := s.persistAndSettleQwenAudio(ctx, job, body); err != nil {
		return nil, err
	}
	return &AudioTranscriptionResult{Text: text.String(), Model: req.Model, RequestID: job.UpstreamRequestID, Usage: qwenAudioUsage(body)}, nil
}

func (s *MediaGenerationService) CreateClonedVoice(ctx context.Context, meta MediaRequestMeta, account *Account, req VoiceCloneRequest) (*VoiceResource, error) {
	if err := validateQwenAudioAccount(account, req.Model); err != nil {
		return nil, err
	}
	model, err := s.resolveMediaUpstreamModel(ctx, meta, account, req.Model)
	if err != nil {
		return nil, err
	}
	if model != QwenVoiceCloneModel {
		return nil, &InvalidMediaRequestError{Message: "voice cloning requires qwen3-tts-vc-2026-01-22 as the target model"}
	}
	if !qwenVoiceNamePattern.MatchString(req.Name) {
		return nil, &InvalidMediaRequestError{Message: "name must contain 1 to 16 ASCII letters, numbers, or underscores"}
	}
	audio, err := normalizeAudioInput(req.AudioURL, req.AudioBase64, req.MIMEType, true)
	if err != nil {
		return nil, err
	}
	input := map[string]any{"action": "create", "target_model": model, "preferred_name": req.Name, "audio": map[string]string{"data": audio}}
	if req.Text != "" {
		input["text"] = req.Text
	}
	if req.Language != "" {
		input["language"] = req.Language
	}
	snapshot, err := s.prepareQwenAudioBilling(ctx, meta, account, QwenVoiceEnrollmentModel, QwenVoiceEnrollmentModel, MediaJobKindVoiceClone)
	if err != nil {
		return nil, err
	}
	// Retain routing metadata only, never the reference recording or transcript.
	metadata, _ := json.Marshal(map[string]string{"name": req.Name, "language": req.Language, "upstream_model": model})
	job, err := s.beginQwenAudioJob(ctx, meta, account, req.Model, MediaJobKindVoiceClone, metadata, snapshot, 0, "")
	if err != nil {
		return nil, err
	}
	body, err := s.callQwenAudio(ctx, account, qwenVoiceCustomizationPath, map[string]any{"model": QwenVoiceEnrollmentModel, "input": input})
	if err != nil {
		s.failQwenAudioJob(ctx, job)
		return nil, err
	}
	voice := gjson.GetBytes(body, "output.voice").String()
	target := gjson.GetBytes(body, "output.target_model").String()
	if voice == "" || target != model {
		s.failQwenAudioJob(ctx, job)
		return nil, invalidQwenAudioResponse(body)
	}
	if err := s.persistAndSettleQwenAudio(ctx, job, body); err != nil {
		return nil, err
	}
	return clonedVoiceResource(job), nil
}

func (s *MediaGenerationService) GetClonedVoice(ctx context.Context, meta MediaRequestMeta, id string) (*VoiceResource, error) {
	job, err := s.getOwnedClonedVoice(ctx, meta, id)
	if err != nil {
		return nil, err
	}
	return clonedVoiceResource(job), nil
}

func (s *MediaGenerationService) ListClonedVoices(ctx context.Context, meta MediaRequestMeta, limit, offset int) ([]*VoiceResource, error) {
	if s == nil || s.jobRepo == nil {
		return nil, fmt.Errorf("media generation job repository is not configured")
	}
	repo, ok := s.jobRepo.(MediaAudioResourceRepository)
	if !ok {
		return nil, fmt.Errorf("media audio resource listing is not configured")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 || offset < 0 {
		return nil, &InvalidMediaRequestError{Message: "limit must be between 1 and 100 and offset must not be negative"}
	}
	jobs, err := repo.ListMediaResources(ctx, meta.UserID, meta.APIKeyID, meta.GroupID, MediaJobKindVoiceClone, limit, offset)
	if err != nil {
		return nil, err
	}
	voices := make([]*VoiceResource, 0, len(jobs))
	for _, job := range jobs {
		if ownsClonedVoice(meta, job) && job.Status == MediaJobStatusSucceeded {
			voices = append(voices, clonedVoiceResource(job))
		}
	}
	return voices, nil
}

func (s *MediaGenerationService) DeleteClonedVoice(ctx context.Context, meta MediaRequestMeta, account *Account, id string) (*VoiceResource, error) {
	job, err := s.getOwnedClonedVoice(ctx, meta, id)
	if err != nil {
		return nil, err
	}
	if job.Status == MediaJobStatusCanceled {
		return clonedVoiceResource(job), nil
	}
	if err := validatePinnedQwenVoiceAccount(account, job); err != nil {
		return nil, err
	}
	_, err = s.callQwenAudio(ctx, account, qwenVoiceCustomizationPath, map[string]any{
		"model": QwenVoiceEnrollmentModel, "input": map[string]string{"action": "delete", "voice": clonedVoiceProviderID(job)},
	})
	if err != nil {
		return nil, err
	}
	// Preserve enrollment response and metadata for stable resource identity.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	updated, err := s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, MediaGenerationJobUpdate{Status: MediaJobStatusCanceled, UpstreamStatus: "deleted", CompletedAt: job.CompletedAt})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrClonedVoiceNotFound
	}
	return clonedVoiceResource(updated), nil
}

func (s *MediaGenerationService) SynthesizeClonedVoice(ctx context.Context, meta MediaRequestMeta, account *Account, req ClonedSpeechRequest) (*ClonedSpeechResult, error) {
	job, err := s.getOwnedClonedVoice(ctx, meta, req.Voice)
	if err != nil {
		return nil, err
	}
	if job.Status != MediaJobStatusSucceeded {
		return nil, &InvalidMediaRequestError{Message: "cloned voice is not active"}
	}
	if err := validatePinnedQwenVoiceAccount(account, job); err != nil {
		return nil, err
	}
	voice := clonedVoiceResource(job)
	if req.Model == "" {
		req.Model = voice.Model
	}
	model, err := s.resolveMediaUpstreamModel(ctx, meta, account, req.Model)
	if err != nil {
		return nil, err
	}
	if model != voice.UpstreamModel {
		return nil, &InvalidMediaRequestError{Message: "speech model must match the model used to create the cloned voice"}
	}
	if strings.TrimSpace(req.Input) == "" || len([]rune(req.Input)) > 600 {
		return nil, &InvalidMediaRequestError{Message: "input must contain between 1 and 600 characters"}
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "url" {
		return nil, &InvalidMediaRequestError{Message: "Qwen cloned speech supports only response_format=url"}
	}
	input := map[string]string{"text": req.Input, "voice": clonedVoiceProviderID(job)}
	if req.Language != "" {
		input["language_type"] = qwenSpeechLanguage(req.Language)
	}
	snapshot, err := s.prepareQwenAudioBilling(ctx, meta, account, req.Model, voice.UpstreamModel, MediaJobKindAudioSpeech)
	if err != nil {
		return nil, err
	}
	usageJob, err := s.beginQwenAudioJob(ctx, meta, account, req.Model, MediaJobKindAudioSpeech, nil, snapshot, len([]rune(req.Input)), req.Voice)
	if err != nil {
		return nil, err
	}
	body, err := s.callQwenAudio(ctx, account, qwenMultimodalPath, map[string]any{"model": voice.UpstreamModel, "input": input})
	if err != nil {
		s.failQwenAudioJob(ctx, usageJob)
		return nil, err
	}
	resultURL := gjson.GetBytes(body, "output.audio.url").String()
	if resultURL == "" {
		s.failQwenAudioJob(ctx, usageJob)
		return nil, invalidQwenAudioResponse(body)
	}
	result := &ClonedSpeechResult{URL: resultURL, Model: req.Model, Voice: req.Voice, RequestID: gjson.GetBytes(body, "request_id").String(), Usage: qwenAudioUsage(body)}
	usageJob.ResultURL = resultURL
	if expiry := gjson.GetBytes(body, "output.audio.expires_at"); expiry.Type == gjson.Number && expiry.Int() > 0 {
		unix := expiry.Int()
		result.ExpiresAt = &unix
		expiresAt := time.Unix(unix, 0).UTC()
		usageJob.ExpiresAt = &expiresAt
	}
	if err := s.persistAndSettleQwenAudio(ctx, usageJob, body); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *MediaGenerationService) getOwnedClonedVoice(ctx context.Context, meta MediaRequestMeta, id string) (*MediaGenerationJob, error) {
	if !strings.HasPrefix(id, "voice_") {
		return nil, ErrClonedVoiceNotFound
	}
	job, err := s.GetJobByPublicID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ownsClonedVoice(meta, job) {
		return nil, ErrClonedVoiceNotFound
	}
	return job, nil
}

func ownsClonedVoice(meta MediaRequestMeta, job *MediaGenerationJob) bool {
	if job == nil || job.Kind != MediaJobKindVoiceClone || job.UserID != meta.UserID || job.APIKeyID != meta.APIKeyID {
		return false
	}
	return (job.GroupID == nil && meta.GroupID == nil) || (job.GroupID != nil && meta.GroupID != nil && *job.GroupID == *meta.GroupID)
}

func clonedVoiceResource(job *MediaGenerationJob) *VoiceResource {
	status := "active"
	if job.Status == MediaJobStatusCanceled {
		status = "deleted"
	} else if job.Status != MediaJobStatusSucceeded {
		status = job.Status
	}
	return &VoiceResource{
		ID: job.PublicID, Object: "audio.voice", Model: job.Model, Name: gjson.GetBytes(job.RequestJSON, "name").String(),
		Status: status, CreatedAt: job.CreatedAt.Unix(), Language: gjson.GetBytes(job.RequestJSON, "language").String(),
		FallbackMode: gjson.GetBytes(job.UpstreamResponseJSON, "output.fallback_mode").Bool(), FallbackReason: gjson.GetBytes(job.UpstreamResponseJSON, "output.fallback_reason").String(),
		AccountID: job.AccountID, UpstreamModel: gjson.GetBytes(job.RequestJSON, "upstream_model").String(),
	}
}

func validateQwenAudioAccount(account *Account, model string) error {
	if account == nil || account.Platform != PlatformDashScope || account.Type != AccountTypeAPIKey || account.GetCredential("api_key") == "" {
		return &InvalidMediaRequestError{Message: "Qwen audio requires a DashScope API key account"}
	}
	if model == "" {
		return &InvalidMediaRequestError{Message: "audio model is required"}
	}
	return nil
}

func validatePinnedQwenVoiceAccount(account *Account, job *MediaGenerationJob) error {
	if account == nil || account.ID != job.AccountID || job.Provider != MediaProviderDashScope || account.Platform != PlatformDashScope || account.Type != AccountTypeAPIKey || account.GetCredential("api_key") == "" {
		return &InvalidMediaRequestError{Message: "cloned voice must use its original DashScope account"}
	}
	return nil
}

func clonedVoiceProviderID(job *MediaGenerationJob) string {
	return firstNonEmpty(job.AudioVoice, gjson.GetBytes(job.UpstreamResponseJSON, "output.voice").String())
}

func (s *MediaGenerationService) callQwenAudio(ctx context.Context, account *Account, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dashScopeBaseURL(account)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	response, headers, status, err := s.doJSONUpstream(req, account)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: response, ResponseHeaders: headers}
	}
	if !gjson.ValidBytes(response) || gjson.GetBytes(response, "code").String() != "" {
		return nil, invalidQwenAudioResponse(response)
	}
	return response, nil
}

func (s *MediaGenerationService) prepareQwenAudioBilling(ctx context.Context, meta MediaRequestMeta, account *Account, model, upstreamModel, kind string) (*MediaBillingSnapshot, error) {
	if s == nil || s.jobRepo == nil {
		return nil, fmt.Errorf("media generation job repository is not configured")
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, nil
	}
	if s.mediaBilling == nil {
		return nil, ErrMediaBillingUnavailable
	}
	return s.mediaBilling.Prepare(ctx, meta, account, model, upstreamModel, kind, "")
}

func (s *MediaGenerationService) beginQwenAudioJob(ctx context.Context, meta MediaRequestMeta, account *Account, model, kind string, metadata []byte, snapshot *MediaBillingSnapshot, characters int, voice string) (*MediaGenerationJob, error) {
	job := s.newJob(meta, account, model, kind, MediaProviderDashScope, PlatformDashScope)
	job.PublicID = newMediaPublicID("audjob")
	if kind == MediaJobKindVoiceClone {
		job.PublicID = newMediaPublicID("voice")
	}
	job.Status = MediaJobStatusRunning
	job.NextPollAt = nil
	job.RequestJSON = metadata
	if len(metadata) == 0 {
		job.RequestJSON = []byte(`{}`)
	}
	job.AudioCharacterCount = characters
	job.AudioVoice = voice
	if kind == MediaJobKindAudioSpeech {
		job.AudioFormat = "url"
	}
	if snapshot != nil {
		job.BillingSnapshotJSON = snapshot.JSON()
	}
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *MediaGenerationService) persistAndSettleQwenAudio(ctx context.Context, job *MediaGenerationJob, body []byte) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	providerVoice := ""
	if job.Kind == MediaJobKindVoiceClone {
		providerVoice = gjson.GetBytes(body, "output.voice").String()
	}
	updated, err := s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, MediaGenerationJobUpdate{
		Status: MediaJobStatusSucceeded, UpstreamStatus: "SUCCEEDED", UpstreamRequestID: gjson.GetBytes(body, "request_id").String(),
		UpstreamResponseJSON: body, ResultURL: job.ResultURL, ExpiresAt: job.ExpiresAt, CompletedAt: completedAtForStatus(MediaJobStatusSucceeded), AudioVoice: providerVoice,
	})
	if err != nil {
		return err
	}
	if updated == nil {
		return fmt.Errorf("audio result resource is missing")
	}
	*job = *updated
	if s.mediaBilling != nil && len(job.BillingSnapshotJSON) > 0 {
		if err := s.mediaBilling.Settle(persistCtx, job); err != nil {
			slog.Error("audio billing pending reconciliation", "job_id", job.PublicID, "error", err)
			return nil
		}
		_, err := s.jobRepo.MarkUsageRecorded(persistCtx, job.PublicID, time.Now().UTC())
		if err != nil {
			slog.Error("audio billing marker pending reconciliation", "job_id", job.PublicID, "error", err)
		}
	}
	return nil
}

func (s *MediaGenerationService) failQwenAudioJob(ctx context.Context, job *MediaGenerationJob) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if _, err := s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, MediaGenerationJobUpdate{Status: MediaJobStatusFailed, CompletedAt: completedAtForStatus(MediaJobStatusFailed)}); err != nil {
		slog.Error("failed audio request state could not be persisted", "job_id", job.PublicID, "error", err)
	}
}

func qwenAudioUsage(body []byte) json.RawMessage {
	usage := gjson.GetBytes(body, "usage")
	if usage.IsObject() {
		return json.RawMessage(usage.Raw)
	}
	return nil
}

func invalidQwenAudioResponse(body []byte) error {
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body}
}

func qwenSpeechLanguage(language string) string {
	if mapped := map[string]string{"zh": "Chinese", "en": "English", "de": "German", "it": "Italian", "pt": "Portuguese", "es": "Spanish", "ja": "Japanese", "ko": "Korean", "fr": "French", "ru": "Russian", "auto": "Auto"}[strings.ToLower(language)]; mapped != "" {
		return mapped
	}
	return language
}
