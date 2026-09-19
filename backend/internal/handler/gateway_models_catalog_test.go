package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayModelsCatalogRepoStub struct {
	service.AccountRepository
	accounts  []service.Account
	listCalls int
}

func (s *gatewayModelsCatalogRepoStub) ListSchedulable(context.Context) ([]service.Account, error) {
	s.listCalls++
	return s.accounts, nil
}

func (s *gatewayModelsCatalogRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]service.Account, error) {
	s.listCalls++
	return s.accounts, nil
}

func gatewayModelsCatalogRouter(repo *gatewayModelsCatalogRepoStub, platform string) *gin.Engine {
	gw := service.NewGatewayService(
		repo, // accountRepo
		nil,  // groupRepo
		nil,  // usageLogRepo
		nil,  // usageBillingRepo
		nil,  // userRepo
		nil,  // userSubRepo
		nil,  // userGroupRateRepo
		nil,  // cache
		nil,  // cfg
		nil,  // schedulerSnapshot
		nil,  // concurrencyService
		nil,  // billingService
		nil,  // rateLimitService
		nil,  // billingCacheService
		nil,  // identityService
		nil,  // httpUpstream
		nil,  // deferredService
		nil,  // claudeTokenProvider
		nil,  // sessionLimitCache
		nil,  // rpmCache
		nil,  // digestStore
		nil,  // settingService
		nil,  // tlsFPProfileService
		nil,  // channelService
		nil,  // resolver
		nil,  // balanceNotifyService
	)
	h := &GatewayHandler{gatewayService: gw}
	router := gin.New()
	router.GET("/v1/models", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID: 1, Group: &service.Group{ID: 10, Platform: platform},
		})
		h.Models(c)
	})
	return router
}

func TestGatewayModelsCatalog_OAuthPreservesOpenAIMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}}}
	router := gatewayModelsCatalogRouter(repo, service.PlatformOpenAI)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Object string         `json:"object"`
		Data   []openai.Model `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "list", response.Object)
	byID := make(map[string]openai.Model, len(response.Data))
	for _, model := range response.Data {
		byID[model.ID] = model
		require.Equal(t, "model", model.Object)
		require.Equal(t, "openai", model.OwnedBy)
		require.Positive(t, model.Created)
	}
	require.NotContains(t, byID, "gpt-5.4")
	require.NotContains(t, byID, "gpt-5.4-mini")
	for _, model := range openai.DefaultModels {
		if model.ID == "gpt-5.6-luna" || model.ID == "gpt-image-2.5-sunburst" {
			require.Equal(t, model, byID[model.ID])
		}
	}
}

func TestGatewayModelsCatalog_RetiredWhitelistRemainsEmptyAfterCacheHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"legacy": "gpt-5.4-mini"}},
	}}}
	router := gatewayModelsCatalogRouter(repo, service.PlatformOpenAI)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.JSONEq(t, `{"object":"list","data":[]}`, w.Body.String())
	}
	require.Equal(t, 1, repo.listCalls)
}

func TestGatewayModelsCatalog_ClaudeDefaultsKeepDisplayMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayModelsCatalogRepoStub{accounts: []service.Account{{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey}}}
	router := gatewayModelsCatalogRouter(repo, service.PlatformAnthropic)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data []claude.Model `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.ElementsMatch(t, claude.DefaultModels, response.Data)
}
