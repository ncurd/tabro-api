package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

var _ bc.ReconciliationStore = (*BillingCenterRepository)(nil)

func (r *BillingCenterRepository) PendingReconciliations(ctx context.Context, limit int) ([]bc.Operation, error) {
	if limit <= 0 || limit > 100 {
		return nil, bc.ErrConflict
	}
	// Rotate checked_at so a long-standing unresolved case cannot starve newer
	// cases. The SQL lease is for polling only; financial application uses CAS.
	rows, err := r.db.QueryContext(ctx, `WITH candidates AS (
 SELECT producer_client_id,origin_app_id,operation_id FROM billing_center_operations
 WHERE producer_client_id=$1 AND billing_mode='central' AND reservation_id<>''
 AND state IN ('dispatching','dispatched','settlement_pending','release_pending','reconciliation_required')
 AND (state<>'settlement_pending' OR settlement_not_before IS NULL OR settlement_not_before<=NOW())
 AND updated_at<NOW()-INTERVAL '30 seconds'
 AND (reconciliation_checked_at IS NULL OR reconciliation_checked_at<NOW()-INTERVAL '30 seconds')
 ORDER BY reconciliation_checked_at NULLS FIRST,updated_at LIMIT $2 FOR UPDATE SKIP LOCKED
), marked AS (
 UPDATE billing_center_operations o SET reconciliation_checked_at=NOW() FROM candidates c
 WHERE (o.producer_client_id,o.origin_app_id,o.operation_id)=(c.producer_client_id,c.origin_app_id,c.operation_id)
 RETURNING o.*
) SELECT `+billingOperationColumns+` FROM marked`, r.producer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []bc.Operation
	for rows.Next() {
		op, err := scanBillingOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

func (r *BillingCenterRepository) ApplyReconciliation(ctx context.Context, expected bc.Operation, decision bc.ReconciliationResolution) error {
	if expected.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	if err := bc.ValidateResolution(expected, decision); err != nil {
		return err
	}
	data, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	if err = bc.ValidatePersistentJSON(data); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scanBillingOperation(tx.QueryRowContext(ctx, `SELECT `+billingOperationColumns+` FROM billing_center_operations WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 FOR UPDATE`, expected.ProducerClientID, expected.OriginAppID, expected.OperationID))
	if err != nil {
		return err
	}
	if err = bc.ValidateResolution(op, decision); err != nil {
		return err
	}
	var caseID, hash string
	err = tx.QueryRowContext(ctx, `SELECT case_id,resolution_fingerprint FROM billing_center_reconciliation_receipts WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, op.ProducerClientID, op.OriginAppID, op.OperationID).Scan(&caseID, &hash)
	if err == nil {
		if caseID != decision.Case.CaseID || hash != decision.ResolutionFingerprint || string(op.State) != decision.Reservation.State {
			return bc.ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if op.Version != expected.Version || decision.Reservation.Version <= op.RemoteVersion {
		return bc.ErrState
	}
	switch op.State {
	case bc.Dispatching, bc.Dispatched, bc.SettlementPending, bc.ReleasePending, bc.ReconciliationRequired:
	default:
		return bc.ErrState
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_reconciliation_receipts
 (producer_client_id,origin_app_id,operation_id,case_id,resolution_fingerprint,resolution_action,operator_user_id,resolution)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, op.ProducerClientID, op.OriginAppID, op.OperationID, decision.Case.CaseID, decision.ResolutionFingerprint, decision.Case.ResolutionAction, decision.Case.OperatorUserID, string(data))
	if err != nil {
		return err
	}
	// Preserve original payload, event ID and fingerprint. Clearing the lease
	// prevents an already-running delivery from acknowledging over this decision.
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_outbox SET status='superseded',superseded_by_case_id=$4,superseded_at=NOW(),lease_token='',lease_until=NULL,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND status IN ('pending','processing','blocked')`, op.ProducerClientID, op.OriginAppID, op.OperationID, decision.Case.CaseID)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(decision.Reservation)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE billing_center_operations SET state=$4,version=version+1,remote_version=$5,snapshot=$6,updated_at=NOW()
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND version=$7`, op.ProducerClientID, op.OriginAppID, op.OperationID, decision.Reservation.State, decision.Reservation.Version, string(snapshot), op.Version)
	if err = billingAffected(result, err, bc.ErrState); err != nil {
		return err
	}
	return tx.Commit()
}
