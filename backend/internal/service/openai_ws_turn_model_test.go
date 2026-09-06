package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveAuthorizedWebSocketTurnModelResolvesEachTurnMapping(t *testing.T) {
	t.Parallel()

	const groupID int64 = 71
	channelService := newOpenAIWSTurnChannelService(groupID, Channel{
		ID:                 91,
		Status:             StatusActive,
		RestrictModels:     false,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelMapping: map[string]map[string]string{
			PlatformOpenAI: {
				"client-model-a": "channel-model-a",
				"client-model-b": "channel-model-b",
			},
		},
	})
	svc := &OpenAIGatewayService{channelService: channelService}
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
	}

	mappingA, upstreamA, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), openAIWSTurnInt64Ptr(groupID), account, "client-model-a")
	require.NoError(t, err)
	require.True(t, mappingA.Mapped)
	require.Equal(t, "channel-model-a", upstreamA)

	mappingB, upstreamB, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), openAIWSTurnInt64Ptr(groupID), account, "client-model-b")
	require.NoError(t, err)
	require.True(t, mappingB.Mapped)
	require.Equal(t, "channel-model-b", upstreamB)
	require.NotEqual(t, upstreamA, upstreamB, "later turns must not reuse the first turn's resolved model")
}

func TestResolveAuthorizedWebSocketTurnModelAccountMappingTakesPrecedence(t *testing.T) {
	t.Parallel()

	const groupID int64 = 72
	svc := &OpenAIGatewayService{channelService: newOpenAIWSTurnChannelService(groupID, Channel{
		ID:                 92,
		Status:             StatusActive,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelMapping: map[string]map[string]string{
			PlatformOpenAI: {"client-model": "channel-model"},
		},
	})}
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"client-model": "account-model"},
		},
	}

	mapping, upstream, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), openAIWSTurnInt64Ptr(groupID), account, "client-model")
	require.NoError(t, err)
	require.Equal(t, "channel-model", mapping.MappedModel)
	require.Equal(t, "account-model", upstream)
}

func TestResolveAuthorizedWebSocketTurnModelRejectsUnsupportedOrRestrictedModel(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"allowed-client-model": "allowed-upstream"},
		},
	}
	svc := &OpenAIGatewayService{}
	_, _, err := svc.ResolveAuthorizedWebSocketTurnModel(context.Background(), nil, account, "other-client-model")
	require.ErrorContains(t, err, "does not support")

	const groupID int64 = 73
	restrictedSvc := &OpenAIGatewayService{channelService: newOpenAIWSTurnChannelService(groupID, Channel{
		ID:                 93,
		Status:             StatusActive,
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"allowed-upstream"}},
		},
		ModelMapping: map[string]map[string]string{
			PlatformOpenAI: {"client-model": "blocked-upstream"},
		},
	})}
	unrestrictedAccount := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
	}
	_, _, err = restrictedSvc.ResolveAuthorizedWebSocketTurnModel(context.Background(), openAIWSTurnInt64Ptr(groupID), unrestrictedAccount, "client-model")
	require.ErrorContains(t, err, "upstream model")
	require.ErrorContains(t, err, "restricted")
}

func newOpenAIWSTurnChannelService(groupID int64, channel Channel) *ChannelService {
	channel.GroupIDs = []int64{groupID}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	cache.channelByGroupID[groupID] = &channel
	cache.groupPlatform[groupID] = PlatformOpenAI
	cache.byID[channel.ID] = &channel
	expandPricingToCache(cache, &channel, groupID, PlatformOpenAI)
	expandMappingToCache(cache, &channel, groupID, PlatformOpenAI)
	service := &ChannelService{}
	service.cache.Store(cache)
	return service
}

func openAIWSTurnInt64Ptr(value int64) *int64 {
	return &value
}
