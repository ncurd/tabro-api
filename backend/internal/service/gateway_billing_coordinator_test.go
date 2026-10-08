package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

type gatewayBillingStoreStub struct {
	GatewayBillingRepository
	route     *GatewayBillingRoute
	workspace *GatewayBillingRoute
	op        bc.Operation
	mu        sync.Mutex
	shadow    int
}

func (s *gatewayBillingStoreStub) GetBillingRoute(context.Context, int64) (*GatewayBillingRoute, error) {
	return s.route, nil
}
func (s *gatewayBillingStoreStub) GetBillingWorkspaceRoute(context.Context, string, string) (*GatewayBillingRoute, error) {
	if s.workspace == nil {
		return nil, nil
	}
	clone := *s.workspace
	return &clone, nil
}
func (s *gatewayBillingStoreStub) Get(context.Context, bc.Key) (bc.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.OperationID == "" {
		return bc.Operation{}, bc.ErrNotFound
	}
	return s.op, nil
}
func (s *gatewayBillingStoreStub) CreateIntent(_ context.Context, o bc.Operation) (bc.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.OperationID != "" {
		if s.op.RequestFingerprint != o.RequestFingerprint {
			return s.op, false, bc.ErrConflict
		}
		return s.op, false, nil
	}
	o.State = bc.Intent
	o.Version = 1
	s.op = o
	return o, true, nil
}
func (s *gatewayBillingStoreStub) BindReservation(_ context.Context, _ bc.Key, _ int64, r bc.Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.op.ReservationID = r.ReservationID
	s.op.RemoteVersion = r.Version
	s.op.State = bc.Reserved
	s.op.Version++
	return nil
}
func (s *gatewayBillingStoreStub) ClaimDispatch(_ context.Context, _ bc.Key, v int64, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != v || s.op.State != bc.Reserved {
		return false, nil
	}
	s.op.State = bc.Dispatching
	s.op.AttemptID = id
	s.op.Version++
	return true, nil
}
func (s *gatewayBillingStoreStub) ConfirmDispatched(_ context.Context, _ bc.Key, v int64, id string, r bc.Reservation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != v || s.op.AttemptID != id {
		return false, nil
	}
	s.op.State = bc.Dispatched
	s.op.RemoteVersion = r.Version
	s.op.Version++
	return true, nil
}
func (s *gatewayBillingStoreStub) RecordBillingShadow(_ context.Context, _ GatewayBillingRoute, _ *bc.Execution) error {
	s.shadow++
	return nil
}

type gatewayBillingAuthorityStub struct {
	GatewayBillingAuthority
	quote                            bc.Quote
	quoteCalls, reserves, dispatches int
	quoteMode                        string
	quoted                           bc.QuoteRequest
}

func (s *gatewayBillingAuthorityStub) Quote(_ context.Context, q bc.ProofRequest[bc.QuoteRequest]) (bc.Quote, error) {
	s.quoteCalls++
	s.quoteMode = q.Request.QuoteMode
	s.quoted = q.Request
	result := s.quote
	result.Request.OperationID = q.Request.OperationID
	result.Request.MaximumUsage = q.Request.MaximumUsage
	result.Request.RequestPayloadHash = q.Request.RequestPayloadHash
	return result, nil
}

type testGatewayCreditPricer struct {
	maximum bc.Decimal
	calls   int
	err     error
}

func (p *testGatewayCreditPricer) MaximumCredit(_ context.Context, _ *APIKey, _ string, _ bc.QuoteRequest) (bc.Decimal, json.RawMessage, error) {
	p.calls++
	return p.maximum, json.RawMessage(`{"billing_model":"gpt-6-sol","rate_multiplier":1,"resolved":{"Mode":"token","BasePricing":{}}}`), p.err
}

func TestGatewayPricedCreditQuoteAndSettlementUseOneTechnicalMeter(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	pricer := &testGatewayCreditPricer{maximum: "12.5"}
	s.creditPricer = pricer
	body := []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`)
	e, err := s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "priced-op", body, nil)
	require.NoError(t, err)
	require.Equal(t, gatewayCreditProductKey, authority.quoted.ProductKey)
	require.Equal(t, "default", authority.quoted.ServiceTier)
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: "12.5"}, authority.quoted.MaximumUsage)
	require.Equal(t, "ai:gpt-6-sol", e.ProductKey)
	require.Equal(t, 1, pricer.calls)
	_, err = s.PrepareForKey(context.Background(), *repo.route, principal, "new-proof", "/v1/responses", "priced-op", body, nil)
	require.NoError(t, err)
	require.Equal(t, 1, pricer.calls, "retries must retain the durable price and multiplier from the original reservation")
	ctx := bc.WithExecution(context.Background(), e)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	cmd := &UsageBillingCommand{RequestID: "priced-op", InputTokens: 10, OutputTokens: 20, ActualCost: 2.25}
	require.NoError(t, attachCentralUsage(ctx, cmd))
	var settlement bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &settlement))
	require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: "2.25"}, settlement.Usage)
	tooMuch := &UsageBillingCommand{RequestID: "priced-op-too-much", InputTokens: 10, ActualCost: 12.5000000001}
	require.ErrorIs(t, attachCentralUsage(ctx, tooMuch), bc.ErrState)
	require.Nil(t, tooMuch.CentralEvent)
}
func (s *gatewayBillingAuthorityStub) Reserve(_ context.Context, q bc.ProofRequest[bc.ReserveRequest]) (bc.Reservation, error) {
	s.reserves++
	return bc.Reservation{OperationID: q.Request.OperationID, ReservationID: "reservation", State: "reserved", Version: 1, OwnerEpoch: q.Request.OwnerEpoch}, nil
}
func (s *gatewayBillingAuthorityStub) GetOperation(_ context.Context, k bc.Key) (bc.Reservation, error) {
	return bc.Reservation{OperationID: k.OperationID, ReservationID: "reservation", State: "reserved", Version: 1, OwnerEpoch: 4}, nil
}
func (s *gatewayBillingAuthorityStub) Dispatch(_ context.Context, k bc.Key, _ string, _ bc.TransitionRequest) (bc.Reservation, error) {
	s.dispatches++
	return bc.Reservation{OperationID: k.OperationID, ReservationID: "reservation", State: "dispatched", Version: 2, OwnerEpoch: 4}, nil
}

func gatewayBillingFixture() (*GatewayBillingCoordinator, *gatewayBillingStoreStub, *gatewayBillingAuthorityStub, *GatewayOIDCPrincipal) {
	route := &GatewayBillingRoute{LocalUserID: 1, Mode: "central", Issuer: "https://auth.example", ActorUserID: "user", TenantID: "tenant", OriginAppID: "run", BillingAccountID: "payer", OwnerEpoch: 4}
	repo := &gatewayBillingStoreStub{route: route}
	authority := &gatewayBillingAuthorityStub{quote: bc.Quote{QuoteID: "quote", Request: bc.ReserveRequest{BillingAccountID: "payer", BalanceID: "balance", PriceVersionID: "price", OwnerEpoch: 4, ServiceTier: "default"}, EstimatedAmount: "1.5", WalletUnit: "credit"}}
	principal := &GatewayOIDCPrincipal{Issuer: route.Issuer, Subject: route.ActorUserID, Tenant: route.TenantID, ClientID: route.OriginAppID}
	return &GatewayBillingCoordinator{repo: repo, authority: authority, producer: "gateway", enabled: true}, repo, authority, principal
}

func TestGatewayBillingCentralPrepareDispatchAndUsage(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	ctx := context.Background()
	e, err := s.Prepare(ctx, *repo.route, principal, "sensitive-proof", "/v1/messages", "operation", []byte(`{"model":"claude-opus-5-5","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.Equal(t, 1, authority.reserves)
	require.NotContains(t, string(repo.op.RequestPayload), "sensitive-proof")
	ctx = bc.WithExecution(ctx, e)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	require.ErrorIs(t, bc.BeforeSupplierRequest(ctx), bc.ErrState)
	require.Equal(t, 1, authority.dispatches)
	cmd := &UsageBillingCommand{RequestID: "operation", InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 9, CacheCreation5mTokens: 3, CacheCreation1hTokens: 4, BalanceCost: 5, SubscriptionCost: 3, APIKeyQuotaCost: 2, APIKeyRateLimitCost: 2, AccountQuotaCost: 0.9}
	require.NoError(t, attachCentralUsage(ctx, cmd))
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	require.Zero(t, cmd.APIKeyRateLimitCost)
	require.Equal(t, 0.9, cmd.AccountQuotaCost)
	var request bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &request))
	require.Equal(t, bc.Decimal("0"), request.Usage["cache_write_tokens"])
	require.Equal(t, bc.Decimal("5"), request.Usage["cache_write_5m_tokens"])
	require.Equal(t, bc.Decimal("4"), request.Usage["cache_write_1h_tokens"])
	require.True(t, request.UsageComplete)
	captured := BindUsageRecordTaskContext(ctx, func(worker context.Context) {
		require.True(t, bc.IsCentral(worker))
		require.NotNil(t, bc.ExecutionFromContext(worker))
	})
	captured(context.Background())
}
func TestGatewayBillingDoesNotFallbackWhenDisabledOrIdentityMismatches(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	s.enabled = false
	_, err := s.Route(context.Background(), 1)
	require.ErrorIs(t, err, bc.ErrState)
	s.enabled = true
	principal.Tenant = "attacker-tenant"
	_, err = s.Prepare(context.Background(), *repo.route, principal, "proof", "/v1/messages", "op", []byte(`{"model":"m","max_tokens":5}`))
	require.ErrorIs(t, err, bc.ErrConflict)
	require.Zero(t, authority.quoteCalls)
	repo.route.Mode = "fenced"
	_, err = s.Route(context.Background(), 1)
	require.ErrorIs(t, err, bc.ErrState)
}
func TestGatewayBillingRetryUsesFrozenRequestAfterPriceChanges(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	body := []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`)
	first, err := s.Prepare(context.Background(), *repo.route, principal, "proof", "/v1/responses", "op", body)
	require.NoError(t, err)
	authority.quote.Request.PriceVersionID = "new-price"
	again, err := s.Prepare(context.Background(), *repo.route, principal, "fresh-proof", "/v1/responses", "op", body)
	require.NoError(t, err)
	require.Equal(t, first.Quote.Request.PriceVersionID, again.Quote.Request.PriceVersionID)
	require.Equal(t, 1, authority.quoteCalls)
	require.Equal(t, 1, authority.reserves)
	_, err = s.Prepare(context.Background(), *repo.route, principal, "proof", "/v1/responses", "op", []byte(`{"model":"gpt-6-luna","max_output_tokens":50}`))
	require.ErrorIs(t, err, bc.ErrConflict)
}
func TestGatewayBillingShadowNeverReserves(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	repo.route.Mode = "shadow"
	e, err := s.Prepare(context.Background(), *repo.route, principal, "proof", "/v1/responses", "op", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.NoError(t, err)
	require.Equal(t, "shadow", authority.quoteMode)
	require.Equal(t, 1, repo.shadow)
	require.Zero(t, authority.reserves)
	require.NoError(t, bc.BeforeSupplierRequest(bc.WithExecution(context.Background(), e)))
	require.Zero(t, authority.dispatches)
}
func TestGatewayBillingRejectsUnboundedOrUnsupportedRequests(t *testing.T) {
	for _, body := range []string{`{"model":"m"}`, `{"model":"m","max_tokens":0}`, `{"model":"m","max_tokens":30,"background":true}`, `{"model":"m","max_tokens":30,"previous_response_id":"hidden-input"}`, `{"model":"m","max_tokens":30,"n":2}`, `{"model":"m","max_tokens":30,"input":[{"type":"input_image","image_url":"https://example.test/image"}]}`} {
		_, err := textBillingQuoteRequest("/v1/responses", "op", []byte(body))
		require.Error(t, err, body)
	}
	q, err := textBillingQuoteRequest("/v1/messages", "op", []byte(`{"model":"claude-opus-5-5","max_tokens":10,"speed":"fast"}`))
	require.NoError(t, err)
	require.Equal(t, "anthropic_fast", q.ServiceTier)
	require.Len(t, q.MaximumUsage, 9)
}

func TestGatewayBillingWorkspacePayerAndMultipleVerifiedApps(t *testing.T) {
	s, repo, authority, p := gatewayBillingFixture()
	repo.workspace = &GatewayBillingRoute{Mode: "central", Issuer: p.Issuer, TenantID: "team-a", BillingAccountID: "team-wallet", OwnerEpoch: 9}
	p.Tenant = "team-a"
	p.ClientID = "other-verified-app"
	route, err := s.RouteForPrincipal(context.Background(), 1, p)
	require.NoError(t, err)
	require.Equal(t, "team-wallet", route.BillingAccountID)
	require.Equal(t, "user", route.ActorUserID)
	require.Equal(t, "other-verified-app", route.OriginAppID)
	authority.quote.Request.BillingAccountID = "team-wallet"
	authority.quote.Request.OwnerEpoch = 9
	e, err := s.Prepare(context.Background(), *route, p, "proof", "/v1/responses", "team-op", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.NoError(t, err)
	require.Equal(t, "other-verified-app", e.Key.OriginAppID)
	require.Equal(t, "team-wallet", e.Quote.Request.BillingAccountID)
	repo.workspace.Mode = "frozen"
	_, err = s.RouteForPrincipal(context.Background(), 1, p)
	require.ErrorIs(t, err, bc.ErrState)
}
