package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/google/uuid"
)

type BillingCenterRepository struct {
	db       *sql.DB
	producer string
}

func NewBillingCenterRepository(db *sql.DB, producerClientID string) *BillingCenterRepository {
	return &BillingCenterRepository{db: db, producer: producerClientID}
}

var _ bc.OutboxStore = (*BillingCenterRepository)(nil)

const billingOperationColumns = `producer_client_id, origin_app_id, operation_id, actor_user_id, tenant_id,
 request_fingerprint, billing_mode, owner_epoch, state, version, attempt_id, reservation_id,
 remote_version, request_payload, snapshot, created_at, updated_at, settlement_not_before, gateway_pricing_snapshot`

type billingRowScanner interface{ Scan(...any) error }

func scanBillingOperation(row billingRowScanner) (bc.Operation, error) {
	var o bc.Operation
	var notBefore sql.NullTime
	var pricingSnapshot []byte
	err := row.Scan(&o.ProducerClientID, &o.OriginAppID, &o.OperationID, &o.ActorUserID, &o.TenantID,
		&o.RequestFingerprint, &o.Mode, &o.OwnerEpoch, &o.State, &o.Version, &o.AttemptID, &o.ReservationID,
		&o.RemoteVersion, &o.RequestPayload, &o.Snapshot, &o.CreatedAt, &o.UpdatedAt, &notBefore, &pricingSnapshot)
	o.GatewayPricingSnapshot = pricingSnapshot
	if notBefore.Valid {
		o.SettlementNotBefore = notBefore.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return o, bc.ErrNotFound
	}
	return o, err
}

func (r *BillingCenterRepository) Get(ctx context.Context, key bc.Key) (bc.Operation, error) {
	if key.ProducerClientID != r.producer {
		return bc.Operation{}, bc.ErrConflict
	}
	return scanBillingOperation(r.db.QueryRowContext(ctx, `SELECT `+billingOperationColumns+`
 FROM billing_center_operations WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, key.ProducerClientID, key.OriginAppID, key.OperationID))
}

// CreateIntent is idempotent even after an ambiguous commit. A caller cannot
// change the actor, tenant, epoch or billing mode by reusing an operation ID.
func (r *BillingCenterRepository) CreateIntent(ctx context.Context, o bc.Operation) (bc.Operation, bool, error) {
	if o.ProducerClientID != r.producer {
		return bc.Operation{}, false, bc.ErrConflict
	}
	if err := o.ValidateIntent(); err != nil {
		return bc.Operation{}, false, err
	}
	if err := bc.ValidatePersistentJSON(o.RequestPayload); err != nil {
		return bc.Operation{}, false, err
	}
	var pricingSnapshot any
	if len(o.GatewayPricingSnapshot) > 0 {
		if err := bc.ValidatePersistentJSON(o.GatewayPricingSnapshot); err != nil {
			return bc.Operation{}, false, err
		}
		pricingSnapshot = string(o.GatewayPricingSnapshot)
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO billing_center_operations
 (producer_client_id,origin_app_id,operation_id,actor_user_id,tenant_id,request_fingerprint,billing_mode,owner_epoch,request_payload,settlement_not_before,gateway_pricing_snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, o.ProducerClientID, o.OriginAppID, o.OperationID,
		o.ActorUserID, o.TenantID, o.RequestFingerprint, o.Mode, o.OwnerEpoch, string(o.RequestPayload), billingSettlementDeadline(o), pricingSnapshot)
	if err != nil {
		return bc.Operation{}, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return bc.Operation{}, false, err
	}
	stored, err := r.Get(ctx, o.Key)
	if err != nil {
		return stored, false, err
	}
	if stored.ActorUserID != o.ActorUserID || stored.TenantID != o.TenantID || stored.Mode != o.Mode || stored.OwnerEpoch != o.OwnerEpoch || stored.RequestFingerprint != o.RequestFingerprint || !stored.SettlementNotBefore.Equal(o.SettlementNotBefore) {
		return stored, false, bc.ErrConflict
	}
	return stored, created == 1, nil
}

func billingSettlementDeadline(o bc.Operation) any {
	if o.SettlementNotBefore.IsZero() {
		return nil
	}
	return o.SettlementNotBefore
}

func reservationJSON(key bc.Key, remote bc.Reservation) ([]byte, error) {
	if remote.OperationID != key.OperationID || remote.ReservationID == "" || remote.Version <= 0 {
		return nil, bc.ErrConflict
	}
	b, err := json.Marshal(remote)
	if err != nil {
		return nil, err
	}
	return b, bc.ValidatePersistentJSON(b)
}

func (r *BillingCenterRepository) BindReservation(ctx context.Context, key bc.Key, version int64, remote bc.Reservation) error {
	if key.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	if remote.State != "reserved" {
		return bc.ErrState
	}
	b, err := reservationJSON(key, remote)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE billing_center_operations SET state='reserved',version=version+1,
 reservation_id=$5,remote_version=$6,snapshot=$7,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND version=$4
 AND state='intent' AND billing_mode='central' AND owner_epoch=$8`, key.ProducerClientID, key.OriginAppID, key.OperationID, version,
		remote.ReservationID, remote.Version, string(b), remote.OwnerEpoch)
	return billingAffected(result, err, bc.ErrState)
}

// ClaimDispatch never reclaims a crashed dispatch attempt. Its ambiguity must
// be reconciled, not turned into permission for a second supplier execution.
func (r *BillingCenterRepository) ClaimDispatch(ctx context.Context, key bc.Key, version int64, attemptID string) (bool, error) {
	if key.ProducerClientID != r.producer {
		return false, bc.ErrConflict
	}
	if attemptID == "" || len(attemptID) > 200 {
		return false, bc.ErrConflict
	}
	result, err := r.db.ExecContext(ctx, `UPDATE billing_center_operations SET state='dispatching',attempt_id=$5,
 version=version+1,updated_at=NOW() WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3
 AND state='reserved' AND billing_mode='central' AND version=$4`, key.ProducerClientID, key.OriginAppID, key.OperationID, version, attemptID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ConfirmDispatched returns execution permission only to the successful CAS
// owner. Reading a previously dispatched row never grants execution permission.
func (r *BillingCenterRepository) ConfirmDispatched(ctx context.Context, key bc.Key, version int64, attemptID string, remote bc.Reservation) (bool, error) {
	if key.ProducerClientID != r.producer {
		return false, bc.ErrConflict
	}
	if remote.State != "dispatched" {
		return false, bc.ErrState
	}
	b, err := reservationJSON(key, remote)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE billing_center_operations SET state='dispatched',version=version+1,
 remote_version=$6,snapshot=$7,updated_at=NOW() WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3
 AND state='dispatching' AND version=$4 AND attempt_id=$5 AND reservation_id=$8 AND owner_epoch=$9`,
		key.ProducerClientID, key.OriginAppID, key.OperationID, version, attemptID, remote.Version, string(b), remote.ReservationID, remote.OwnerEpoch)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *BillingCenterRepository) MarkReconciliation(ctx context.Context, key bc.Key, version int64) error {
	if key.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	result, err := r.db.ExecContext(ctx, `UPDATE billing_center_operations SET state='reconciliation_required',version=version+1,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND version=$4 AND state IN ('dispatching','dispatched')`,
		key.ProducerClientID, key.OriginAppID, key.OperationID, version)
	return billingAffected(result, err, bc.ErrState)
}

func (r *BillingCenterRepository) Enqueue(ctx context.Context, event bc.Event) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	inserted, err := r.EnqueueTx(ctx, tx, event)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

// EnqueueTx joins the caller's usage/provider-effects transaction. Only apply
// those local effects when this returns true; commit them with the outbox row.
// Auth network calls are never made inside this transaction.
func (r *BillingCenterRepository) EnqueueTx(ctx context.Context, tx *sql.Tx, event bc.Event) (bool, error) {
	if tx == nil {
		return false, errors.New("billing enqueue requires transaction")
	}
	if event.ProducerClientID != r.producer {
		return false, bc.ErrConflict
	}
	if err := event.Validate(); err != nil {
		return false, err
	}
	fingerprint, err := bc.Fingerprint(struct {
		Key           bc.Key
		ID            string
		Kind          bc.EventKind
		ReservationID string
		Body          json.RawMessage
	}{event.Key, event.ID, event.Kind, event.ReservationID, event.Body})
	if err != nil {
		return false, err
	}
	o, err := scanBillingOperation(tx.QueryRowContext(ctx, `SELECT `+billingOperationColumns+` FROM billing_center_operations
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 FOR UPDATE`, event.ProducerClientID, event.OriginAppID, event.OperationID))
	if err != nil {
		return false, err
	}
	var oldID, oldFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT event_id,payload_fingerprint FROM billing_center_outbox
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, event.ProducerClientID, event.OriginAppID, event.OperationID).Scan(&oldID, &oldFingerprint)
	if err == nil {
		if oldID == event.ID && oldFingerprint == fingerprint {
			return false, nil
		}
		return false, bc.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if o.Mode != "central" || o.ReservationID != event.ReservationID {
		return false, bc.ErrConflict
	}
	var request bc.TransitionRequest
	if err = json.Unmarshal(event.Body, &request); err != nil {
		return false, err
	}
	if request.EventID != event.ID || request.ExpectedVersion != o.RemoteVersion {
		return false, bc.ErrConflict
	}
	next := bc.SettlementPending
	if event.Kind == bc.SettleEvent {
		if o.State != bc.Dispatched && o.State != bc.ReconciliationRequired {
			return false, bc.ErrState
		}
	} else {
		next = bc.ReleasePending
		if o.State != bc.Reserved && o.State != bc.Dispatched && o.State != bc.ReconciliationRequired {
			return false, bc.ErrState
		}
	}
	// The frozen operation owns the cutoff. Releases free failed-call holds
	// immediately; successful usage remains durable and held until the cutoff.
	// A call that finishes after its cutoff is eligible immediately.
	var notBefore any
	if event.Kind == bc.SettleEvent {
		notBefore = billingSettlementDeadline(o)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_outbox
 (producer_client_id,origin_app_id,operation_id,event_id,kind,reservation_id,payload,payload_fingerprint,available_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,GREATEST(NOW(),$9::timestamptz))`, event.ProducerClientID, event.OriginAppID, event.OperationID, event.ID, event.Kind, event.ReservationID, string(event.Body), fingerprint, notBefore)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_operations SET state=$4,version=version+1,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, event.ProducerClientID, event.OriginAppID, event.OperationID, next)
	return err == nil, err
}

// Claim uses database time, SKIP LOCKED and a fresh token per lease. An expired
// worker cannot acknowledge or reschedule a row claimed by another process.
func (r *BillingCenterRepository) Claim(ctx context.Context, lease time.Duration, limit int) ([]bc.Event, error) {
	if r.producer == "" || lease <= 0 || limit <= 0 || limit > 100 {
		return nil, errors.New("invalid billing outbox lease or batch")
	}
	token := uuid.NewString()
	rows, err := r.db.QueryContext(ctx, `WITH candidates AS (
 SELECT producer_client_id,origin_app_id,event_id FROM billing_center_outbox
 WHERE producer_client_id=$4 AND status IN ('pending','processing') AND available_at<=NOW() AND (lease_until IS NULL OR lease_until<=NOW())
 ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT $1
 ) UPDATE billing_center_outbox e SET status='processing',attempts=e.attempts+1,lease_token=$2,
 lease_until=NOW()+($3*INTERVAL '1 millisecond'),updated_at=NOW() FROM candidates c
 WHERE e.producer_client_id=c.producer_client_id AND e.origin_app_id=c.origin_app_id AND e.event_id=c.event_id
 RETURNING e.producer_client_id,e.origin_app_id,e.operation_id,e.event_id,e.kind,e.reservation_id,e.payload,e.payload_fingerprint,e.lease_token,e.lease_until,e.attempts`, limit, token, lease.Milliseconds(), r.producer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []bc.Event{}
	for rows.Next() {
		var event bc.Event
		if err = rows.Scan(&event.ProducerClientID, &event.OriginAppID, &event.OperationID, &event.ID, &event.Kind, &event.ReservationID, &event.Body, &event.Fingerprint, &event.LeaseToken, &event.LeaseUntil, &event.Attempts); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *BillingCenterRepository) Complete(ctx context.Context, event bc.Event, remote bc.Reservation) error {
	if event.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	target := bc.Settled
	pending := bc.SettlementPending
	if event.Kind == bc.ReleaseEvent {
		target = bc.Released
		pending = bc.ReleasePending
	}
	if remote.State != string(target) || remote.ReservationID != event.ReservationID {
		return bc.ErrConflict
	}
	snapshot, err := reservationJSON(event.Key, remote)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE billing_center_outbox SET status='delivered',delivered_at=NOW(),lease_token='',lease_until=NULL,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND event_id=$3 AND status='processing' AND lease_token=$4 AND lease_until>NOW()`,
		event.ProducerClientID, event.OriginAppID, event.ID, event.LeaseToken)
	if err = billingAffected(result, err, bc.ErrLeaseLost); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE billing_center_operations SET state=$4,version=version+1,remote_version=$5,snapshot=$6,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND state=$7 AND reservation_id=$8 AND owner_epoch=$9 AND remote_version<$5`,
		event.ProducerClientID, event.OriginAppID, event.OperationID, target, remote.Version, string(snapshot), pending, event.ReservationID, remote.OwnerEpoch)
	if err = billingAffected(result, err, bc.ErrState); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *BillingCenterRepository) Retry(ctx context.Context, event bc.Event, delay time.Duration, code string, permanent bool) error {
	if event.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	// Codes are controlled classifications, never remote bodies or request data.
	if len(code) > 120 {
		return errors.New("billing error code too long")
	}
	status := "pending"
	if permanent {
		status = "blocked"
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE billing_center_outbox SET status=$5,available_at=NOW()+($6*INTERVAL '1 millisecond'),
 last_error_code=$7,lease_token='',lease_until=NULL,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND event_id=$3 AND status='processing' AND lease_token=$4 AND lease_until>NOW()`,
		event.ProducerClientID, event.OriginAppID, event.ID, event.LeaseToken, status, delay.Milliseconds(), code)
	if err = billingAffected(result, err, bc.ErrLeaseLost); err != nil {
		return err
	}
	if permanent {
		_, err = tx.ExecContext(ctx, `UPDATE billing_center_operations SET state='reconciliation_required',version=version+1,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND state IN ('settlement_pending','release_pending')`, event.ProducerClientID, event.OriginAppID, event.OperationID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func billingAffected(result sql.Result, err, errorIfMissing error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("missing billing mutation result")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errorIfMissing
	}
	return nil
}

func (r *BillingCenterRepository) GetTerminalEvent(ctx context.Context, key bc.Key) (bc.Event, error) {
	event := bc.Event{Key: key}
	if key.ProducerClientID != r.producer {
		return event, bc.ErrConflict
	}
	err := r.db.QueryRowContext(ctx, `SELECT event_id,kind,reservation_id,payload,payload_fingerprint FROM billing_center_outbox WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, key.ProducerClientID, key.OriginAppID, key.OperationID).Scan(&event.ID, &event.Kind, &event.ReservationID, &event.Body, &event.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return event, bc.ErrNotFound
	}
	return event, err
}
