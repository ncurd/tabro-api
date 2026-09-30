package billingcenter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type tokenFunc func(context.Context) (string, error)

func (f tokenFunc) Token(ctx context.Context) (string, error) { return f(ctx) }
func testReservation(state string, version int64) Reservation {
	return Reservation{OperationID: "operation", ReservationID: "reservation", State: state, Version: version, BillingAccountID: "payer", OwnerEpoch: 3, WalletUnit: "credit", ReservedAmount: "1.2345678901", SettledAmount: "0", PriceVersionID: "price-v1", ProductVersion: "product-v1", BalanceID: "balance", PeriodIDs: []string{"period"}, ExpiresAt: time.Now().Add(time.Hour).UTC()}
}
func testKey() Key {
	return Key{ProducerClientID: "gateway-billing", OriginAppID: "agent", OperationID: "operation"}
}
func testReserve() ProofRequest[ReserveRequest] {
	return ProofRequest[ReserveRequest]{OriginAppID: "agent", SubjectProof: "user-model-token", Request: ReserveRequest{OperationID: "operation", BillingAccountID: "payer", BalanceID: "balance", PriceVersionID: "price-v1", OwnerEpoch: 3, ServiceTier: "default", MaximumUsage: map[string]Decimal{"input": "9007199254740993", "output": "128000"}}}
}

func TestClientSeparatesServiceBearerAndSubjectProofWithExactDecimalContract(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer billing-service-token", r.Header.Get("Authorization"))
		require.Equal(t, "/internal/billing/v1/reservations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(testReservation("reserved", 1)))
	}))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "billing-service-token", nil }), nil)
	require.NoError(t, err)
	result, err := client.Reserve(context.Background(), testReserve())
	require.NoError(t, err)
	require.Equal(t, Decimal("1.2345678901"), result.ReservedAmount)
	require.Equal(t, "user-model-token", body["subject_proof"])
	require.Equal(t, "9007199254740993", body["request"].(map[string]any)["maximum_usage"].(map[string]any)["input"])
	require.NotContains(t, body, "actor_user_id")
	require.NotContains(t, body, "client_secret")
}

func TestClientDeliveryBindsOriginAndNeverSendsSubjectProof(t *testing.T) {
	event, err := NewSettlement(testKey(), "reservation", SettleRequest{TransitionRequest: TransitionRequest{EventID: "event", ExpectedVersion: 2}, UsageItemID: "usage", Usage: map[string]Decimal{"input": "42"}, EvidenceReference: "usage-ledger:1", UsageComplete: true})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/billing/v1/reservations/reservation/settle", r.URL.Path)
		require.Equal(t, "agent", r.URL.Query().Get("origin_app_id"))
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, string(event.Body), string(data))
		require.NotContains(t, string(data), "subject_proof")
		require.NotContains(t, string(data), "actor_user_id")
		require.NoError(t, json.NewEncoder(w).Encode(testReservation("settled", 3)))
	}))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service-token", nil }), nil)
	require.NoError(t, err)
	_, err = client.Deliver(context.Background(), event)
	require.NoError(t, err)
}

func TestClientRejectsRedirectAndRedactsRemoteErrors(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, `{"message":"secret user-model-token"}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "secret", nil }), nil)
	require.NoError(t, err)
	_, err = client.Reserve(context.Background(), testReserve())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
	require.Zero(t, leaked.Load())
	_, err = NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: "http://billing.example"}, tokenFunc(func(context.Context) (string, error) { return "secret", nil }), nil)
	require.Error(t, err)
}

func TestTokenSourceCachesCredentialsAndRefreshesAfterInvalidation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		require.Equal(t, "billing.reserve billing.settle", r.Form.Get("scope"))
		require.Equal(t, "gateway-client", r.Form.Get("client_id"))
		require.Equal(t, "private", r.Form.Get("client_secret"))
		require.Empty(t, r.Form.Get("subject_token"))
		_, _ = io.WriteString(w, `{"access_token":"service","token_type":"Bearer","expires_in":600}`)
	}))
	defer server.Close()
	source, err := NewClientCredentialsTokenSource(TokenConfig{TokenURL: server.URL, ClientID: "gateway-client", ClientSecret: "private", Scopes: []string{"billing.reserve", "billing.settle"}, InsecureLocal: true}, nil)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := source.Token(context.Background())
			require.NoError(t, err)
			require.Equal(t, "service", got)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, requests.Load())
	source.Invalidate()
	_, err = source.Token(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, requests.Load())
}

func TestDecimalAndPersistentPayloadRejectLossyOrCredentialData(t *testing.T) {
	for _, input := range []string{`1.25`, `"NaN"`, `"1e-6"`, `null`} {
		var d Decimal
		require.Error(t, json.Unmarshal([]byte(input), &d))
	}
	var d Decimal
	require.NoError(t, json.Unmarshal([]byte(`"0.0000000001"`), &d))
	require.Equal(t, Decimal("0.0000000001"), d)
	_, err := Fingerprint(testReserve())
	require.ErrorIs(t, err, ErrCredentialsInPayload)
	first, err := Fingerprint(json.RawMessage(`{"z":{"b":9007199254740993,"a":"v"},"a":1}`))
	require.NoError(t, err)
	second, err := Fingerprint(json.RawMessage(`{"a":1,"z":{"a":"v","b":9007199254740993}}`))
	require.NoError(t, err)
	require.Equal(t, first, second)
	event := Event{Key: testKey(), ID: "event", Kind: SettleEvent, ReservationID: "reservation", Body: json.RawMessage(`{"event_id":"event","expected_version":2,"usage_item_id":"u","usage":{"input":"1"},"evidence_reference":"e","actor_user_id":"other"}`)}
	require.Error(t, event.Validate())
}

func TestServiceTokenFailureNeverLeaksOrAttemptsReserve(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "", errors.New("private secret") }), nil)
	require.NoError(t, err)
	_, err = client.Reserve(context.Background(), testReserve())
	require.Error(t, err)
	require.True(t, IsRetryable(err))
	require.False(t, strings.Contains(err.Error(), "private"))
	require.Zero(t, calls.Load())
}
