package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestMediaResourcesStub struct {
	service.MediaGenerationJobRepository
	job *service.MediaGenerationJob
}

func (r *requestMediaResourcesStub) GetByPublicID(context.Context, string) (*service.MediaGenerationJob, error) {
	return r.job, nil
}

func requestMediaFixture() (*service.APIKeyService, *service.APIKey, *requestMediaResourcesStub) {
	_, key := requestRoutingFixture()
	group := &service.Group{ID: 99, Platform: service.PlatformAll, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, Hydrated: true, RateMultiplier: 7}
	account := service.Account{ID: 41, Platform: service.PlatformDashScope, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true}
	// Put another default group first: synthesis must still choose the recorded
	// voice pool, never whichever pool happened to be the first scheduler match.
	defaultGroup := *group
	defaultGroup.ID, defaultGroup.RateMultiplier = 42, 1
	defaultAccount := account
	defaultAccount.ID = 3
	repo := &requestRoutingGroupsStub{candidates: []service.GatewayRoutingCandidate{
		{Group: &defaultGroup, Accounts: []service.Account{defaultAccount}},
		{Group: group, Accounts: []service.Account{account}},
	}}
	keys := service.NewAPIKeyService(nil, nil, repo, nil, nil, nil, &config.Config{})
	id := group.ID
	resources := &requestMediaResourcesStub{job: &service.MediaGenerationJob{PublicID: "voice_owned", Kind: service.MediaJobKindVoiceClone, Platform: service.PlatformDashScope, Provider: service.MediaProviderDashScope, UserID: key.UserID, APIKeyID: key.ID, GroupID: &id, AccountID: account.ID}}
	keys.GatewayMediaResources = resources
	return keys, key, resources
}

func TestRequestResourceResolutionPinsPoolBeforeBillingAndPreservesBody(t *testing.T) {
	keys, key, _ := requestMediaFixture()
	body := []byte(`{"model":"qwen3-tts-vc-2026-01-22","voice":"voice_owned","input":"hi"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	resolved, err := resolveRequestGatewayGroup(c, keys, key)
	require.NoError(t, err)
	require.Equal(t, int64(99), *resolved.GroupID)
	require.Equal(t, service.PlatformDashScope, resolved.Group.Platform)
	require.Equal(t, float64(7), resolved.Group.RateMultiplier)
	require.Same(t, resolved.Group, c.Request.Context().Value(ctxkey.Group))
	restored, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, body, restored)
	require.Nil(t, key.GroupID)
}

func TestRequestResourceMetadataUsesRecordedGroupAndRejectsForeignOwnership(t *testing.T) {
	for _, path := range []string{"/v1/audio/voices/voice_owned", "/audio/voices/voice_owned", "/v1/videos/generations/video_owned", "/v1/audio/speech/jobs/speech_owned"} {
		t.Run(path, func(t *testing.T) {
			keys, key, resources := requestMediaFixture()
			if path == "/v1/videos/generations/video_owned" {
				resources.job.Kind = service.MediaJobKindVideoGeneration
			}
			if path == "/v1/audio/speech/jobs/speech_owned" {
				resources.job.Kind = service.MediaJobKindAudioSpeech
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			resolved, err := resolveRequestGatewayGroup(c, keys, key)
			require.NoError(t, err)
			require.Equal(t, int64(99), *resolved.GroupID)
			resources.job.UserID++
			_, err = resolveRequestGatewayGroup(c, keys, key)
			require.ErrorIs(t, err, service.ErrGatewayMediaResourceNotFound)
			status, _, _ := gatewayGroupRoutingError(err)
			require.Equal(t, http.StatusNotFound, status)
		})
	}
}
