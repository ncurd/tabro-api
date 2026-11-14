package handler

import (
	"context"
	"net/http"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type realtimeSpeechHandlerStub struct {
	mediaAudioServiceAPI
	called bool
}

func (s *realtimeSpeechHandlerStub) StreamAudioTranscription(context.Context, service.MediaRequestMeta, *service.Account, string, []byte, service.SpeechSocket) error {
	s.called = true
	return nil
}

func (s *realtimeSpeechHandlerStub) ForwardQwenRealtimeSpeech(context.Context, service.MediaRequestMeta, *service.Account, service.AzureSpeechRequest) (*service.MediaSyncAudioResult, []byte, http.Header, error) {
	s.called = true
	return &service.MediaSyncAudioResult{ContentType: "audio/wav"}, []byte("RIFFactual-provider-audio"), nil, nil
}

func TestRealtimeSpeechHandlerRejectsUnauthorizedOrUnacquiredUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authenticated bool
		selection     *service.AccountSelectionResult
		want          int
	}{
		{"missing authentication", false, nil, http.StatusUnauthorized},
		{"no authorized account", true, &service.AccountSelectionResult{}, http.StatusServiceUnavailable},
		{"account concurrency denied", true, &service.AccountSelectionResult{Account: &service.Account{ID: 42, Platform: service.PlatformDashScope}}, http.StatusTooManyRequests},
		{"wrong platform", true, &service.AccountSelectionResult{Account: &service.Account{ID: 42, Platform: service.PlatformOpenAI}, Acquired: true}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			speech := &realtimeSpeechHandlerStub{}
			h := &MediaGenerationHandler{audioService: speech, accountSelector: &mediaGenerationAccountSelectorStub{selection: tc.selection}}
			c, w := newMediaGenerationHandlerTestContext(http.MethodGet, "/v1/audio/transcriptions/stream?model=listen", "")
			if !tc.authenticated {
				delete(c.Keys, string(middleware2.ContextKeyAPIKey))
			}
			h.AudioTranscriptionStream(c)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			require.False(t, speech.called, "supplier adapter must not run before authorization and concurrency admission")
		})
	}
}

func TestRealtimeSpeechHandlerAliasReturnsAudioFromDashScopeAdapter(t *testing.T) {
	speech := &realtimeSpeechHandlerStub{}
	h := &MediaGenerationHandler{audioService: speech, accountSelector: &mediaGenerationAccountSelectorStub{account: &service.Account{ID: 42, Platform: service.PlatformDashScope}}}
	c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/audio/speech", `{"model":"public-voice","input":"你好","response_format":"wav"}`)
	key, ok := middleware2.GetAPIKeyFromContext(c)
	require.True(t, ok)
	key.Group = &service.Group{ID: 3, Platform: service.PlatformDashScope}
	h.AudioSpeech(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, speech.called)
	require.Equal(t, "audio/wav", w.Header().Get("Content-Type"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "RIFFactual-provider-audio", w.Body.String())
}
