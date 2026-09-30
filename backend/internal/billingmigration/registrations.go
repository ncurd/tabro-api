package billingmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Local operators copy reviewed Auth receipts; neither request headers nor user
// supplied claims can create these mappings. The issuer must match gateway OIDC.
type WorkspaceRegistration struct {
	EventID           string `json:"event_id"`
	Issuer            string `json:"issuer"`
	TenantID          string `json:"tenant_id"`
	AccountID         string `json:"account_id"`
	OwnerEpoch        int64  `json:"owner_epoch"`
	ExpectedEpoch     int64  `json:"expected_epoch"`
	Mode              string `json:"mode"`
	TargetReceipt     string `json:"target_receipt"`
	OperatorReference string `json:"operator_reference"`
}
type CredentialRegistration struct {
	EventID           string `json:"event_id"`
	APIKeyID          int64  `json:"api_key_id"`
	LocalUserID       int64  `json:"local_user_id"`
	BindingID         string `json:"binding_id"`
	Version           int64  `json:"version"`
	ExpectedVersion   int64  `json:"expected_version"`
	Issuer            string `json:"issuer"`
	ActorUserID       string `json:"actor_user_id"`
	TenantID          string `json:"tenant_id"`
	Enabled           bool   `json:"enabled"`
	TargetReceipt     string `json:"target_receipt"`
	OperatorReference string `json:"operator_reference"`
}

func operatorAudit(ctx context.Context, tx *sql.Tx, event, action string, evidence any) (bool, error) {
	b, err := json.Marshal(evidence)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	result, err := tx.ExecContext(ctx, `INSERT INTO billing_center_operator_audit(event_id,action,fingerprint,evidence) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, event, action, hash, string(b))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	var actual, kind string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,action FROM billing_center_operator_audit WHERE event_id=$1`, event).Scan(&actual, &kind)
	if err != nil {
		return false, err
	}
	if actual != hash || kind != action {
		return false, errors.New("operator event conflicts with recorded evidence")
	}
	return n == 0, nil
}
func (s Source) RegisterWorkspace(ctx context.Context, r WorkspaceRegistration) error {
	if r.EventID == "" || r.Issuer == "" || r.TenantID == "" || r.AccountID == "" || r.OwnerEpoch <= 0 || r.TargetReceipt == "" || r.OperatorReference == "" || (r.Mode != "central" && r.Mode != "frozen" && r.Mode != "draining") {
		return errors.New("complete reviewed workspace receipt required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	replay, err := operatorAudit(ctx, tx, r.EventID, "register_workspace", r)
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit()
	}
	// A workspace row must never bypass a personal source which is still draining.
	var conflicts int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM billing_center_account_routes WHERE oidc_issuer=$1 AND tenant_id=$2 AND (billing_mode<>'central' OR billing_account_id<>$3 OR owner_epoch<>$4)`, r.Issuer, r.TenantID, r.AccountID, r.OwnerEpoch).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return errors.New("source personal authority has not reconciled and activated this workspace")
	}
	var account, mode string
	var epoch int64
	err = tx.QueryRowContext(ctx, `SELECT billing_account_id,owner_epoch,billing_mode FROM billing_center_workspace_routes WHERE oidc_issuer=$1 AND tenant_id=$2 FOR UPDATE`, r.Issuer, r.TenantID).Scan(&account, &epoch, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		if r.ExpectedEpoch != 0 {
			return errors.New("workspace expected epoch must be zero for initial registration")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_workspace_routes(oidc_issuer,tenant_id,billing_account_id,owner_epoch,billing_mode) VALUES($1,$2,$3,$4,$5)`, r.Issuer, r.TenantID, r.AccountID, r.OwnerEpoch, r.Mode)
	} else if err == nil {
		if account != r.AccountID || epoch != r.ExpectedEpoch || r.OwnerEpoch < epoch {
			return errors.New("workspace ownership/version mismatch")
		}
		if r.OwnerEpoch != epoch && (mode != "frozen" || r.OwnerEpoch != epoch+1) {
			return errors.New("ownership transfer requires a frozen workspace and exactly the next epoch")
		}
		_, err = tx.ExecContext(ctx, `UPDATE billing_center_workspace_routes SET owner_epoch=$3,billing_mode=$4,updated_at=NOW() WHERE oidc_issuer=$1 AND tenant_id=$2`, r.Issuer, r.TenantID, r.OwnerEpoch, r.Mode)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s Source) RegisterCredential(ctx context.Context, r CredentialRegistration) error {
	if r.EventID == "" || r.APIKeyID <= 0 || r.LocalUserID <= 0 || r.BindingID == "" || r.Version <= 0 || r.Issuer == "" || r.ActorUserID == "" || r.TenantID == "" || r.TargetReceipt == "" || r.OperatorReference == "" {
		return errors.New("complete reviewed Auth credential receipt required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockUser(ctx, tx, r.LocalUserID); err != nil {
		return err
	}
	replay, err := operatorAudit(ctx, tx, r.EventID, "register_credential", r)
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit()
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys k JOIN billing_center_account_routes a ON a.local_user_id=k.user_id WHERE k.id=$1 AND k.user_id=$2 AND NOT k.oidc_managed AND k.deleted_at IS NULL AND a.oidc_issuer=$3 AND a.actor_user_id=$4 AND a.tenant_id=$5 AND a.billing_mode IN ('frozen','central'))`, r.APIKeyID, r.LocalUserID, r.Issuer, r.ActorUserID, r.TenantID).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("credential ownership does not match the frozen reviewed source identity")
	}
	var binding string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT credential_binding_id,credential_version FROM billing_center_credential_bindings WHERE api_key_id=$1 FOR UPDATE`, r.APIKeyID).Scan(&binding, &version)
	if errors.Is(err, sql.ErrNoRows) {
		if r.ExpectedVersion != 0 {
			return errors.New("initial credential binding requires expected version zero")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_credential_bindings(api_key_id,local_user_id,credential_binding_id,credential_version,oidc_issuer,actor_user_id,tenant_id,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, r.APIKeyID, r.LocalUserID, r.BindingID, r.Version, r.Issuer, r.ActorUserID, r.TenantID, r.Enabled)
	} else if err == nil {
		if binding != r.BindingID || version != r.ExpectedVersion || r.Version < version {
			return errors.New("credential receipt cannot rebind ownership or decrease version")
		}
		_, err = tx.ExecContext(ctx, `UPDATE billing_center_credential_bindings SET credential_version=$2,enabled=$3,updated_at=NOW() WHERE api_key_id=$1`, r.APIKeyID, r.Version, r.Enabled)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type ReplayRequest struct {
	EventID           string `json:"event_id"`
	ProducerID        string `json:"producer_id"`
	AppID             string `json:"app_id"`
	OutboxEventID     string `json:"outbox_event_id"`
	OperatorReference string `json:"operator_reference"`
}

func (s Source) Replay(ctx context.Context, r ReplayRequest) error {
	if r.EventID == "" || r.ProducerID == "" || r.AppID == "" || r.OutboxEventID == "" || r.OperatorReference == "" {
		return errors.New("reviewed replay identity and reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	replay, err := operatorAudit(ctx, tx, r.EventID, "replay_outbox", r)
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE billing_center_outbox SET status='pending',available_at=NOW(),lease_token='',lease_until=NULL,last_error_code='',updated_at=NOW() WHERE producer_client_id=$1 AND origin_app_id=$2 AND event_id=$3 AND status IN ('blocked','pending')`, r.ProducerID, r.AppID, r.OutboxEventID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("replay requires one blocked/pending event; payload and identity remain immutable")
	}
	return tx.Commit()
}

// Inspect emits references, states and attempt counts only; no request bodies,
// API keys, PSP credentials or subject proofs are included in the report.
func (s Source) Inspect(ctx context.Context) ([]byte, error) {
	queries := map[string]string{
		"authorities":        `SELECT COALESCE(jsonb_agg(jsonb_build_object('local_user_id',local_user_id,'account_id',billing_account_id,'tenant_id',tenant_id,'mode',billing_mode,'owner_epoch',owner_epoch) ORDER BY local_user_id),'[]') FROM billing_center_account_routes`,
		"pending_operations": `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY updated_at),'[]') FROM (SELECT producer_client_id,origin_app_id,operation_id,reservation_id,state,version,remote_version,updated_at FROM billing_center_operations WHERE state NOT IN ('settled','released') ORDER BY updated_at LIMIT 1000)t`,
		"pending_outbox":     `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY updated_at),'[]') FROM (SELECT producer_client_id,origin_app_id,operation_id,event_id,status,attempts,last_error_code,updated_at FROM billing_center_outbox WHERE status<>'delivered' ORDER BY updated_at LIMIT 1000)t`,
		"pending_payments":   `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY updated_at),'[]') FROM (SELECT producer_client_id,order_id,state,provider_key,provider_instance_id,updated_at FROM billing_center_payment_jobs WHERE state NOT IN ('paid','cancelled') ORDER BY updated_at LIMIT 1000)t`,
		"pending_refunds":    `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY updated_at),'[]') FROM (SELECT producer_client_id,refund_id,order_id,state,provider_key,provider_instance_id,updated_at FROM billing_center_refund_jobs WHERE state NOT IN ('succeeded','failed') ORDER BY updated_at LIMIT 1000)t`,
	}
	result := map[string]json.RawMessage{}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for key, query := range queries {
		var payload []byte
		if err = tx.QueryRowContext(ctx, query).Scan(&payload); err != nil {
			return nil, err
		}
		result[key] = payload
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (s Source) Shadow(ctx context.Context, userID int64, reference string) error {
	if userID <= 0 || reference == "" {
		return errors.New("shadow requires a source user and operator reference")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockUser(ctx, tx, userID); err != nil {
		return err
	}
	r, err := readRoute(ctx, tx, userID)
	if err != nil {
		return err
	}
	if r.Mode != "local" && r.Mode != "shadow" {
		return errors.New("only local authority may enable no-money shadow comparison")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE billing_center_account_routes SET billing_mode='shadow',updated_at=NOW() WHERE local_user_id=$1`, userID); err != nil {
		return err
	}
	if err = audit(ctx, tx, r, reference, "shadow", map[string]string{"operator_reference": reference}); err != nil {
		return err
	}
	return tx.Commit()
}
