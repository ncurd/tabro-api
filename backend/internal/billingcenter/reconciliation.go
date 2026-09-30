package billingcenter

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

type ReconciliationCase struct {
	CaseID            string     `json:"case_id"`
	ReservationID     string     `json:"reservation_id"`
	State             string     `json:"state"`
	Reason            string     `json:"reason"`
	EvidenceReference string     `json:"evidence_reference"`
	Version           int64      `json:"version"`
	ResolutionAction  string     `json:"resolution_action"`
	OperatorUserID    string     `json:"operator_user_id"`
	ResolvedAt        *time.Time `json:"resolved_at"`
}

// The subject and producer binding is returned from the frozen reservation,
// never inferred from the operator who resolved it or from a current route.
type ReconciliationResolution struct {
	Case                  ReconciliationCase `json:"case"`
	Reservation           Reservation        `json:"reservation"`
	ResolutionFingerprint string             `json:"resolution_fingerprint"`
	ActorUserID           string             `json:"actor_user_id"`
	TenantID              string             `json:"tenant_id"`
	ProducerClientID      string             `json:"producer_client_id"`
	OriginAppID           string             `json:"origin_app_id"`
}

func (c *Client) Reconciliation(ctx context.Context, key Key, reservationID string) (ReconciliationResolution, error) {
	if err := key.Validate(); err != nil {
		return ReconciliationResolution{}, err
	}
	if key.ProducerClientID != c.producer || reservationID == "" {
		return ReconciliationResolution{}, ErrConflict
	}
	return paymentJSON[ReconciliationResolution](ctx, c, http.MethodGet, "/internal/billing/v1/reservations/"+url.PathEscape(reservationID)+"/reconciliation", url.Values{"origin_app_id": {key.OriginAppID}}, nil)
}

// ValidateResolution verifies the entire frozen financial identity before the
// local terminal command may be superseded by an administrator's decision.
func ValidateResolution(op Operation, decision ReconciliationResolution) error {
	r, c := decision.Reservation, decision.Case
	digest, err := hex.DecodeString(decision.ResolutionFingerprint)
	if err != nil || len(digest) != 32 || c.CaseID == "" || c.State != "resolved" || c.OperatorUserID == "" || c.ResolvedAt == nil || c.ResolvedAt.IsZero() || c.Version <= 0 || c.Reason == "" || c.EvidenceReference == "" {
		return ErrConflict
	}
	if op.Mode != "central" || decision.ActorUserID != op.ActorUserID || decision.TenantID != op.TenantID || decision.ProducerClientID != op.ProducerClientID || decision.OriginAppID != op.OriginAppID {
		return ErrConflict
	}
	if r.OperationID != op.OperationID || r.ReservationID == "" || r.ReservationID != op.ReservationID || c.ReservationID != op.ReservationID || r.OwnerEpoch != op.OwnerEpoch || r.Version < op.RemoteVersion {
		return ErrConflict
	}
	var frozen ReserveRequest
	if err := json.Unmarshal(op.RequestPayload, &frozen); err != nil {
		return ErrConflict
	}
	if r.BillingAccountID != frozen.BillingAccountID || r.BalanceID != frozen.BalanceID || r.PriceVersionID != frozen.PriceVersionID || r.MemberAllocationID != frozen.MemberAllocationID {
		return ErrConflict
	}
	var previous Reservation
	if err := json.Unmarshal(op.Snapshot, &previous); err != nil || previous.WalletUnit == "" || r.WalletUnit != previous.WalletUnit || r.ProductVersion != previous.ProductVersion {
		return ErrConflict
	}
	previousPeriods, resolvedPeriods := slices.Clone(previous.PeriodIDs), slices.Clone(r.PeriodIDs)
	slices.Sort(previousPeriods)
	slices.Sort(resolvedPeriods)
	if !slices.Equal(previousPeriods, resolvedPeriods) {
		return ErrConflict
	}
	if c.ResolutionAction == "verified_usage" && r.State == "settled" || c.ResolutionAction == "waive" && r.State == "released" {
		return nil
	}
	return ErrState
}

type ReconciliationStore interface {
	PendingReconciliations(context.Context, int) ([]Operation, error)
	ApplyReconciliation(context.Context, Operation, ReconciliationResolution) error
}
type ReconciliationReader interface {
	Reconciliation(context.Context, Key, string) (ReconciliationResolution, error)
}

func (w *Worker) reconcile(ctx context.Context) error {
	store, ok := w.store.(ReconciliationStore)
	if !ok {
		return nil
	}
	reader, ok := w.sender.(ReconciliationReader)
	if !ok {
		return nil
	}
	ops, err := store.PendingReconciliations(ctx, w.config.BatchSize)
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	errorsCh := make(chan error, len(ops))
	for _, op := range ops {
		workers.Add(1)
		go func(op Operation) {
			defer workers.Done()
			attempt, cancel := context.WithTimeout(ctx, w.config.AttemptTimeout)
			decision, err := reader.Reconciliation(attempt, op.Key, op.ReservationID)
			cancel()
			if err != nil {
				var remote *RemoteError
				if errors.As(err, &remote) && remote.Status == http.StatusNotFound {
					return
				}
				errorsCh <- err
				return
			}
			if decision.Case.State != "resolved" {
				return
			}
			if err = ValidateResolution(op, decision); err == nil {
				err = store.ApplyReconciliation(ctx, op, decision)
			}
			if err != nil {
				errorsCh <- err
			}
		}(op)
	}
	workers.Wait()
	close(errorsCh)
	var failures []error
	for err := range errorsCh {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
