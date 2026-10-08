//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
)

func gatewayOIDCBillingSettings() (*SettingService, *oidcOnlySettingRepo) {
	repo := &oidcOnlySettingRepo{values: map[string]string{
		SettingKeyOIDCOnlyEnabled: "true", SettingKeyOIDCBillingEnabled: "true",
		SettingKeyOIDCBillingRateMultiplier: "2.5",
	}}
	return NewSettingService(repo, oidcBillingTestConfig()), repo
}

func TestOIDCBillingSwitchForcesAuthAndRejectsLocalFallback(t *testing.T) {
	s, repo, _, principal := gatewayBillingFixture()
	s.settings, _ = gatewayOIDCBillingSettings()
	repo.route = nil
	route, err := s.RouteForPrincipal(context.Background(), 1, principal)
	require.NoError(t, err)
	require.True(t, route.DynamicAuthority)
	require.True(t, principal.AuthBilled)
	_, err = s.RouteForPrincipal(context.Background(), 1, nil)
	require.ErrorIs(t, err, bc.ErrState, "an unbound local API key must not spend the local wallet")
	repo.route = &GatewayBillingRoute{Mode: "local"}
	_, err = s.RouteForPrincipal(context.Background(), 1, principal)
	require.ErrorIs(t, err, bc.ErrState, "explicit financial ownership cannot be silently migrated")
	s.settings.settingRepo.(*oidcOnlySettingRepo).err = errors.New("settings unavailable")
	_, err = s.RouteForPrincipal(context.Background(), 1, principal)
	require.Error(t, err)
}

type policyCreditPricer struct{ calls int }

func (p *policyCreditPricer) MaximumCredit(ctx context.Context, _ *APIKey, _ string, _ bc.QuoteRequest) (bc.Decimal, json.RawMessage, error) {
	p.calls++
	rate, _ := ctx.Value(oidcBillingMultiplierContextKey{}).(float64)
	maximum, err := gatewayCreditDecimal(10*rate, true)
	if err != nil {
		return "", nil, err
	}
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: "gpt-6-sol", RateMultiplier: rate,
		Resolved: &ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2}}})
	return maximum, snapshot, err
}

func TestOIDCBillingFreezesMultiplierAndDailyCutoffAcrossRetriesAndSettingsChanges(t *testing.T) {
	s, repo, authority, principal := gatewayBillingFixture()
	settings, settingsRepo := gatewayOIDCBillingSettings()
	s.settings = settings
	pricer := &policyCreditPricer{}
	s.creditPricer = pricer
	body := []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`)
	before := time.Now()
	e, err := s.PrepareForKey(context.Background(), *repo.route, principal, "proof", "/v1/responses", "daily", body, nil)
	require.NoError(t, err)
	require.True(t, e.SettlementNotBefore.After(before))
	zone, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, "00:00", e.SettlementNotBefore.In(zone).Format("15:04"))
	require.Equal(t, bc.Decimal("25.0000000001"), e.Quote.Request.MaximumUsage[gatewayCreditMeter], "admission rounds upward with the existing floating-point safety margin")
	require.NotEmpty(t, repo.op.GatewayPricingSnapshot)

	settingsRepo.values[SettingKeyOIDCBillingRateMultiplier] = "9"
	settingsRepo.values[SettingKeyOIDCBillingSettlementTime] = "12:30"
	settingsRepo.values[SettingKeyOIDCBillingEnabled] = "false"
	again, err := s.PrepareForKey(context.Background(), *repo.route, principal, "fresh-proof", "/v1/responses", "daily", body, nil)
	require.NoError(t, err)
	require.Equal(t, e.GatewayPricingSnapshot, again.GatewayPricingSnapshot)
	require.Equal(t, e.SettlementNotBefore, again.SettlementNotBefore)
	require.Equal(t, e.Quote.Request.MaximumUsage, again.Quote.Request.MaximumUsage)
	require.Equal(t, 1, authority.reserves)
	require.Equal(t, 1, pricer.calls)
	restored, err := s.Restore(context.Background(), e.Snapshot())
	require.NoError(t, err)
	require.Equal(t, e.SettlementNotBefore, restored.SettlementNotBefore)

	gateway := &GatewayService{billingService: &BillingService{}, resolver: &ModelPricingResolver{}}
	cost, _, rate, err := gateway.frozenGatewayCreditCost(context.Background(), again,
		&ForwardResult{Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 20}}, &recordUsageOpts{})
	require.NoError(t, err)
	require.Equal(t, 2.5, rate)
	require.Equal(t, 12.5, cost.ActualCost)
	ctx := bc.WithExecution(context.Background(), again)
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	cmd := &UsageBillingCommand{RequestID: "daily", InputTokens: 10, OutputTokens: 20, ActualCost: cost.ActualCost, BalanceCost: 12.5, SubscriptionCost: 12.5, APIKeyQuotaCost: 12.5}
	require.NoError(t, attachCentralUsage(ctx, cmd))
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	var bill bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &bill))
	require.Equal(t, bc.Decimal("12.5"), bill.Usage[gatewayCreditMeter])

	tampered := e.Snapshot()
	tampered.SettlementNotBefore = tampered.SettlementNotBefore.Add(-time.Hour)
	_, err = s.Restore(context.Background(), tampered)
	require.ErrorIs(t, err, bc.ErrConflict)
}

func TestOIDCBillingSwitchEnablesVerifiedAutomaticIdentity(t *testing.T) {
	repo := &gatewayAutoIdentityRepo{}
	server, token, _ := newAutoProvisionResourceServer(t, repo)
	server.cfg.AutoProvision = false
	server.settings, _ = gatewayOIDCBillingSettings()
	server.settings.cfg.OIDC.IssuerURL = server.cfg.IssuerURL
	server.settings.cfg.Gateway.ResourceServer.IssuerURL = server.cfg.IssuerURL
	key, principal, err := server.Authenticate(context.Background(), token)
	require.NoError(t, err)
	require.True(t, key.AuthBillingOnly)
	require.True(t, principal.AuthBilled)
	server.settings.settingRepo.(*oidcOnlySettingRepo).values[SettingKeyOIDCBillingEnabled] = "false"
	_, _, err = server.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable, "an Auth-only identity cannot fall back after disabling the switch")
}
