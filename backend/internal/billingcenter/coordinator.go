package billingcenter

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
)

type OperationStore interface {
	CreateIntent(context.Context, Operation) (Operation, bool, error)
	Get(context.Context, Key) (Operation, error)
	BindReservation(context.Context, Key, int64, Reservation) error
	ClaimDispatch(context.Context, Key, int64, string) (bool, error)
	ConfirmDispatched(context.Context, Key, int64, string, Reservation) (bool, error)
}
type Authority interface {
	Reserve(context.Context, ProofRequest[ReserveRequest]) (Reservation, error)
	GetOperation(context.Context, Key) (Reservation, error)
	Dispatch(context.Context, Key, string, TransitionRequest) (Reservation, error)
}
type Coordinator struct {
	Store     OperationStore
	Authority Authority
}

func NewIntent(key Key, actorUserID, tenantID, mode string, request ReserveRequest) (Operation, error) {
	return NewScheduledIntent(key, actorUserID, tenantID, mode, request, time.Time{})
}

// NewScheduledIntent freezes the settlement deadline alongside the financial
// identity. A later settings change cannot accelerate an already reserved bill.
// Legacy intents omit the deadline from the fingerprint to preserve replays.
func NewScheduledIntent(key Key, actorUserID, tenantID, mode string, request ReserveRequest, notBefore time.Time, pricingSnapshots ...json.RawMessage) (Operation, error) {
	if len(pricingSnapshots) > 1 {
		return Operation{}, ErrConflict
	}
	var pricingSnapshot json.RawMessage
	if len(pricingSnapshots) == 1 && len(pricingSnapshots[0]) > 0 {
		pricingSnapshot = append(json.RawMessage(nil), pricingSnapshots[0]...)
		if err := ValidatePersistentJSON(pricingSnapshot); err != nil {
			return Operation{}, err
		}
	}
	var frozenDeadline *time.Time
	if !notBefore.IsZero() {
		// PostgreSQL stores microsecond precision; canonicalize before hashing.
		notBefore = notBefore.UTC().Truncate(time.Microsecond)
		frozenDeadline = &notBefore
	}
	o := Operation{Key: key, ActorUserID: actorUserID, TenantID: tenantID, Mode: mode, OwnerEpoch: request.OwnerEpoch, SettlementNotBefore: notBefore, GatewayPricingSnapshot: pricingSnapshot}
	fingerprint, err := Fingerprint(struct {
		Key                         Key
		ActorUserID, TenantID, Mode string
		Request                     ReserveRequest
		SettlementNotBefore         *time.Time      `json:",omitempty"`
		GatewayPricingSnapshot      json.RawMessage `json:",omitempty"`
	}{key, actorUserID, tenantID, mode, request, frozenDeadline, pricingSnapshot})
	if err != nil {
		return o, err
	}
	o.RequestPayload, err = json.Marshal(request)
	if err != nil {
		return o, err
	}
	o.RequestFingerprint = fingerprint
	return o, o.ValidateIntent()
}

// Reserve saves intent before any remote authorization. It never substitutes a
// local wallet for an unavailable authority and never changes the operation ID.
func (c Coordinator) Reserve(ctx context.Context, intent Operation, request ProofRequest[ReserveRequest]) (Operation, error) {
	if c.Store == nil || c.Authority == nil {
		return Operation{}, errors.New("billing coordinator is not configured")
	}
	if intent.Mode != "central" || request.OriginAppID != intent.OriginAppID || request.Request.OperationID != intent.OperationID || request.Request.OwnerEpoch != intent.OwnerEpoch {
		return Operation{}, ErrConflict
	}
	expected, err := NewScheduledIntent(intent.Key, intent.ActorUserID, intent.TenantID, intent.Mode, request.Request, intent.SettlementNotBefore, intent.GatewayPricingSnapshot)
	if err != nil || expected.RequestFingerprint != intent.RequestFingerprint {
		return Operation{}, ErrConflict
	}
	o, created, err := c.Store.CreateIntent(ctx, intent)
	if err != nil {
		return o, err
	}
	if o.State != Intent {
		return o, nil
	}
	var remote Reservation
	if !created {
		remote, err = c.Authority.GetOperation(ctx, o.Key)
		if err == nil {
			return c.bind(ctx, o, remote)
		}
		var re *RemoteError
		if !errors.As(err, &re) || re.Status != 404 {
			return o, err
		}
	}
	remote, err = c.Authority.Reserve(ctx, request)
	if err != nil {
		var re *RemoteError
		if errors.As(err, &re) && re.UnknownOutcome && ctx.Err() == nil {
			// The request may have committed. Query precisely the same scoped ID.
			remote, err = c.Authority.GetOperation(ctx, o.Key)
		}
		if err != nil {
			return o, err
		}
	}
	return c.bind(ctx, o, remote)
}
func (c Coordinator) bind(ctx context.Context, o Operation, remote Reservation) (Operation, error) {
	if err := validateReservationChargeMode(o, remote); err != nil {
		return o, err
	}
	if err := c.Store.BindReservation(ctx, o.Key, o.Version, remote); err != nil {
		return o, err
	}
	return c.Store.Get(ctx, o.Key)
}

// Dispatch grants a one-shot execution permit. A crash or an uncertain local
// commit conservatively loses the permit: recovery must reconcile, never
// blindly execute again. The caller must not infer permission from row state.
func (c Coordinator) Dispatch(ctx context.Context, key Key) (bool, error) {
	o, err := c.Store.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if o.State != Reserved {
		return false, ErrState
	}
	attemptID := uuid.NewString()
	claimed, err := c.Store.ClaimDispatch(ctx, key, o.Version, attemptID)
	if err != nil || !claimed {
		return false, err
	}
	remote, err := c.Authority.Dispatch(ctx, key, o.ReservationID, TransitionRequest{EventID: attemptID, ExpectedVersion: o.RemoteVersion})
	if err != nil {
		return false, err
	} // dispatching is intentionally never auto-reclaimed.
	if err := validateReservationChargeMode(o, remote); err != nil {
		return false, err
	}
	return c.Store.ConfirmDispatched(ctx, key, o.Version+1, attemptID, remote)
}

// Both fresh Reserve and recovery by GetOperation must acknowledge the frozen
// contract. An old authority must never silently turn actual billing into a
// speculative hold, or grant a dispatch it cannot subsequently charge.
func validateReservationChargeMode(o Operation, remote Reservation) error {
	var request ReserveRequest
	if err := json.Unmarshal(o.RequestPayload, &request); err != nil {
		return ErrConflict
	}
	return validateChargeModeResult(request.ChargeMode, remote)
}

func validateChargeModeResult(mode string, remote Reservation) error {
	if mode != remote.ChargeMode {
		return ErrState
	}
	if mode != ChargeModeActualUsage {
		return nil
	}
	if _, err := remote.ReservedAmount.MarshalJSON(); err != nil {
		return ErrConflict
	}
	amount, ok := new(big.Rat).SetString(string(remote.ReservedAmount))
	if !ok || amount.Sign() != 0 {
		return ErrConflict
	}
	return nil
}
