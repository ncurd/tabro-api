package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memoryOperationStore struct {
	mu     sync.Mutex
	op     Operation
	exists bool
}

func (s *memoryOperationStore) CreateIntent(_ context.Context, o Operation) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exists {
		if o.RequestFingerprint != s.op.RequestFingerprint {
			return s.op, false, ErrConflict
		}
		return s.op, false, nil
	}
	o.State = Intent
	o.Version = 1
	s.op = o
	s.exists = true
	return o, true, nil
}
func (s *memoryOperationStore) Get(context.Context, Key) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.op, nil
}
func (s *memoryOperationStore) BindReservation(_ context.Context, _ Key, v int64, r Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != v || s.op.State != Intent {
		return ErrState
	}
	s.op.State = Reserved
	s.op.Version++
	s.op.RemoteVersion = r.Version
	s.op.ReservationID = r.ReservationID
	return nil
}
func (s *memoryOperationStore) ClaimDispatch(_ context.Context, _ Key, v int64, attempt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != v || s.op.State != Reserved {
		return false, nil
	}
	s.op.State = Dispatching
	s.op.AttemptID = attempt
	s.op.Version++
	return true, nil
}
func (s *memoryOperationStore) ConfirmDispatched(_ context.Context, _ Key, v int64, attempt string, r Reservation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != v || s.op.State != Dispatching || s.op.AttemptID != attempt {
		return false, nil
	}
	s.op.State = Dispatched
	s.op.Version++
	s.op.RemoteVersion = r.Version
	return true, nil
}

func TestReserveRecoversAmbiguousCommitAndDispatchOnlyGrantsOneExecution(t *testing.T) {
	var reserves, queries, dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/billing/v1/reservations":
			reserves.Add(1)
			// Simulate a committed reserve whose HTTP response was lost.
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		case "/internal/billing/v1/operations/operation":
			queries.Add(1)
			require.Equal(t, "agent", r.URL.Query().Get("origin_app_id"))
			require.NoError(t, json.NewEncoder(w).Encode(testReservation("reserved", 1)))
		case "/internal/billing/v1/reservations/reservation/dispatch":
			dispatches.Add(1)
			require.NoError(t, json.NewEncoder(w).Encode(testReservation("dispatched", 2)))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service", nil }), nil)
	require.NoError(t, err)
	request := testReserve()
	intent, err := NewIntent(testKey(), "actor", "tenant", "central", request.Request)
	require.NoError(t, err)
	store := &memoryOperationStore{}
	coordinator := Coordinator{Store: store, Authority: client}
	o, err := coordinator.Reserve(context.Background(), intent, request)
	require.NoError(t, err)
	require.Equal(t, Reserved, o.State)
	_, err = coordinator.Reserve(context.Background(), intent, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, reserves.Load())
	require.EqualValues(t, 1, queries.Load())
	var permits atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			permit, _ := coordinator.Dispatch(context.Background(), testKey())
			if permit {
				permits.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, permits.Load())
	require.EqualValues(t, 1, dispatches.Load())
	// Restarting after a dispatched record never grants another permit.
	permit, err := coordinator.Dispatch(context.Background(), testKey())
	require.False(t, permit)
	require.ErrorIs(t, err, ErrState)
}

func TestReserveRejectsChangedRequestUnderSameFingerprint(t *testing.T) {
	request := testReserve()
	intent, err := NewIntent(testKey(), "actor", "tenant", "central", request.Request)
	require.NoError(t, err)
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: "https://billing.invalid"}, tokenFunc(func(context.Context) (string, error) { t.Fatal("must not acquire token"); return "", nil }), nil)
	require.NoError(t, err)
	request.Request.BillingAccountID = "different-payer"
	_, err = (Coordinator{Store: &memoryOperationStore{}, Authority: client}).Reserve(context.Background(), intent, request)
	require.ErrorIs(t, err, ErrConflict)
}

func TestReserveChargeModeIsPartOfFrozenFinancialIdentity(t *testing.T) {
	request := testReserve()
	intent, err := NewIntent(testKey(), "actor", "tenant", "central", request.Request)
	require.NoError(t, err)
	require.NotContains(t, string(intent.RequestPayload), "charge_mode", "old operations retain the historical JSON identity")
	request.Request.ChargeMode = ChargeModeActualUsage
	request.Request.MaximumUsage = map[string]Decimal{"credit_amount": "0"}
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: "https://billing.invalid"}, tokenFunc(func(context.Context) (string, error) {
		t.Fatal("a mode change must be rejected before contacting Auth")
		return "", nil
	}), nil)
	require.NoError(t, err)
	_, err = (Coordinator{Store: &memoryOperationStore{}, Authority: client}).Reserve(context.Background(), intent, request)
	require.ErrorIs(t, err, ErrConflict)
}

func TestScheduledIntentFreezesDeadlineAndPricingWithoutChangingLegacyFingerprint(t *testing.T) {
	request := testReserve().Request
	key := testKey()
	legacyFingerprint, err := Fingerprint(struct {
		Key                         Key
		ActorUserID, TenantID, Mode string
		Request                     ReserveRequest
	}{key, "actor", "tenant", "central", request})
	require.NoError(t, err)
	legacy, err := NewIntent(key, "actor", "tenant", "central", request)
	require.NoError(t, err)
	require.Equal(t, legacyFingerprint, legacy.RequestFingerprint)

	notBefore := time.Date(2026, 10, 9, 0, 0, 0, 123456789, time.FixedZone("CST", 8*3600))
	pricing := json.RawMessage(`{"rate_multiplier":"2.5","price":"0.01"}`)
	scheduled, err := NewScheduledIntent(key, "actor", "tenant", "central", request, notBefore, pricing)
	require.NoError(t, err)
	require.Equal(t, time.UTC, scheduled.SettlementNotBefore.Location())
	require.Equal(t, 123456000, scheduled.SettlementNotBefore.Nanosecond())
	require.NotEqual(t, legacy.RequestFingerprint, scheduled.RequestFingerprint)
	// JSONB changes whitespace and key ordering, and DB timestamps use UTC.
	replayed, err := NewScheduledIntent(key, "actor", "tenant", "central", request, scheduled.SettlementNotBefore, json.RawMessage(`{ "price": "0.01", "rate_multiplier": "2.5" }`))
	require.NoError(t, err)
	require.Equal(t, scheduled.RequestFingerprint, replayed.RequestFingerprint)
	pricing[0] = '['
	require.True(t, json.Valid(scheduled.GatewayPricingSnapshot), "intent owns a copy of its pricing evidence")

	changedTime, err := NewScheduledIntent(key, "actor", "tenant", "central", request, notBefore.Add(time.Hour), scheduled.GatewayPricingSnapshot)
	require.NoError(t, err)
	require.NotEqual(t, scheduled.RequestFingerprint, changedTime.RequestFingerprint)
	changedPrice, err := NewScheduledIntent(key, "actor", "tenant", "central", request, notBefore, json.RawMessage(`{"rate_multiplier":"3","price":"0.01"}`))
	require.NoError(t, err)
	require.NotEqual(t, scheduled.RequestFingerprint, changedPrice.RequestFingerprint)
}

func TestScheduledReserveRejectsMutatedDeadlineOrPricingBeforeNetwork(t *testing.T) {
	request := testReserve()
	client, err := NewClient(Config{ProducerClientID: "gateway-billing", BaseURL: "https://billing.invalid"}, tokenFunc(func(context.Context) (string, error) { t.Fatal("must not acquire token"); return "", nil }), nil)
	require.NoError(t, err)
	for _, change := range []func(*Operation){
		func(op *Operation) { op.SettlementNotBefore = time.Time{} },
		func(op *Operation) { op.GatewayPricingSnapshot = json.RawMessage(`{"rate_multiplier":"4"}`) },
	} {
		intent, err := NewScheduledIntent(testKey(), "actor", "tenant", "central", request.Request, time.Now().Add(time.Hour), json.RawMessage(`{"rate_multiplier":"2"}`))
		require.NoError(t, err)
		change(&intent)
		_, err = (Coordinator{Store: &memoryOperationStore{}, Authority: client}).Reserve(context.Background(), intent, request)
		require.ErrorIs(t, err, ErrConflict)
	}
	_, err = NewScheduledIntent(testKey(), "actor", "tenant", "central", request.Request, time.Now(), json.RawMessage(`{"access_token":"secret"}`))
	require.ErrorIs(t, err, ErrCredentialsInPayload)
}

type actualUsageAuthorityStub struct {
	result       Reservation
	reserveCalls int
}

func (s *actualUsageAuthorityStub) Reserve(context.Context, ProofRequest[ReserveRequest]) (Reservation, error) {
	s.reserveCalls++
	return s.result, nil
}

func (s *actualUsageAuthorityStub) GetOperation(context.Context, Key) (Reservation, error) {
	return s.result, nil
}

func (s *actualUsageAuthorityStub) Dispatch(context.Context, Key, string, TransitionRequest) (Reservation, error) {
	return s.result, nil
}

func TestActualReserveRecoveryRequiresModeAcknowledgementAndZeroHold(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		amount     Decimal
		valid      bool
	}{
		{"actual_zero", ChargeModeActualUsage, "0", true},
		{"old_auth", "", "0", false},
		{"unexpected_hold", ChargeModeActualUsage, "208", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := testReserve()
			request.Request.ChargeMode = ChargeModeActualUsage
			request.Request.MaximumUsage = map[string]Decimal{"credit_amount": "0"}
			intent, err := NewIntent(testKey(), "actor", "tenant", "central", request.Request)
			require.NoError(t, err)
			store := &memoryOperationStore{}
			_, _, err = store.CreateIntent(context.Background(), intent)
			require.NoError(t, err)
			result := testReservation("reserved", 1)
			result.ChargeMode, result.ReservedAmount = tc.mode, tc.amount
			authority := &actualUsageAuthorityStub{result: result}
			coordinator := Coordinator{Store: store, Authority: authority}
			_, err = coordinator.Reserve(context.Background(), intent, request)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, Reserved, store.op.State)
				// A response that drops the mode after remote dispatch must never
				// grant local permission to call the supplier.
				authority.result.ChargeMode = ""
				permit, err := coordinator.Dispatch(context.Background(), testKey())
				require.ErrorIs(t, err, ErrState)
				require.False(t, permit)
				require.Equal(t, Dispatching, store.op.State)
			} else {
				require.Error(t, err)
				require.Equal(t, Intent, store.op.State)
			}
			require.Zero(t, authority.reserveCalls, "recovery must query the original operation instead of reserving another one")
		})
	}
}
