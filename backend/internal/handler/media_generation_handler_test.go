package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMediaGenerationHandler_AudioSpeechRequiresModelAndInput(t *testing.T) {
	c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/audio/speech", `{}`)
	h := &MediaGenerationHandler{}

	h.AudioSpeech(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "invalid_request_error", jsonPathString(t, w.Body.Bytes(), "error.type"))
}

func TestMediaGenerationHandler_CreateVideoGenerationReturnsJobObject(t *testing.T) {
	account := &service.Account{ID: 42, Platform: service.PlatformDashScope}
	mediaSvc := &mediaGenerationServiceStub{
		videoJob: &service.MediaGenerationJob{
			PublicID:       "vidjob_test",
			Kind:           service.MediaJobKindVideoGeneration,
			Provider:       service.MediaProviderDashScope,
			Platform:       service.PlatformDashScope,
			Status:         service.MediaJobStatusQueued,
			UpstreamTaskID: "task-1",
			AccountID:      42,
			Model:          "happyhorse-1.0-r2v",
			CreatedAt:      time.Now().UTC(),
		},
	}
	h := &MediaGenerationHandler{mediaService: mediaSvc, accountSelector: &mediaGenerationAccountSelectorStub{account: account}}
	c, w := newMediaGenerationHandlerTestContext(http.MethodPost, "/v1/videos/generations", `{"model":"happyhorse-1.0-r2v","prompt":"draw"}`)

	h.CreateVideoGeneration(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "video.generation.job", jsonPathString(t, w.Body.Bytes(), "object"))
	require.Equal(t, "vidjob_test", jsonPathString(t, w.Body.Bytes(), "id"))
	require.Equal(t, service.MediaJobStatusQueued, jsonPathString(t, w.Body.Bytes(), "status"))
	require.Equal(t, int64(7), mediaSvc.lastMeta.UserID)
	require.Equal(t, int64(8), mediaSvc.lastMeta.APIKeyID)
}

func TestMediaGenerationHandler_GetVideoGenerationEnforcesOwnership(t *testing.T) {
	h := &MediaGenerationHandler{mediaService: &mediaGenerationServiceStub{
		job: &service.MediaGenerationJob{
			PublicID: "vidjob_other",
			Kind:     service.MediaJobKindVideoGeneration,
			UserID:   999,
			APIKeyID: 998,
			Status:   service.MediaJobStatusSucceeded,
		},
	}}
	c, w := newMediaGenerationHandlerTestContext(http.MethodGet, "/v1/videos/generations/vidjob_other", "")
	c.Params = gin.Params{{Key: "id", Value: "vidjob_other"}}

	h.GetVideoGeneration(c)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func newMediaGenerationHandlerTestContext(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	apiKey := &service.APIKey{ID: 8, UserID: 7, GroupID: int64PtrForMediaHandlerTest(3), User: &service.User{ID: 7}}
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7, Concurrency: 1})
	return c, w
}

type mediaGenerationServiceStub struct {
	audioResult *service.MediaSyncAudioResult
	audioBody   []byte
	audioHeader http.Header
	videoJob    *service.MediaGenerationJob
	job         *service.MediaGenerationJob
	account     *service.Account
	lastMeta    service.MediaRequestMeta
}

func (s *mediaGenerationServiceStub) ForwardAzureSpeech(context.Context, *service.Account, service.AzureSpeechRequest) (*service.MediaSyncAudioResult, []byte, http.Header, error) {
	return s.audioResult, s.audioBody, s.audioHeader, nil
}

func (s *mediaGenerationServiceStub) CreateAudioSpeechJob(_ context.Context, meta service.MediaRequestMeta, _ *service.Account, _ service.AzureSpeechRequest) (*service.MediaGenerationJob, error) {
	s.lastMeta = meta
	return s.videoJob, nil
}

func (s *mediaGenerationServiceStub) RefreshAudioSpeechJob(context.Context, *service.MediaGenerationJob, *service.Account) (*service.MediaGenerationJob, error) {
	return s.job, nil
}

func (s *mediaGenerationServiceStub) CreateVideoJob(_ context.Context, meta service.MediaRequestMeta, _ *service.Account, _ service.VideoGenerationRequest) (*service.MediaGenerationJob, error) {
	s.lastMeta = meta
	return s.videoJob, nil
}

func (s *mediaGenerationServiceStub) RefreshVideoJob(context.Context, *service.MediaGenerationJob, *service.Account) (*service.MediaGenerationJob, error) {
	return s.job, nil
}

func (s *mediaGenerationServiceStub) GetJobByPublicID(context.Context, string) (*service.MediaGenerationJob, error) {
	return s.job, nil
}

func (s *mediaGenerationServiceStub) GetAccountByID(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}

type mediaGenerationAccountSelectorStub struct {
	account *service.Account
}

func (s *mediaGenerationAccountSelectorStub) SelectAccountWithLoadAwareness(context.Context, *int64, string, string, map[int64]struct{}, string, int64) (*service.AccountSelectionResult, error) {
	return &service.AccountSelectionResult{Account: s.account}, nil
}

func int64PtrForMediaHandlerTest(v int64) *int64 { return &v }

func jsonPathString(t *testing.T, body []byte, path string) string {
	t.Helper()
	var parsed any
	require.NoError(t, json.Unmarshal(body, &parsed))
	current := parsed
	for _, part := range bytes.Split([]byte(path), []byte(".")) {
		m, ok := current.(map[string]any)
		require.True(t, ok)
		current = m[string(part)]
	}
	if current == nil {
		return ""
	}
	s, _ := current.(string)
	return s
}
