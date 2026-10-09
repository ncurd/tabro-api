package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func TestRestoreGatewayPricingNumbersPreservesUnknownMetadataAndExactIDs(t *testing.T) {
	original := json.RawMessage(`{"effective_group_id":9007199254740993,"billing_model":"gpt-6.1-sol","rate_multiplier":10,"resolved":{"BasePricing":{"CacheReadPricePerToken":1e-7,"LongContextInputThreshold":9007199254740993,"future":{"decimal":"9007199254740993.125000","id":9007199254740993}}},"future":{"id":9007199254740993,"decimal":"0.000000100000","number":0.0000001234567890123456789}}`)
	stored := bytes.ReplaceAll(original, []byte(`"CacheReadPricePerToken":1e-7`), []byte(`"CacheReadPricePerToken":0.0000001`))
	originalHash, err := bc.Fingerprint(original)
	require.NoError(t, err)
	storedHash, err := bc.Fingerprint(stored)
	require.NoError(t, err)
	require.NotEqual(t, originalHash, storedHash, "JSONB decimal expansion reproduces the old conflict")
	restored, err := restoreGatewayPricingNumberEncoding(stored)
	require.NoError(t, err)
	restoredHash, err := bc.Fingerprint(restored)
	require.NoError(t, err)
	require.Equal(t, originalHash, restoredHash, "match the fingerprint computed by the deployed old code")
	require.Contains(t, string(restored), `"effective_group_id":9007199254740993`)
	require.Contains(t, string(restored), `"LongContextInputThreshold":9007199254740993`)
	require.Contains(t, string(restored), `"decimal":"9007199254740993.125000"`)
	require.Contains(t, string(restored), `"number":0.0000001234567890123456789`)
	require.JSONEq(t, string(original), string(restored))
}

func gatewayPricingReplayFixture(t *testing.T) (*GatewayBillingCoordinator, *gatewayBillingStoreStub, *gatewayBillingAuthorityStub, *GatewayOIDCPrincipal, *APIKey) {
	t.Helper()
	s, repo, authority, principal := gatewayBillingFixture()
	channels := &ChannelService{}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	channels.cache.Store(cache)
	billing := NewBillingService(nil, nil)
	gateway := &GatewayService{billingService: billing, channelService: channels, resolver: NewModelPricingResolver(channels, billing)}
	s.creditPricer = &gatewayCreditPriceCalculator{gateway: gateway}
	group := &Group{ID: 9007199254740993, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: 2}
	key := &APIKey{UserID: repo.route.LocalUserID, GroupID: &group.ID, Group: group}
	return s, repo, authority, principal, key
}

func testGatewayPricingReplay(t *testing.T, roundTrip func(json.RawMessage) json.RawMessage) {
	t.Helper()
	s, repo, authority, principal, key := gatewayPricingReplayFixture(t)
	body := []byte(`{"model":"gpt-6.1-sol","max_output_tokens":20}`)
	ctx := context.WithValue(context.Background(), oidcBillingMultiplierContextKey{}, 10.0)
	first, err := s.PrepareForKey(ctx, *repo.route, principal, "proof", "/v1/responses", "same-request", body, key)
	require.NoError(t, err)
	require.Contains(t, string(first.GatewayPricingSnapshot), `"CacheReadPricePerToken":1e-7`)
	oldFingerprint := repo.op.RequestFingerprint
	storedPricing := roundTrip(repo.op.GatewayPricingSnapshot)
	require.Contains(t, string(storedPricing), `0.0000001`)
	repo.op.GatewayPricingSnapshot = storedPricing
	replay, err := s.PrepareForKey(ctx, *repo.route, principal, "proof", "/v1/responses", "same-request", body, key)
	require.NoError(t, err)
	require.Equal(t, oldFingerprint, repo.op.RequestFingerprint)
	require.Equal(t, storedPricing, repo.op.GatewayPricingSnapshot, "do not rewrite the durable snapshot")
	require.Equal(t, storedPricing, replay.GatewayPricingSnapshot, "interpret the original durable prices")
	require.Equal(t, first.Quote.Request.MaximumUsage, replay.Quote.Request.MaximumUsage)
	require.Equal(t, 1, authority.quoteCalls)
	require.Equal(t, 1, authority.reserves)

	// A real price change must still conflict with the complete frozen identity.
	repo.op.GatewayPricingSnapshot = bytes.Replace(storedPricing, []byte(`0.0000001`), []byte(`0.00000011`), 1)
	_, err = s.PrepareForKey(ctx, *repo.route, principal, "proof", "/v1/responses", "same-request", body, key)
	require.ErrorIs(t, err, bc.ErrConflict)
	require.Equal(t, 1, authority.reserves)
}

func TestGatewayPricingReplayRestoresHistoricalScientificFloatFingerprint(t *testing.T) {
	testGatewayPricingReplay(t, func(raw json.RawMessage) json.RawMessage {
		return json.RawMessage(strings.ReplaceAll(string(raw), "1e-7", "0.0000001"))
	})
}

func TestGatewayPricingReplayStillRejectsChangedBodyGroupAndOwner(t *testing.T) {
	s, repo, authority, principal, key := gatewayPricingReplayFixture(t)
	body := []byte(`{"model":"gpt-6.1-sol","max_output_tokens":20}`)
	_, err := s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "frozen", body, key)
	require.NoError(t, err)
	repo.op.GatewayPricingSnapshot = json.RawMessage(strings.ReplaceAll(string(repo.op.GatewayPricingSnapshot), "1e-7", "0.0000001"))
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "frozen", []byte(`{"model":"gpt-6.1-sol","max_output_tokens":21}`), key)
	require.ErrorIs(t, err, bc.ErrConflict)
	key.Group.ID++
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "frozen", body, key)
	require.ErrorIs(t, err, bc.ErrConflict)
	key.Group.ID--
	route := *repo.route
	route.OwnerEpoch++
	_, err = s.PrepareForKey(context.Background(), route, principal, "proof", "/v1/responses", "frozen", body, key)
	require.ErrorIs(t, err, bc.ErrConflict)
	require.Equal(t, 1, authority.quoteCalls)
	require.Equal(t, 1, authority.reserves)
}

func TestRestoreGatewayPricingNumbersRejectsSubFloatPrecisionPriceChanges(t *testing.T) {
	_, err := restoreGatewayPricingNumberEncoding(json.RawMessage(`{"resolved":{"BasePricing":{"CacheReadPricePerToken":0.000000100000000000000001}}}`))
	require.ErrorIs(t, err, bc.ErrConflict)
}

func TestGatewayPricingReplayDoesNotReserveReleasedOrDispatchedOperationsAgain(t *testing.T) {
	for _, state := range []bc.State{bc.ReleasePending, bc.Released, bc.Dispatching, bc.Dispatched, bc.SettlementPending, bc.Settled} {
		t.Run(string(state), func(t *testing.T) {
			s, repo, authority, principal, key := gatewayPricingReplayFixture(t)
			body := []byte(`{"model":"gpt-6.1-sol","max_output_tokens":20}`)
			_, err := s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "finished", body, key)
			require.NoError(t, err)
			repo.op.GatewayPricingSnapshot = json.RawMessage(strings.ReplaceAll(string(repo.op.GatewayPricingSnapshot), "1e-7", "0.0000001"))
			repo.op.State = state
			_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "finished", body, key)
			require.ErrorIs(t, err, bc.ErrState)
			require.Equal(t, state, repo.op.State)
			require.Equal(t, 1, authority.quoteCalls)
			require.Equal(t, 1, authority.reserves)
			require.Zero(t, authority.dispatches)
		})
	}
}
