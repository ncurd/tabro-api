package service

import (
	"context"
	"encoding/json"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

type sessionGroupCreditPricer struct{ key *APIKey }

func (p *sessionGroupCreditPricer) MaximumCredit(_ context.Context, key *APIKey, _ string, _ bc.QuoteRequest) (bc.Decimal, json.RawMessage, error) {
	p.key = key
	raw, err := json.Marshal(gatewayCreditPriceSnapshot{EffectiveGroupID: key.Group.ID, EffectivePlatform: key.Group.Platform, BillingModel: "gpt-6-sol", RateMultiplier: key.Group.RateMultiplier})
	return "12.5", raw, err
}

func TestGatewayBillingSessionRebindsSelectedGroupWithPinnedConnector(t *testing.T) {
	coordinator, repo, authority, principal := gatewayBillingFixture()
	pricer := &sessionGroupCreditPricer{}
	coordinator.creditPricer = pricer
	route := *repo.route
	route.runtime = coordinator
	initial := &APIKey{ID: 3, UserID: 1, Group: &Group{ID: 11, Platform: PlatformOpenAI, RateMultiplier: 1}}
	selected := *initial
	selected.Group = &Group{ID: 12, Platform: PlatformOpenAI, RateMultiplier: 2}
	selected.GroupID = &selected.Group.ID
	// An outer connector must not replace the runtime/proof captured at upgrade.
	session := (&GatewayBillingCoordinator{}).Session(route, principal, "proof", nil, initial)
	bound := session.WithKey(&selected)
	execution, _, err := bound.Prepare(context.Background(), "ws-selected-group", []byte(`{"type":"response.create","model":"gpt-6-sol","max_output_tokens":50}`))
	require.NoError(t, err)
	require.Same(t, &selected, pricer.key)
	require.Equal(t, 1, authority.reserves)
	require.NoError(t, validateGatewayPricingIdentity(execution.GatewayPricingSnapshot, &selected))
	require.ErrorIs(t, validateGatewayPricingIdentity(execution.GatewayPricingSnapshot, initial), bc.ErrConflict)
	require.Equal(t, int64(11), initial.Group.ID)
}
