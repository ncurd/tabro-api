package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayRoutingGroupsStub struct {
	GroupRepository
	candidates []GatewayRoutingCandidate
	err        error
}

func (r *gatewayRoutingGroupsStub) ListGatewayRoutingCandidates(context.Context, []int64, bool) ([]GatewayRoutingCandidate, error) {
	return append([]GatewayRoutingCandidate(nil), r.candidates...), r.err
}

type gatewayRoutingOwnerStub struct {
	UserRepository
	owner *User
}

func (r *gatewayRoutingOwnerStub) GetByID(context.Context, int64) (*User, error) {
	return r.owner, nil
}

func routingTestCandidate(id int64, platform string, models ...string) GatewayRoutingCandidate {
	g := &Group{ID: id, Platform: platform, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, Hydrated: true, RateMultiplier: float64(id)}
	a := Account{ID: id, Platform: platform, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey}
	if len(models) > 0 {
		mapping := map[string]any{}
		for _, model := range models {
			mapping[model] = model
		}
		a.Credentials = map[string]any{"model_mapping": mapping}
	}
	return GatewayRoutingCandidate{Group: g, Accounts: []Account{a}}
}

func routingTestKey() *APIKey {
	return &APIKey{ID: 10, UserID: 20, GroupScope: APIKeyGroupScopePublic, User: &User{ID: 20, Status: StatusActive}}
}

func TestGatewayPublicKeyRoutesModelsBeforeBillingWithoutMutatingCredential(t *testing.T) {
	repo := &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{
		routingTestCandidate(3, PlatformGemini, "gemini-3-pro"),
		routingTestCandidate(1, PlatformAnthropic, "claude-sonnet-4-6"),
		routingTestCandidate(2, PlatformOpenAI, "gpt-6-sol"),
	}}
	s := &APIKeyService{groupRepo: repo}
	key := routingTestKey()
	for _, test := range []struct {
		model, path, platform string
		id                    int64
	}{
		{"gpt-6-sol", "/v1/responses", PlatformOpenAI, 2},
		{"claude-sonnet-4-6", "/v1/chat/completions", PlatformAnthropic, 1},
		{"gemini-3-pro", "/v1beta/models/gemini-3-pro:generateContent", PlatformGemini, 3},
	} {
		t.Run(test.model, func(t *testing.T) {
			resolved, _, err := s.ResolveGatewayGroup(context.Background(), key, GatewayGroupRequest{Model: test.model, Path: test.path})
			require.NoError(t, err)
			require.Equal(t, test.id, *resolved.GroupID)
			require.Equal(t, test.platform, resolved.Group.Platform)
			require.Equal(t, float64(test.id), resolved.Group.RateMultiplier)
			require.Equal(t, key.ID, resolved.ID)
			require.NotSame(t, key, resolved)
		})
	}
	require.Nil(t, key.GroupID)
	require.Nil(t, key.Group)
}

func TestGatewayUniversalGroupKeepsOneIDWithDifferentRequestPlatforms(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformAll, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, Hydrated: true, RateMultiplier: 1}
	candidate := GatewayRoutingCandidate{Group: group}
	for _, provider := range []struct{ platform, model string }{{PlatformOpenAI, "gpt-6-sol"}, {PlatformAnthropic, "claude-sonnet-4-6"}, {PlatformGemini, "gemini-3-pro"}} {
		candidate.Accounts = append(candidate.Accounts, routingTestCandidate(1, provider.platform, provider.model).Accounts...)
	}
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{candidate}}}
	key := routingTestKey()
	var wg sync.WaitGroup
	for _, test := range []struct{ platform, model string }{{PlatformOpenAI, "gpt-6-sol"}, {PlatformAnthropic, "claude-sonnet-4-6"}, {PlatformGemini, "gemini-3-pro"}} {
		wg.Add(1)
		go func(platform, model string) {
			defer wg.Done()
			resolved, _, err := s.ResolveGatewayGroup(context.Background(), key, GatewayGroupRequest{Path: "/v1/chat/completions", Model: model})
			require.NoError(t, err)
			require.Equal(t, int64(1), *resolved.GroupID)
			require.Equal(t, platform, resolved.Group.Platform)
		}(test.platform, test.model)
	}
	wg.Wait()
	require.Equal(t, PlatformAll, group.Platform)
	require.Nil(t, key.Group)
}

func TestGatewayPublicScopeCannotUsePrivateSubscriptionDisabledOrUnlinkedPools(t *testing.T) {
	for _, mode := range []string{"exclusive", "subscription", "disabled", "no-account", "quota"} {
		t.Run(mode, func(t *testing.T) {
			candidate := routingTestCandidate(1, PlatformOpenAI, "gpt-6-sol")
			switch mode {
			case "exclusive":
				candidate.Group.IsExclusive = true
			case "subscription":
				candidate.Group.SubscriptionType = SubscriptionTypeSubscription
			case "disabled":
				candidate.Group.Status = StatusDisabled
			case "no-account":
				candidate.Accounts = nil
			case "quota":
				candidate.Accounts[0].Schedulable = false
			}
			s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{candidate}}}
			_, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Model: "gpt-6-sol", Path: "/v1/responses"})
			require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
		})
	}
}

func TestGatewaySelectedScopeRechecksLivePermissionsAndNeverUsesOutsideGroup(t *testing.T) {
	private := routingTestCandidate(1, PlatformOpenAI, "gpt-6-sol")
	private.Group.IsExclusive = true
	outside := routingTestCandidate(2, PlatformOpenAI, "gpt-6-sol")
	owner := &User{ID: 20, Status: StatusActive, AllowedGroups: []int64{1}}
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{outside, private}}, userRepo: &gatewayRoutingOwnerStub{owner: owner}}
	key := routingTestKey()
	key.GroupScope, key.GroupIDs = APIKeyGroupScopeSelected, []int64{1}
	resolved, _, err := s.ResolveGatewayGroup(context.Background(), key, GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.NoError(t, err)
	require.Equal(t, int64(1), *resolved.GroupID)
	owner.AllowedGroups = nil
	_, _, err = s.ResolveGatewayGroup(context.Background(), key, GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestGatewayRoutingPreservesLegacyAndReturnsScopedEmptyCatalog(t *testing.T) {
	legacy := routingTestKey()
	legacy.GroupScope = APIKeyGroupScopeSingle
	s := &APIKeyService{}
	result, groups, err := s.ResolveGatewayGroup(context.Background(), legacy, GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.NoError(t, err)
	require.Same(t, legacy, result)
	require.Nil(t, groups)
	s.groupRepo = &gatewayRoutingGroupsStub{}
	result, groups, err = s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/models", Catalog: true})
	require.NoError(t, err)
	require.NotNil(t, groups)
	require.Empty(t, groups)
	require.Nil(t, result.GroupID)
	s.groupRepo = &gatewayRoutingGroupsStub{err: errors.New("database down")}
	_, _, err = s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/models", Catalog: true})
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
}

func TestGatewayRoutingDoesNotCaptureOtherProvidersAndPinsWebSocketToOpenAI(t *testing.T) {
	anthropic := routingTestCandidate(1, PlatformAnthropic)
	openai := routingTestCandidate(2, PlatformOpenAI)
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{anthropic, openai}}}
	result, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.NoError(t, err)
	require.Equal(t, int64(2), *result.GroupID)
	result, _, err = s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses"})
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, result.Group.Platform)
}

func TestGatewayRoutingClearsCrossGroupPostBillingFallback(t *testing.T) {
	candidate := routingTestCandidate(1, PlatformOpenAI, "gpt-6-sol")
	fallback := int64(99)
	candidate.Group.FallbackGroupID = &fallback
	candidate.Group.FallbackGroupIDOnInvalidRequest = &fallback
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{candidate}}}
	result, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.NoError(t, err)
	require.Nil(t, result.Group.FallbackGroupID)
	require.Nil(t, result.Group.FallbackGroupIDOnInvalidRequest)
	require.NotNil(t, candidate.Group.FallbackGroupID)
}

func TestGatewayRoutingPrefersExplicitAliasOverBroadProviderAccount(t *testing.T) {
	group := routingTestCandidate(1, PlatformAll)
	broad := routingTestCandidate(2, PlatformOpenAI).Accounts[0]
	explicit := routingTestCandidate(3, PlatformAnthropic, "company-chat").Accounts[0]
	explicit.Credentials = map[string]any{"model_mapping": map[string]any{"company-chat": "claude-sonnet-4-6"}}
	group.Accounts = []Account{broad, explicit}
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{group}}}
	result, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses", Model: "company-chat"})
	require.NoError(t, err)
	require.Equal(t, PlatformAnthropic, result.Group.Platform)
}

func TestGatewayRoutingDoesNotRouteGPTToAntigravity(t *testing.T) {
	candidate := routingTestCandidate(1, PlatformAntigravity, "gpt-6-sol")
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{candidate}}}
	_, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.ErrorIs(t, err, ErrGatewayRouteUnavailable)
}

func TestGatewayWebSocketRoutingSkipsHTTPOnlyGroup(t *testing.T) {
	httpOnly := routingTestCandidate(1, PlatformOpenAI, "gpt-6-sol")
	websocket := routingTestCandidate(2, PlatformOpenAI, "gpt-6-sol")
	websocket.Accounts[0].Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true, true, true
	s := &APIKeyService{cfg: cfg, groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{httpOnly, websocket}}}
	key, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{
		Path: "/v1/responses", Model: "gpt-6-sol", ForcePlatform: PlatformOpenAI, RequiredTransport: OpenAIUpstreamTransportResponsesWebsocketV2,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), *key.GroupID)
}
