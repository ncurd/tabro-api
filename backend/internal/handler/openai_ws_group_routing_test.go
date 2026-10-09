package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type wsRoutingGroupsStub struct {
	service.GroupRepository
	candidates []service.GatewayRoutingCandidate
}

func (r *wsRoutingGroupsStub) ListGatewayRoutingCandidates(context.Context, []int64, bool) ([]service.GatewayRoutingCandidate, error) {
	return r.candidates, nil
}

type wsRoutingOwnerStub struct{ service.UserRepository }

func (*wsRoutingOwnerStub) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 1, Status: service.StatusActive}, nil
}

func TestOpenAIWebSocketSelectsFirstFrameModelGroupBeforeAcquiringOrBilling(t *testing.T) {
	for _, scope := range []string{service.APIKeyGroupScopePublic, service.APIKeyGroupScopeSelected, service.APIKeyGroupScopeSingle} {
		t.Run(scope, func(t *testing.T) {
			groups := &wsRoutingGroupsStub{}
			for _, candidate := range []struct {
				id    int64
				model string
			}{{11, "gpt-6-luna"}, {12, "gpt-6-sol"}} {
				group := &service.Group{ID: candidate.id, Platform: service.PlatformOpenAI, Status: service.StatusActive,
					SubscriptionType: service.SubscriptionTypeStandard, Hydrated: true, RateMultiplier: float64(candidate.id)}
				account := service.Account{ID: candidate.id, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, Type: service.AccountTypeAPIKey,
					Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true},
					Credentials: map[string]any{"model_mapping": map[string]any{candidate.model: candidate.model}}}
				groups.candidates = append(groups.candidates, service.GatewayRoutingCandidate{Group: group, Accounts: []service.Account{account}})
			}
			original := &service.APIKey{ID: 101, UserID: 1, GroupScope: scope, GroupIDs: []int64{11, 12}, User: &service.User{ID: 1, Status: service.StatusActive}}
			provisional := *original
			provisional.Group, provisional.GroupID = groups.candidates[0].Group, &groups.candidates[0].Group.ID
			if scope == service.APIKeyGroupScopeSingle {
				groups.candidates = groups.candidates[1:]
				groups.candidates[0].Group.Platform = service.PlatformAll
				original.Group, original.GroupID = groups.candidates[0].Group, &groups.candidates[0].Group.ID
				provisional.GroupID = original.GroupID
				copy := *original.Group
				copy.Platform = service.PlatformOpenAI
				provisional.Group = &copy
			}
			selectedGroups := make(chan *service.Group, 1)
			cache := &concurrencyCacheMock{acquireUserSlotFn: func(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
				group, _ := ctx.Value(ctxkey.Group).(*service.Group)
				selectedGroups <- group
				return false, errors.New("stop before any account or billing operation")
			}}
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			h.apiKeyService = service.NewAPIKeyService(nil, &wsRoutingOwnerStub{}, groups, nil, nil, nil, cfg)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &provisional)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
				c.Request = c.Request.WithContext(service.WithGatewayRoutingKey(c.Request.Context(), original))
				c.Next()
			})
			router.GET("/openai/v1/responses", h.ResponsesWebSocket)
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer conn.CloseNow()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol"}`)))
			_, _, err = conn.Read(ctx)
			require.Error(t, err)
			var closeErr coderws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusInternalError, closeErr.Code)
			select {
			case group := <-selectedGroups:
				require.Equal(t, int64(12), group.ID)
				require.Equal(t, service.PlatformOpenAI, group.Platform)
				require.Equal(t, 12.0, group.RateMultiplier)
			case <-ctx.Done():
				t.Fatal("first-frame group was not selected")
			}
			if scope != service.APIKeyGroupScopeSingle {
				require.Nil(t, original.GroupID)
			} else {
				require.Equal(t, service.PlatformAll, original.Group.Platform)
			}
		})
	}
}
