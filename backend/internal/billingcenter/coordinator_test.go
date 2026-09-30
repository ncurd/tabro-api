package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

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
