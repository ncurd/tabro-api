package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type completedAudioReadStub struct {
	mediaGenerationServiceAPI
	job *service.MediaGenerationJob
}

func (s *completedAudioReadStub) GetJobByPublicID(context.Context, string) (*service.MediaGenerationJob, error) {
	return s.job, nil
}
func (s *completedAudioReadStub) GetAccountByID(context.Context, int64) (*service.Account, error) {
	panic("completed audio must not need the old credential")
}
func (s *completedAudioReadStub) RefreshAudioSpeechJob(context.Context, *service.MediaGenerationJob, *service.Account) (*service.MediaGenerationJob, error) {
	panic("completed audio must not poll its provider again")
}

func TestCompletedAudioResourceRemainsReadableAfterAccountRemoval(t *testing.T) {
	for _, status := range []string{service.MediaJobStatusSucceeded, service.MediaJobStatusFailed, service.MediaJobStatusCanceled} {
		t.Run(status, func(t *testing.T) {
			job := &service.MediaGenerationJob{PublicID: "speech_owned", Kind: service.MediaJobKindAudioSpeech, UserID: 7, APIKeyID: 8, GroupID: int64PtrForMediaHandlerTest(3), AccountID: 41, Status: status}
			h := &MediaGenerationHandler{mediaService: &completedAudioReadStub{job: job}}
			c, w := newMediaGenerationHandlerTestContext(http.MethodGet, "/v1/audio/speech/jobs/speech_owned", "")
			c.Params = gin.Params{{Key: "id", Value: "speech_owned"}}
			h.GetAudioSpeechJob(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, status, jsonPathString(t, w.Body.Bytes(), "status"))
		})
	}
}
