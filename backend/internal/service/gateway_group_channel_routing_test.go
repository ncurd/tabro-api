//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func routingChannelService(channels []Channel, platforms map[int64]string) *ChannelService {
	return newTestChannelService(&mockChannelRepository{
		listAllFn:           func(context.Context) ([]Channel, error) { return channels, nil },
		getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) { return platforms, nil },
	})
}

func TestGatewayMultiGroupRoutingSkipsForbiddenChannelBeforeBilling(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			model := "gpt-6-sol"
			if source != BillingModelSourceRequested {
				model = "named"
			}
			first, second := routingTestCandidate(1, PlatformOpenAI, model), routingTestCandidate(2, PlatformOpenAI, model)
			if source != BillingModelSourceRequested {
				for _, candidate := range []*GatewayRoutingCandidate{&first, &second} {
					candidate.Accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"named": "gpt-6-sol", "gpt-6-sol": "gpt-6-sol"}}
				}
			}
			firstChannel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{1}, RestrictModels: true, BillingModelSource: source,
				ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"other"}}}}
			secondChannel := Channel{ID: 2, Status: StatusActive, GroupIDs: []int64{2}, RestrictModels: true, BillingModelSource: source,
				ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-6-sol"}}}}
			if source == BillingModelSourceChannelMapped {
				firstChannel.ModelMapping = map[string]map[string]string{PlatformOpenAI: {"named": "gpt-6-sol"}}
				secondChannel.ModelMapping = map[string]map[string]string{PlatformOpenAI: {"named": "gpt-6-sol"}}
			}
			channels := routingChannelService([]Channel{firstChannel, secondChannel}, map[int64]string{1: PlatformOpenAI, 2: PlatformOpenAI})
			pricer, _ := noChannelCreditPricer()
			pricer.gateway.channelService = channels
			s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{first, second}},
				GatewayBilling: &GatewayBillingCoordinator{creditPricer: pricer}}
			key := routingTestKey()
			resolved, _, err := s.ResolveGatewayGroup(context.Background(), key, GatewayGroupRequest{Path: "/v1/responses", Model: model})
			require.NoError(t, err)
			require.Equal(t, int64(2), *resolved.GroupID)
			require.Equal(t, PlatformOpenAI, resolved.Group.Platform)
			require.Nil(t, key.GroupID, "routing must not change the cached key")
		})
	}
}

func TestGatewayUniversalRoutingSkipsForbiddenProviderAndFreezesCorrectPrice(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformAll, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, Hydrated: true, RateMultiplier: 2}
	candidate := GatewayRoutingCandidate{Group: group, Accounts: []Account{
		{Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-sol": "gpt-6-sol"}}},
		{Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-sol": "claude-opus-5"}}},
	}}
	input, output := 2e-6, 3e-6
	channels := routingChannelService([]Channel{{ID: 1, Status: StatusActive, GroupIDs: []int64{1}, RestrictModels: true, BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"other"}},
			{Platform: PlatformAnthropic, Models: []string{"gpt-6-sol"}, InputPrice: &input, OutputPrice: &output},
		}}}, map[int64]string{1: PlatformAll})
	pricer, _ := noChannelCreditPricer()
	pricer.gateway.channelService = channels
	pricer.gateway.resolver = NewModelPricingResolver(channels, pricer.gateway.billingService)
	s := &APIKeyService{groupRepo: &gatewayRoutingGroupsStub{candidates: []GatewayRoutingCandidate{candidate}},
		GatewayBilling: &GatewayBillingCoordinator{creditPricer: pricer}}
	resolved, _, err := s.ResolveGatewayGroup(context.Background(), routingTestKey(), GatewayGroupRequest{Path: "/v1/responses", Model: "gpt-6-sol"})
	require.NoError(t, err)
	require.Equal(t, group.ID, *resolved.GroupID)
	require.Equal(t, PlatformAnthropic, resolved.Group.Platform)
	require.Equal(t, PlatformAll, group.Platform)
	quote, err := textBillingQuoteRequest("/v1/responses", "price", []byte(`{"model":"gpt-6-sol","max_output_tokens":20}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), oidcBillingMultiplierContextKey{}, 10.0)
	_, raw, err := pricer.MaximumCredit(ctx, resolved, "/v1/responses", quote)
	require.NoError(t, err)
	var snapshot gatewayCreditPriceSnapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, group.ID, snapshot.EffectiveGroupID)
	require.Equal(t, PlatformAnthropic, snapshot.EffectivePlatform)
	require.Equal(t, 20.0, snapshot.RateMultiplier)
	require.Equal(t, input, snapshot.Resolved.BasePricing.InputPricePerToken)
	require.Equal(t, output, snapshot.Resolved.BasePricing.OutputPricePerToken)
}
