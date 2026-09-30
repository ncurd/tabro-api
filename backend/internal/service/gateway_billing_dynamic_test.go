package service

import (
	"context"
	"errors"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

type gatewayBillingProofCapture struct {
	*gatewayBillingAuthorityStub
	quoteProof   string
	reserveProof string
	quoteErr     error
}

func (s *gatewayBillingProofCapture) Quote(ctx context.Context, request bc.ProofRequest[bc.QuoteRequest]) (bc.Quote, error) {
	s.quoteProof = request.SubjectProof
	if s.quoteErr != nil {
		return bc.Quote{}, s.quoteErr
	}
	return s.gatewayBillingAuthorityStub.Quote(ctx, request)
}

func (s *gatewayBillingProofCapture) Reserve(ctx context.Context, request bc.ProofRequest[bc.ReserveRequest]) (bc.Reservation, error) {
	s.reserveProof = request.SubjectProof
	return s.gatewayBillingAuthorityStub.Reserve(ctx, request)
}

func TestGatewayBillingDynamicAuthRouteUsesAuthorityPayer(t *testing.T) {
	coordinator, repo, baseAuthority, principal := gatewayBillingFixture()
	repo.route = nil
	principal.AuthBilled = true
	baseAuthority.quote.Request.BillingAccountID = "auth-selected-payer"
	baseAuthority.quote.Request.OwnerEpoch = 17
	authority := &gatewayBillingProofCapture{gatewayBillingAuthorityStub: baseAuthority}
	coordinator.authority = authority

	route, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
	require.NoError(t, err)
	require.Equal(t, "central", route.Mode)
	require.True(t, route.DynamicAuthority)
	require.Equal(t, principal.Issuer, route.Issuer)
	require.Equal(t, principal.Subject, route.ActorUserID)
	require.Equal(t, principal.Tenant, route.TenantID)
	require.Equal(t, principal.ClientID, route.OriginAppID)
	require.Empty(t, route.BillingAccountID, "payer must come from Auth Quote, not a token claim")

	execution, err := coordinator.Prepare(context.Background(), *route, principal, "verified-subject-proof", "/v1/responses", "dynamic-operation", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.NoError(t, err)
	require.Equal(t, "auth-selected-payer", execution.Quote.Request.BillingAccountID)
	require.EqualValues(t, 17, execution.Quote.Request.OwnerEpoch)
	require.Equal(t, "verified-subject-proof", authority.quoteProof)
	require.Equal(t, "verified-subject-proof", authority.reserveProof)
	require.Equal(t, 1, authority.reserves)
}

func TestGatewayBillingDynamicAuthRouteFailsClosedWhenAuthorityUnavailable(t *testing.T) {
	coordinator, repo, baseAuthority, principal := gatewayBillingFixture()
	repo.route = nil
	principal.AuthBilled = true
	authority := &gatewayBillingProofCapture{gatewayBillingAuthorityStub: baseAuthority, quoteErr: errors.New("auth unavailable")}
	coordinator.authority = authority

	route, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
	require.NoError(t, err)
	require.Equal(t, "central", route.Mode)
	execution, err := coordinator.Prepare(context.Background(), *route, principal, "verified-subject-proof", "/v1/responses", "dynamic-operation", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.Error(t, err)
	require.Nil(t, execution)
	require.Zero(t, authority.reserves)

	coordinator.enabled = false
	_, err = coordinator.RouteForPrincipal(context.Background(), 1, principal)
	require.ErrorIs(t, err, bc.ErrState, "disabling Auth billing cannot turn this identity into a local-billed user")
}

func TestGatewayBillingAuthRouteRejectsExplicitLocalAndShadowModes(t *testing.T) {
	for _, mode := range []string{"local", "shadow", "fenced", "draining", "frozen"} {
		t.Run(mode, func(t *testing.T) {
			coordinator, repo, _, principal := gatewayBillingFixture()
			principal.AuthBilled = true
			repo.route.Mode = mode
			_, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
			require.ErrorIs(t, err, bc.ErrState)
		})
	}
}

func TestGatewayBillingAuthRouteRejectsMismatchedExplicitCentralIdentity(t *testing.T) {
	tests := map[string]func(*GatewayBillingRoute){
		"issuer": func(route *GatewayBillingRoute) { route.Issuer = "https://other-auth.example" },
		"actor":  func(route *GatewayBillingRoute) { route.ActorUserID = "other-user" },
		"tenant": func(route *GatewayBillingRoute) { route.TenantID = "other-tenant" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			coordinator, repo, _, principal := gatewayBillingFixture()
			principal.AuthBilled = true
			mutate(repo.route)
			_, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
			require.ErrorIs(t, err, bc.ErrConflict)
		})
	}
}

func TestGatewayBillingAuthRouteUsesRegisteredWorkspaceWhilePersonalFundsStayLocal(t *testing.T) {
	coordinator, repo, authority, principal := gatewayBillingFixture()
	principal.AuthBilled = true
	repo.route.Mode = "local"
	repo.workspace = &GatewayBillingRoute{
		Mode: "central", Issuer: principal.Issuer, TenantID: principal.Tenant,
		BillingAccountID: "team-wallet", OwnerEpoch: 12,
	}
	authority.quote.Request.BillingAccountID = "team-wallet"
	authority.quote.Request.OwnerEpoch = 12

	route, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
	require.NoError(t, err)
	require.Equal(t, "central", route.Mode)
	require.False(t, route.DynamicAuthority)
	require.Equal(t, "team-wallet", route.BillingAccountID)
	execution, err := coordinator.Prepare(context.Background(), *route, principal, "verified-subject-proof", "/v1/responses", "workspace-operation", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.NoError(t, err)
	require.Equal(t, "team-wallet", execution.Quote.Request.BillingAccountID)
	require.Equal(t, 1, authority.reserves)
}

func TestGatewayBillingAuthRouteKeepsExplicitCentralPayerFence(t *testing.T) {
	coordinator, _, baseAuthority, principal := gatewayBillingFixture()
	principal.AuthBilled = true
	baseAuthority.quote.Request.BillingAccountID = "unexpected-payer"
	authority := &gatewayBillingProofCapture{gatewayBillingAuthorityStub: baseAuthority}
	coordinator.authority = authority

	route, err := coordinator.RouteForPrincipal(context.Background(), 1, principal)
	require.NoError(t, err)
	require.Equal(t, "central", route.Mode)
	require.False(t, route.DynamicAuthority)
	execution, err := coordinator.Prepare(context.Background(), *route, principal, "verified-subject-proof", "/v1/responses", "explicit-operation", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`))
	require.ErrorIs(t, err, bc.ErrConflict)
	require.Nil(t, execution)
	require.Zero(t, authority.reserves)
}
