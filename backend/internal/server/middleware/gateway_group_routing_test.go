package middleware

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestRoutingGroupsStub struct {
	service.GroupRepository
	candidates []service.GatewayRoutingCandidate
}

func (r *requestRoutingGroupsStub) ListGatewayRoutingCandidates(context.Context, []int64, bool) ([]service.GatewayRoutingCandidate, error) {
	return r.candidates, nil
}

func requestRoutingFixture() (*service.APIKeyService, *service.APIKey) {
	group := &service.Group{ID: 42, Platform: service.PlatformAll, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, Hydrated: true}
	accounts := []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true},
		{ID: 2, Platform: service.PlatformGemini, Status: service.StatusActive, Schedulable: true},
		{ID: 3, Platform: service.PlatformDashScope, Status: service.StatusActive, Schedulable: true},
	}
	repo := &requestRoutingGroupsStub{candidates: []service.GatewayRoutingCandidate{{Group: group, Accounts: accounts}}}
	keys := service.NewAPIKeyService(nil, nil, repo, nil, nil, nil, &config.Config{})
	key := &service.APIKey{ID: 1, UserID: 2, GroupScope: service.APIKeyGroupScopePublic, User: &service.User{ID: 2, Status: service.StatusActive}}
	return keys, key
}

func TestRequestGroupResolutionSetsPricingContextAndPreservesJSONBody(t *testing.T) {
	keys, key := requestRoutingFixture()
	body := []byte("{\"model\":\"gpt-6-sol\",\"input\":\"hello\",\"stream\":true}")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	resolved, err := resolveRequestGatewayGroup(c, keys, key)
	require.NoError(t, err)
	require.Equal(t, int64(42), *resolved.GroupID)
	require.Equal(t, service.PlatformOpenAI, resolved.Group.Platform)
	require.Same(t, resolved.Group, c.Request.Context().Value(ctxkey.Group))
	restored, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, body, restored)
	require.Nil(t, key.Group)
}

func TestRequestGroupResolutionReadsGeminiModelFromPath(t *testing.T) {
	keys, key := requestRoutingFixture()
	body := []byte("{\"contents\":[]}")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro:generateContent", bytes.NewReader(body))
	resolved, err := resolveRequestGatewayGroup(c, keys, key)
	require.NoError(t, err)
	require.Equal(t, service.PlatformGemini, resolved.Group.Platform)
}

func TestRequestGroupResolutionPreservesMultipartUploads(t *testing.T) {
	keys, key := requestRoutingFixture()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "paraformer-v2"))
	part, err := writer.CreateFormFile("file", "sample.wav")
	require.NoError(t, err)
	_, err = part.Write([]byte("audio contents"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	original := append([]byte(nil), body.Bytes()...)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	resolved, err := resolveRequestGatewayGroup(c, keys, key)
	require.NoError(t, err)
	require.Equal(t, service.PlatformDashScope, resolved.Group.Platform)
	restored, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, original, restored)
}

func TestScopedEmptyCatalogDoesNotEnableUngroupedScheduling(t *testing.T) {
	keys := service.NewAPIKeyService(nil, nil, &requestRoutingGroupsStub{}, nil, nil, nil, &config.Config{})
	key := &service.APIKey{ID: 1, UserID: 2, GroupScope: service.APIKeyGroupScopePublic, User: &service.User{ID: 2, Status: service.StatusActive}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resolved, err := resolveRequestGatewayGroup(c, keys, key)
	require.NoError(t, err)
	require.Nil(t, resolved.GroupID)
	groups, scoped := service.GatewayModelGroups(c.Request.Context())
	require.True(t, scoped)
	require.Empty(t, groups)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("{\"model\":\"gpt-6-sol\"}"))
	_, err = resolveRequestGatewayGroup(c, keys, key)
	require.ErrorIs(t, err, service.ErrGatewayRouteUnavailable)
}

func TestRequestGroupResolutionRejectsMalformedBodyBeforeReservation(t *testing.T) {
	keys, key := requestRoutingFixture()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("{"))
	_, err := resolveRequestGatewayGroup(c, keys, key)
	require.ErrorIs(t, err, errGatewayRoutingBody)
	status, code, _ := gatewayGroupRoutingError(err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "INVALID_REQUEST", code)
}
