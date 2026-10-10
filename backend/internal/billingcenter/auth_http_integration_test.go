//go:build billing_auth_integration

package billingcenter

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The fixture must be created by the isolated Auth Kestrel test host, never by
// copying production settings. Its transient subject token is used only by
// Quote/Reserve/Extend; all other calls carry only the service credential.
func TestAuthBillingRealHTTPContract(t *testing.T) {
	fixturePath := os.Getenv("BILLING_AUTH_TEST_FIXTURE")
	if fixturePath == "" {
		t.Skip("isolated Auth HTTP fixture is not configured")
	}
	resolved, err := filepath.EvalSymlinks(fixturePath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(resolved, "/private/tmp/") || strings.HasPrefix(resolved, "/tmp/"), "fixture must be ephemeral")
	data, err := os.ReadFile(resolved)
	require.NoError(t, err)
	var fixture struct {
		BaseURL           string `json:"base_url"`
		TokenURL          string `json:"token_url"`
		ClientID          string `json:"client_id"`
		ClientSecret      string `json:"client_secret"`
		SubjectProof      string `json:"subject_proof"`
		OriginAppID       string `json:"origin_app_id"`
		ProductKey        string `json:"product_key"`
		GatewayProductKey string `json:"gateway_product_key"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	for _, endpoint := range []string{fixture.BaseURL, fixture.TokenURL} {
		u, err := url.Parse(endpoint)
		require.NoError(t, err)
		ip := net.ParseIP(u.Hostname())
		require.True(t, u.Hostname() == "localhost" || ip != nil && ip.IsLoopback(), "test endpoints must be loopback")
	}
	tokens, err := NewClientCredentialsTokenSource(TokenConfig{TokenURL: fixture.TokenURL, ClientID: fixture.ClientID, ClientSecret: fixture.ClientSecret, Scopes: []string{"billing.reserve", "billing.dispatch", "billing.extend", "billing.settle", "billing.release", "billing.read"}, InsecureLocal: true}, nil)
	require.NoError(t, err)
	client, err := NewClient(Config{BaseURL: fixture.BaseURL, ProducerClientID: fixture.ClientID, InsecureLocal: true}, tokens, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opID := "go-http-" + uuid.NewString()
	key := Key{ProducerClientID: fixture.ClientID, OriginAppID: fixture.OriginAppID, OperationID: opID}
	maxUsage := map[string]Decimal{"input_tokens": "10", "output_tokens": "5", "cache_read_tokens": "0", "cache_write_tokens": "0", "cache_write_5m_tokens": "0", "cache_write_1h_tokens": "0", "image_output_tokens": "0", "request_count": "1", "image_count": "0"}
	hash, err := Fingerprint(struct{ ID string }{opID})
	require.NoError(t, err)
	quote, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{OperationID: opID, ProductKey: fixture.ProductKey, MaximumUsage: maxUsage, ServiceTier: "default", RequestPayloadHash: hash, QuoteMode: "admission"}})
	require.NoError(t, err)
	require.NotEmpty(t, quote.QuoteID)
	reserved, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: quote.Request})
	require.NoError(t, err)
	require.Equal(t, "reserved", reserved.State)
	require.Empty(t, reserved.ChargeMode, "historical model reservations must retain the original hold contract")
	repeat, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: quote.Request})
	require.NoError(t, err)
	require.Equal(t, reserved, repeat)
	maxUsage["input_tokens"] = "20"
	extended, err := client.Extend(ctx, reserved.ReservationID, ProofRequest[ExtendRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: ExtendRequest{TransitionRequest: TransitionRequest{EventID: "extend-" + opID, ExpectedVersion: reserved.Version}, MaximumUsage: maxUsage}})
	require.NoError(t, err)
	dispatched, err := client.Dispatch(ctx, key, reserved.ReservationID, TransitionRequest{EventID: "dispatch-" + opID, ExpectedVersion: extended.Version})
	require.NoError(t, err)
	require.Equal(t, "dispatched", dispatched.State)
	usage := map[string]Decimal{"input_tokens": "3", "output_tokens": "2", "request_count": "1"}
	event, err := NewSettlement(key, reserved.ReservationID, SettleRequest{TransitionRequest: TransitionRequest{EventID: "settle-" + opID, ExpectedVersion: dispatched.Version}, UsageItemID: opID, Usage: usage, EvidenceReference: "test-ledger:" + opID, UsageComplete: true})
	require.NoError(t, err)
	settled, err := client.Deliver(ctx, event)
	require.NoError(t, err)
	require.Equal(t, "settled", settled.State)
	replay, err := client.Deliver(ctx, event)
	require.NoError(t, err)
	require.Equal(t, settled, replay)
	fetched, err := client.GetOperation(ctx, key)
	require.NoError(t, err)
	require.Equal(t, settled, fetched)
	wrongOrigin := key
	wrongOrigin.OriginAppID += "-unauthorized"
	_, err = client.GetOperation(ctx, wrongOrigin)
	require.Error(t, err)
	shadowID := "go-shadow-" + uuid.NewString()
	shadow, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{OperationID: shadowID, ProductKey: fixture.ProductKey, MaximumUsage: maxUsage, ServiceTier: "default", QuoteMode: "shadow"}})
	require.NoError(t, err)
	estimate, err := client.Estimate(ctx, key, shadow.QuoteID, usage)
	require.NoError(t, err)
	require.Equal(t, shadow.Request.PriceVersionID, estimate.PriceVersionID)
	require.Equal(t, settled.SettledAmount, estimate.Amount)
	shadowKey := key
	shadowKey.OperationID = shadowID
	_, err = client.GetOperation(ctx, shadowKey)
	require.Error(t, err)
	releaseID := "go-release-" + uuid.NewString()
	releaseKey := key
	releaseKey.OperationID = releaseID
	releaseQuote, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{OperationID: releaseID, ProductKey: fixture.ProductKey, MaximumUsage: maxUsage, ServiceTier: "default"}})
	require.NoError(t, err)
	pending, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: releaseQuote.Request})
	require.NoError(t, err)
	releaseEvent, err := NewRelease(releaseKey, pending.ReservationID, ReleaseRequest{TransitionRequest: TransitionRequest{EventID: "release-" + releaseID, ExpectedVersion: pending.Version}, Reason: "test_no_supplier_execution", ConfirmedNoBillableWork: true})
	require.NoError(t, err)
	released, err := client.Deliver(ctx, releaseEvent)
	require.NoError(t, err)
	require.Equal(t, "released", released.State)

	// The gateway settles its own frozen price as generic credits. Auth must
	// charge the credit_amount meter at 1:1 rather than reprice model tokens.
	require.Equal(t, "gateway:credits", fixture.GatewayProductKey)
	creditID := "go-credits-" + uuid.NewString()
	creditKey := key
	creditKey.OperationID = creditID
	creditUsage := map[string]Decimal{"credit_amount": "4.125"}
	creditHash, err := Fingerprint(struct{ ID string }{creditID})
	require.NoError(t, err)
	creditQuote, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{OperationID: creditID, ProductKey: fixture.GatewayProductKey, MaximumUsage: creditUsage, ServiceTier: "default", RequestPayloadHash: creditHash, QuoteMode: "admission"}})
	require.NoError(t, err)
	creditReserved, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: creditQuote.Request})
	require.NoError(t, err)
	require.Empty(t, creditReserved.ChargeMode, "historical credit reservations must retain the original hold contract")
	require.True(t, PaymentAmountsEqual("4.125", creditReserved.ReservedAmount))
	creditDispatched, err := client.Dispatch(ctx, creditKey, creditReserved.ReservationID, TransitionRequest{EventID: "dispatch-" + creditID, ExpectedVersion: creditReserved.Version})
	require.NoError(t, err)
	creditEvent, err := NewSettlement(creditKey, creditReserved.ReservationID, SettleRequest{TransitionRequest: TransitionRequest{EventID: "settle-" + creditID, ExpectedVersion: creditDispatched.Version}, UsageItemID: creditID, Usage: map[string]Decimal{"credit_amount": "2.75"}, EvidenceReference: "test-ledger:" + creditID, UsageComplete: true})
	require.NoError(t, err)
	creditSettled, err := client.Deliver(ctx, creditEvent)
	require.NoError(t, err)
	require.Equal(t, "settled", creditSettled.State)
	require.True(t, PaymentAmountsEqual("2.75", creditSettled.SettledAmount))
	creditReplay, err := client.Deliver(ctx, creditEvent)
	require.NoError(t, err)
	require.Equal(t, creditSettled, creditReplay)

	// The current gateway contract authorizes execution with no maximum hold,
	// then reports the exact immutable credits after receiving complete usage.
	actualID := "go-actual-" + uuid.NewString()
	actualKey := key
	actualKey.OperationID = actualID
	actualQuote, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{
		OperationID: actualID, ProductKey: fixture.GatewayProductKey, MaximumUsage: map[string]Decimal{"credit_amount": "0"},
		ServiceTier: "default", QuoteMode: "admission", ChargeMode: ChargeModeActualUsage,
	}})
	require.NoError(t, err)
	require.Equal(t, ChargeModeActualUsage, actualQuote.Request.ChargeMode)
	require.True(t, PaymentAmountsEqual("0", actualQuote.EstimatedAmount))
	actualReserved, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: actualQuote.Request})
	require.NoError(t, err)
	require.Equal(t, "reserved", actualReserved.State)
	require.Equal(t, ChargeModeActualUsage, actualReserved.ChargeMode)
	require.True(t, PaymentAmountsEqual("0", actualReserved.ReservedAmount))
	actualReserveReplay, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: actualQuote.Request})
	require.NoError(t, err)
	require.Equal(t, actualReserved, actualReserveReplay)
	actualDispatched, err := client.Dispatch(ctx, actualKey, actualReserved.ReservationID, TransitionRequest{EventID: "dispatch-" + actualID, ExpectedVersion: actualReserved.Version})
	require.NoError(t, err)
	require.Equal(t, "dispatched", actualDispatched.State)
	require.Equal(t, ChargeModeActualUsage, actualDispatched.ChargeMode)
	require.True(t, PaymentAmountsEqual("0", actualDispatched.ReservedAmount))
	actualEvent, err := NewSettlement(actualKey, actualReserved.ReservationID, SettleRequest{
		TransitionRequest: TransitionRequest{EventID: "settle-" + actualID, ExpectedVersion: actualDispatched.Version},
		UsageItemID:       actualID, Usage: map[string]Decimal{"credit_amount": "2.75"}, EvidenceReference: "test-ledger:" + actualID, UsageComplete: true,
	})
	require.NoError(t, err)
	actualSettled, err := client.Deliver(ctx, actualEvent)
	require.NoError(t, err)
	require.Equal(t, "settled", actualSettled.State)
	require.Equal(t, ChargeModeActualUsage, actualSettled.ChargeMode)
	require.True(t, PaymentAmountsEqual("0", actualSettled.ReservedAmount))
	require.True(t, PaymentAmountsEqual("2.75", actualSettled.SettledAmount))
	actualReplay, err := client.Deliver(ctx, actualEvent)
	require.NoError(t, err)
	require.Equal(t, actualSettled, actualReplay)
	actualFetched, err := client.GetOperation(ctx, actualKey)
	require.NoError(t, err)
	require.Equal(t, actualSettled, actualFetched)

	// A measured charge larger than the wallet is a precise retriable 409.
	// Auth must retain its immutable usage without a debit or new credit hold.
	unpaidID := "go-unpaid-" + uuid.NewString()
	unpaidKey := key
	unpaidKey.OperationID = unpaidID
	unpaidQuote, err := client.Quote(ctx, ProofRequest[QuoteRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: QuoteRequest{
		OperationID: unpaidID, ProductKey: fixture.GatewayProductKey, MaximumUsage: map[string]Decimal{"credit_amount": "0"},
		ServiceTier: "default", QuoteMode: "admission", ChargeMode: ChargeModeActualUsage,
	}})
	require.NoError(t, err)
	unpaidReserved, err := client.Reserve(ctx, ProofRequest[ReserveRequest]{OriginAppID: fixture.OriginAppID, SubjectProof: fixture.SubjectProof, Request: unpaidQuote.Request})
	require.NoError(t, err)
	unpaidDispatched, err := client.Dispatch(ctx, unpaidKey, unpaidReserved.ReservationID, TransitionRequest{EventID: "dispatch-" + unpaidID, ExpectedVersion: unpaidReserved.Version})
	require.NoError(t, err)
	unpaidEvent, err := NewSettlement(unpaidKey, unpaidReserved.ReservationID, SettleRequest{
		TransitionRequest: TransitionRequest{EventID: "settle-" + unpaidID, ExpectedVersion: unpaidDispatched.Version},
		UsageItemID:       unpaidID, Usage: map[string]Decimal{"credit_amount": "5000"}, EvidenceReference: "test-ledger:" + unpaidID, UsageComplete: true,
	})
	require.NoError(t, err)
	for attempt := 0; attempt < 2; attempt++ {
		_, err = client.Deliver(ctx, unpaidEvent)
		var remote *RemoteError
		require.ErrorAs(t, err, &remote)
		require.Equal(t, 409, remote.Status)
		require.True(t, remote.Retryable)
		require.False(t, remote.UnknownOutcome)
	}
	unpaid, err := client.GetOperation(ctx, unpaidKey)
	require.NoError(t, err)
	require.Equal(t, "dispatched", unpaid.State)
	require.True(t, PaymentAmountsEqual("0", unpaid.ReservedAmount))
	require.True(t, PaymentAmountsEqual("0", unpaid.SettledAmount))
	require.NoError(t, os.WriteFile(fixturePath+".actual-usage-done", nil, 0600))
	require.NoError(t, os.WriteFile(fixturePath+".credits-done", nil, 0600))
}
