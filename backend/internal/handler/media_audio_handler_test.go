package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type mediaAudioHandlerStub struct {
	mediaAudioServiceAPI
	req       service.AudioTranscriptionRequest
	meta      service.MediaRequestMeta
	voice     *service.VoiceResource
	accountID int64
	err       error
}

func (s *mediaAudioHandlerStub) TranscribeAudio(_ context.Context, meta service.MediaRequestMeta, account *service.Account, req service.AudioTranscriptionRequest) (*service.AudioTranscriptionResult, error) {
	s.req, s.meta, s.accountID = req, meta, account.ID
	return &service.AudioTranscriptionResult{Text: "测试转写", Model: req.Model}, s.err
}
func (s *mediaAudioHandlerStub) GetClonedVoice(context.Context, service.MediaRequestMeta, string) (*service.VoiceResource, error) {
	return s.voice, s.err
}
func (s *mediaAudioHandlerStub) SynthesizeClonedVoice(_ context.Context, meta service.MediaRequestMeta, account *service.Account, req service.ClonedSpeechRequest) (*service.ClonedSpeechResult, error) {
	s.meta, s.accountID = meta, account.ID
	return &service.ClonedSpeechResult{URL: "https://example.com/speech.wav", Voice: req.Voice, Model: req.Model}, s.err
}
func TestAudioTranscriptionsJSONAndBillingContext(t *testing.T) {
	for _, source := range []string{`"audio_url":"https://example.com/a.wav"`, `"audio_base64":"YQ==","mime_type":"audio/wav"`} {
		audio := &mediaAudioHandlerStub{}
		h := &MediaGenerationHandler{audioService: audio, accountSelector: &mediaGenerationAccountSelectorStub{account: &service.Account{ID: 42, Platform: service.PlatformDashScope}}}
		c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/audio/transcriptions", `{"model":"qwen3-asr-flash",`+source+`}`)
		h.AudioTranscriptions(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, "测试转写", jsonPathString(t, w.Body.Bytes(), "text"))
		require.NotNil(t, audio.meta.APIKey)
		require.Equal(t, int64(8), audio.meta.APIKey.ID)
		require.Equal(t, int64(42), audio.accountID)
		require.NotEmpty(t, audio.req.AudioURL+audio.req.AudioBase64)
	}
}
func TestClonedSpeechUsesOriginalAccountAndCurrentGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []int64
		status string
		want   int
	}{
		{"original credential", []int64{3}, service.StatusActive, http.StatusOK},
		{"removed from group", []int64{4}, service.StatusActive, http.StatusForbidden},
		{"disabled account", []int64{3}, service.StatusDisabled, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audio := &mediaAudioHandlerStub{voice: &service.VoiceResource{ID: "voice_test", AccountID: 42, Model: service.QwenVoiceCloneModel, Status: "active"}}
			account := &service.Account{ID: 42, Platform: service.PlatformDashScope, GroupIDs: tc.groups, Status: tc.status, Schedulable: true}
			h := &MediaGenerationHandler{audioService: audio, mediaService: &mediaGenerationServiceStub{account: account}}
			c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/audio/speech", `{"model":"qwen3-tts-vc-2026-01-22","voice":"voice_test","input":"你好","response_format":"url"}`)
			h.AudioSpeech(c)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusOK {
				require.Equal(t, int64(42), audio.accountID)
				require.Equal(t, "https://example.com/speech.wav", jsonPathString(t, w.Body.Bytes(), "url"))
			} else {
				require.Zero(t, audio.accountID)
			}
		})
	}
}
func TestMediaBillingErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{service.ErrMediaPricingNotConfigured, http.StatusBadRequest},
		{service.ErrMediaBillingUnavailable, http.StatusServiceUnavailable},
		{service.ErrClonedVoiceNotFound, http.StatusNotFound},
	} {
		c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/audio/transcriptions", `{}`)
		(&MediaGenerationHandler{}).writeMediaServiceError(c, tc.err)
		require.Equal(t, tc.status, w.Code)
	}
}
