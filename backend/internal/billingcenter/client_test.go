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

func TestClientOnlyRetriesExactActualUsageFundingConflict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{"pending_funds", 409, `{"code":"billing_actual_usage_pending_funds","message":"private response"}`, true},
		{"other_conflict", 409, `{"code":"conflict","message":"private response"}`, false},
		{"wrong_status", 403, `{"code":"billing_actual_usage_pending_funds"}`, false},
		{"nested_code", 409, `{"error":{"code":"billing_actual_usage_pending_funds"}}`, false},
		{"malformed_json", 409, `{"code":"billing_actual_usage_pending_funds"`, false},
		{"oversized_body", 409, `{"code":"billing_actual_usage_pending_funds","message":"` + strings.Repeat("x", 4096) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service", nil }), nil)
			require.NoError(t, err)
			_, err = client.Reserve(context.Background(), testReserve())
			var remote *RemoteError
			require.ErrorAs(t, err, &remote)
			require.Equal(t, tc.status, remote.Status)
			require.Equal(t, tc.retry, remote.Retryable)
			require.False(t, remote.UnknownOutcome)
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, err.Error(), "pending_funds")
		})
	}
}

func TestClientActualReserveRequiresModeAcknowledgementAndZeroHold(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		reserved   Decimal
		valid      bool
	}{
		{"actual_zero", ChargeModeActualUsage, "0.0000000000", true},
		{"old_auth", "", "0", false},
		{"speculative_hold", ChargeModeActualUsage, "200", false},
		{"missing_amount", ChargeModeActualUsage, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reservation := testReservation("reserved", 1)
				reservation.ChargeMode = tc.mode
				reservation.ReservedAmount = tc.reserved
				if tc.reserved == "" {
					_, _ = io.WriteString(w, `{"operation_id":"operation","reservation_id":"reservation","charge_mode":"actual_usage"}`)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(reservation))
			}))
			defer server.Close()
			client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service", nil }), nil)
			require.NoError(t, err)
			request := testReserve()
			request.Request.ChargeMode = ChargeModeActualUsage
			request.Request.MaximumUsage = map[string]Decimal{"credit_amount": "0"}
			_, err = client.Reserve(context.Background(), request)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestClientActualQuoteRejectsAuthorityWithoutModeAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quote := Quote{QuoteID: "quote", Request: testReserve().Request, EstimatedAmount: "0", WalletUnit: "credit"}
		require.NoError(t, json.NewEncoder(w).Encode(quote))
	}))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service", nil }), nil)
	require.NoError(t, err)
	_, err = client.Quote(context.Background(), ProofRequest[QuoteRequest]{OriginAppID: "agent", SubjectProof: "proof", Request: QuoteRequest{OperationID: "operation", ProductKey: "gateway:credits", MaximumUsage: map[string]Decimal{"credit_amount": "0"}, ChargeMode: ChargeModeActualUsage}})
	require.ErrorIs(t, err, ErrState)
}
