package billingcenter

import (
	"context"
	"encoding/json"
	"errors"

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
	o := Operation{Key: key, ActorUserID: actorUserID, TenantID: tenantID, Mode: mode, OwnerEpoch: request.OwnerEpoch}
	fingerprint, err := Fingerprint(struct {
		Key                         Key
		ActorUserID, TenantID, Mode string
		Request                     ReserveRequest
	}{key, actorUserID, tenantID, mode, request})
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
	expected, err := NewIntent(intent.Key, intent.ActorUserID, intent.TenantID, intent.Mode, request.Request)
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
	return c.Store.ConfirmDispatched(ctx, key, o.Version+1, attemptID, remote)
}
