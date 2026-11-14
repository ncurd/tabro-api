package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type realtimeSpeechHandlerStub struct {
	mediaAudioServiceAPI
	called  bool
	failure error
}

func (s *realtimeSpeechHandlerStub) StreamAudioTranscription(context.Context, service.MediaRequestMeta, *service.Account, string, []byte, service.SpeechSocket) error {
	s.called = true
	return s.failure
}

func TestRealtimeSpeechTaskFailuresAreVisibleAfterSuccessfulUpgradeWithoutStoringAudio(t *testing.T) {
	for _, failure := range []error{service.ErrMediaPricingNotConfigured, errors.New("private-provider-response Bearer secret credential transcript")} {
		t.Run(service.DescribeRealtimeSpeechFailure(failure).Code, func(t *testing.T) {
			resetOpsErrorLoggerStateForTest(t)
			t.Cleanup(func() { resetOpsErrorLoggerStateForTest(t) })
			// Capture the real middleware queue without starting background DB workers.
			opsErrorLogOnce.Do(func() {})
			opsErrorLogQueue = make(chan opsErrorLogJob, 4)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			speech := &realtimeSpeechHandlerStub{failure: failure}
			h := &MediaGenerationHandler{audioService: speech, accountSelector: &mediaGenerationAccountSelectorStub{account: &service.Account{ID: 42, Platform: service.PlatformDashScope}}}
			fixture, _ := newMediaGenerationHandlerTestContext(http.MethodGet, "/v1/audio/transcriptions/stream?model=public-listen", "")
			router := gin.New()
			router.Use(func(c *gin.Context) {
				for key, value := range fixture.Keys {
					c.Set(key, value)
				}
				c.Header("X-Request-Id", "speech-visible-failure")
				c.Next()
			})
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.GET("/v1/audio/transcriptions/stream", h.AudioTranscriptionStream)
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, response, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/audio/transcriptions/stream?model=public-listen", nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
			err = conn.Write(ctx, ws.MessageText, []byte(`{"header":{"task_id":"test-session","action":"run-task"},"payload":{"input":{"text":"private-input"}}}`))
			require.NoError(t, err)
			_, body, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "task-failed", gjson.GetBytes(body, "header.event").String())
			require.Equal(t, service.DescribeRealtimeSpeechFailure(failure).Code, gjson.GetBytes(body, "header.error_code").String())
			require.NotContains(t, string(body), "private")
			require.NotContains(t, string(body), "secret")
			_, _, _ = conn.Read(ctx) // Acknowledge server close so the middleware can finish.
			select {
			case job := <-opsErrorLogQueue:
				entry := job.entry
				require.Equal(t, "websocket_task_error", entry.ErrorType)
				require.Equal(t, http.StatusSwitchingProtocols, entry.StatusCode)
				require.Equal(t, "speech-visible-failure", entry.RequestID)
				require.Equal(t, "public-listen", entry.Model)
				require.Equal(t, int64(42), *entry.AccountID)
				require.Equal(t, "/api-ws/v1/inference", entry.UpstreamEndpoint)
				require.Contains(t, entry.ErrorMessage, service.DescribeRealtimeSpeechFailure(failure).Code)
				require.NotContains(t, entry.ErrorMessage+entry.ErrorBody, "private")
				require.NotContains(t, entry.ErrorMessage+entry.ErrorBody, "secret")
				require.Nil(t, entry.RequestBodyJSON)
				require.Nil(t, entry.RequestHeadersJSON)
				require.False(t, entry.IsRetryable, "original audio must not be replayed by Ops")
				require.Empty(t, opsErrorLogQueue, "one failure must not be logged again as a recovered request")
			case <-ctx.Done():
				t.Fatal("upgraded task failure was not recorded in Ops")
			}
		})
	}
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
