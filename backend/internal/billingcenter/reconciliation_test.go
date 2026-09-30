package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type reconciliationOutbox struct {
	recoveryOutbox
	op      Operation
	applied int
}

func (s *reconciliationOutbox) PendingReconciliations(context.Context, int) ([]Operation, error) {
	return []Operation{s.op}, nil
}
func (s *reconciliationOutbox) ApplyReconciliation(_ context.Context, _ Operation, _ ReconciliationResolution) error {
	s.applied++
	return nil
}

func TestWorkerQueriesOperatorResolutionWithOnlyBoundServiceCredential(t *testing.T) {
	op, valid := validReconciliation(t)
	for _, mode := range []string{"valid", "pending", "foreign_tenant", "missing"} {
		t.Run(mode, func(t *testing.T) {
			decision := valid
			if mode == "pending" {
				decision.Case.State = "pending"
			}
			if mode == "foreign_tenant" {
				decision.TenantID = "foreign"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/internal/billing/v1/reservations/"+op.ReservationID+"/reconciliation", r.URL.Path)
				require.Equal(t, op.OriginAppID, r.URL.Query().Get("origin_app_id"))
				require.Equal(t, "Bearer service-only", r.Header.Get("Authorization"))
				require.Zero(t, r.ContentLength)
				if mode == "missing" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(decision))
			}))
			defer server.Close()
			client, err := NewClient(Config{BaseURL: server.URL, ProducerClientID: op.ProducerClientID, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "service-only", nil }), nil)
			require.NoError(t, err)
			store := &reconciliationOutbox{recoveryOutbox: recoveryOutbox{complete: true}, op: op}
			worker, err := NewWorker(store, client, WorkerConfig{})
			require.NoError(t, err)
			err = worker.RunOnce(context.Background())
			if mode == "foreign_tenant" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if mode == "valid" {
				require.Equal(t, 1, store.applied)
			} else {
				require.Zero(t, store.applied)
			}
		})
	}
}

func validReconciliation(t *testing.T) (Operation, ReconciliationResolution) {
	t.Helper()
	request := ReserveRequest{OperationID: "op", BillingAccountID: "account", BalanceID: "balance", PriceVersionID: "price", MemberAllocationID: "member", OwnerEpoch: 2, MaximumUsage: map[string]Decimal{"input_tokens": "10"}, ServiceTier: "default"}
	op, err := NewIntent(Key{ProducerClientID: "producer", OriginAppID: "app", OperationID: "op"}, "actor", "tenant", "central", request)
	require.NoError(t, err)
	op.ReservationID, op.RemoteVersion, op.State = "reservation", 2, ReconciliationRequired
	reservation := Reservation{OperationID: op.OperationID, ReservationID: op.ReservationID, BillingAccountID: "account", BalanceID: "balance", PriceVersionID: "price", MemberAllocationID: "member", OwnerEpoch: 2, WalletUnit: "credit", ProductVersion: "product", ReservedAmount: "10", SettledAmount: "0", Version: 2, State: "dispatched"}
	op.Snapshot, err = json.Marshal(reservation)
	require.NoError(t, err)
	reservation.Version, reservation.State, reservation.ReservedAmount, reservation.SettledAmount = 3, "settled", "0", "5"
	now := time.Now().UTC()
	return op, ReconciliationResolution{Case: ReconciliationCase{CaseID: "case", ReservationID: op.ReservationID, State: "resolved", Reason: "verified", EvidenceReference: "provider:receipt", Version: 2, ResolutionAction: "verified_usage", OperatorUserID: "operator", ResolvedAt: &now}, Reservation: reservation, ResolutionFingerprint: strings.Repeat("a", 64), ActorUserID: op.ActorUserID, TenantID: op.TenantID, ProducerClientID: op.ProducerClientID, OriginAppID: op.OriginAppID}
}

func TestReconciliationRequiresFrozenIdentityAndExplicitOperatorDecision(t *testing.T) {
	op, valid := validReconciliation(t)
	require.NoError(t, ValidateResolution(op, valid))
	waive := valid
	waive.Case.ResolutionAction, waive.Reservation.State = "waive", "released"
	require.NoError(t, ValidateResolution(op, waive))
	for name, mutate := range map[string]func(*ReconciliationResolution){
		"actor":              func(r *ReconciliationResolution) { r.ActorUserID = "another" },
		"tenant":             func(r *ReconciliationResolution) { r.TenantID = "another" },
		"producer":           func(r *ReconciliationResolution) { r.ProducerClientID = "another" },
		"app":                func(r *ReconciliationResolution) { r.OriginAppID = "another" },
		"payer":              func(r *ReconciliationResolution) { r.Reservation.BillingAccountID = "another" },
		"balance":            func(r *ReconciliationResolution) { r.Reservation.BalanceID = "another" },
		"allocation":         func(r *ReconciliationResolution) { r.Reservation.MemberAllocationID = "another" },
		"price":              func(r *ReconciliationResolution) { r.Reservation.PriceVersionID = "another" },
		"epoch":              func(r *ReconciliationResolution) { r.Reservation.OwnerEpoch++ },
		"reservation":        func(r *ReconciliationResolution) { r.Case.ReservationID = "another" },
		"operation":          func(r *ReconciliationResolution) { r.Reservation.OperationID = "another" },
		"wallet":             func(r *ReconciliationResolution) { r.Reservation.WalletUnit = "another" },
		"product":            func(r *ReconciliationResolution) { r.Reservation.ProductVersion = "another" },
		"period":             func(r *ReconciliationResolution) { r.Reservation.PeriodIDs = []string{"another"} },
		"unresolved":         func(r *ReconciliationResolution) { r.Case.State = "pending" },
		"no_operator":        func(r *ReconciliationResolution) { r.Case.OperatorUserID = "" },
		"no_hash":            func(r *ReconciliationResolution) { r.ResolutionFingerprint = "" },
		"bad_hash":           func(r *ReconciliationResolution) { r.ResolutionFingerprint = strings.Repeat("z", 64) },
		"no_resolution_time": func(r *ReconciliationResolution) { r.Case.ResolvedAt = nil },
		"wrong_action":       func(r *ReconciliationResolution) { r.Case.ResolutionAction = "waive" },
		"normal_settlement":  func(r *ReconciliationResolution) { r.Case.ResolutionAction = "settled" },
		"old_version":        func(r *ReconciliationResolution) { r.Reservation.Version = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			require.Error(t, ValidateResolution(op, changed))
		})
	}
}
