// Package billingmigration contains operator-only, resumable migration tools.
// It never reads API key material, passwords, provider secrets or payment URLs.
package billingmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Mapping struct {
	LocalUserID      int64  `json:"local_user_id"`
	Issuer           string `json:"issuer"`
	ActorUserID      string `json:"actor_user_id"`
	TenantID         string `json:"tenant_id"`
	OriginAppID      string `json:"origin_app_id"`
	BillingAccountID string `json:"billing_account_id"`
	OwnerEpoch       int64  `json:"owner_epoch"`
	IdentityEvidence string `json:"identity_evidence"`
}
type DrainEvidence struct {
	AllGatewayInstancesStoppedOrFenced bool   `json:"all_gateway_instances_stopped_or_fenced"`
	NoInflightProviderWork             bool   `json:"no_inflight_provider_work"`
	NoUnrecordedUsage                  bool   `json:"no_unrecorded_usage"`
	NoPendingMediaTasks                bool   `json:"no_pending_media_tasks"`
	OperatorReference                  string `json:"operator_reference"`
}
type Route struct {
	Mapping
	Mode     string     `json:"mode"`
	BatchID  string     `json:"batch_id"`
	FrozenAt *time.Time `json:"frozen_at,omitempty"`
}
type Snapshot struct {
	SchemaVersion   int               `json:"schema_version"`
	SourceSystem    string            `json:"source_system"`
	Route           Route             `json:"route"`
	TimeZone        string            `json:"time_zone"`
	WalletUnit      string            `json:"wallet_unit"`
	Balance         string            `json:"balance"`
	TotalRecharged  string            `json:"total_recharged"`
	Subscriptions   []json.RawMessage `json:"subscriptions"`
	Credentials     []json.RawMessage `json:"credentials"`
	Payments        []json.RawMessage `json:"payments"`
	Redemptions     []json.RawMessage `json:"redemptions"`
	PricingPolicies []json.RawMessage `json:"pricing_policies"`
	LedgerWatermark json.RawMessage   `json:"ledger_watermark"`
}
type TargetReceipt struct {
	ReceiptID        string `json:"receipt_id"`
	BatchID          string `json:"batch_id"`
	SnapshotSHA256   string `json:"snapshot_sha256"`
	BillingAccountID string `json:"billing_account_id"`
	NextOwnerEpoch   int64  `json:"next_owner_epoch"`
	Reconciled       bool   `json:"reconciled"`
}
type Source struct{ DB *sql.DB }

func (s Source) Map(ctx context.Context, m Mapping) error {
	if m.LocalUserID <= 0 || m.OwnerEpoch < 0 || m.Issuer == "" || m.ActorUserID == "" || m.TenantID == "" || m.OriginAppID == "" || m.BillingAccountID == "" || strings.TrimSpace(m.IdentityEvidence) == "" {
		return errors.New("explicit complete identity mapping and evidence are required; email matching is not supported")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, m.LocalUserID).Scan(&id); err != nil {
		return err
	}
	// A pre-existing verified OIDC binding may not contradict the reviewed map.
	var conflicts int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM api_keys WHERE user_id=$1 AND oidc_managed AND deleted_at IS NULL AND (oidc_issuer IS DISTINCT FROM $2 OR oidc_subject IS DISTINCT FROM $3)`, m.LocalUserID, m.Issuer, m.ActorUserID).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts != 0 {
		return errors.New("mapping conflicts with an existing verified OIDC identity")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_account_routes(local_user_id,billing_mode,oidc_issuer,actor_user_id,tenant_id,origin_app_id,billing_account_id,owner_epoch)
 VALUES($1,'local',$2,$3,$4,$5,$6,$7) ON CONFLICT(local_user_id) DO NOTHING`, m.LocalUserID, m.Issuer, m.ActorUserID, m.TenantID, m.OriginAppID, m.BillingAccountID, m.OwnerEpoch)
	if err != nil {
		return err
	}
	r, err := readRoute(ctx, tx, m.LocalUserID)
	if err != nil {
		return err
	}
	if r.Issuer != m.Issuer || r.ActorUserID != m.ActorUserID || r.TenantID != m.TenantID || r.OriginAppID != m.OriginAppID || r.BillingAccountID != m.BillingAccountID || r.OwnerEpoch != m.OwnerEpoch {
		return errors.New("a different mapping already exists; no implicit replacement is allowed")
	}
	if err = audit(ctx, tx, r, "mapping", "mapping", m); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readRoute(ctx context.Context, q queryer, userID int64) (Route, error) {
	var r Route
	err := q.QueryRowContext(ctx, `SELECT local_user_id,billing_mode,oidc_issuer,actor_user_id,tenant_id,origin_app_id,billing_account_id,owner_epoch,COALESCE(migration_batch_id,''),frozen_at FROM billing_center_account_routes WHERE local_user_id=$1`, userID).Scan(&r.LocalUserID, &r.Mode, &r.Issuer, &r.ActorUserID, &r.TenantID, &r.OriginAppID, &r.BillingAccountID, &r.OwnerEpoch, &r.BatchID, &r.FrozenAt)
	return r, err
}
func audit(ctx context.Context, tx *sql.Tx, r Route, batch, action string, evidence any) error {
	b, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_migration_audit(local_user_id,batch_id,action,owner_epoch,evidence) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.LocalUserID, batch, action, r.OwnerEpoch, string(b))
	if err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT evidence=$4::jsonb FROM billing_center_migration_audit WHERE local_user_id=$1 AND batch_id=$2 AND action=$3`, r.LocalUserID, batch, action, string(b)).Scan(&matches)
	if err == nil && !matches {
		return errors.New("migration audit event already exists with different evidence")
	}
	return err
}
func lockUser(ctx context.Context, tx *sql.Tx, id int64) error {
	var got int64
	return tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&got)
}

func (s Source) Drain(ctx context.Context, userID int64, batch, reference string) error {
	if batch == "" || reference == "" {
		return errors.New("batch and operator reference are required")
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
	if r.Mode == "draining" && r.BatchID == batch {
		return nil
	}
	if r.Mode != "local" && r.Mode != "shadow" {
		return errors.New("only a local/shadow account may start draining")
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_account_routes SET billing_mode='draining',migration_batch_id=$2,updated_at=NOW() WHERE local_user_id=$1`, userID, batch)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, r, batch, "drain", map[string]string{"operator_reference": reference}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Source) Freeze(ctx context.Context, userID int64, batch string, evidence DrainEvidence) error {
	if !evidence.AllGatewayInstancesStoppedOrFenced || !evidence.NoInflightProviderWork || !evidence.NoUnrecordedUsage || !evidence.NoPendingMediaTasks || evidence.OperatorReference == "" {
		return errors.New("freeze requires reviewed evidence that every old worker and unfinished responsibility has drained")
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
	if r.BatchID != batch || batch == "" {
		return errors.New("migration batch mismatch")
	}
	if r.Mode == "frozen" {
		return audit(ctx, tx, r, batch, "freeze", evidence)
	}
	if r.Mode != "draining" {
		return errors.New("account must be draining before freeze")
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM payment_orders WHERE user_id=$1 AND status NOT IN ('COMPLETED','PARTIALLY_REFUNDED','REFUNDED','EXPIRED','CANCELLED')`, userID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("%d payment/refund responsibilities remain; drain or explicitly migrate them before freeze", pending)
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM billing_center_operations WHERE actor_user_id=$1 AND tenant_id=$2 AND state NOT IN ('settled','released')`, r.ActorUserID, r.TenantID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("unresolved central/shadow operations remain")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM billing_usage_entries WHERE user_id=$1 AND NOT applied`, userID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("unapplied legacy usage remains")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM media_generation_jobs WHERE user_id=$1 AND (status NOT IN ('succeeded','failed','canceled') OR (status='succeeded' AND usage_recorded_at IS NULL))`, userID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("unresolved media work or unrecorded successful media usage remains")
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_account_routes SET billing_mode='frozen',frozen_at=NOW(),updated_at=NOW() WHERE local_user_id=$1`, userID)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, r, batch, "freeze", evidence); err != nil {
		return err
	}
	return tx.Commit()
}

// Export reads exact PostgreSQL decimals as strings. No float conversion or
// resetting of negative balances, used counters, windows or expiry takes place.
func (s Source) Export(ctx context.Context, userID int64, batch, timeZone string) (Snapshot, []byte, string, error) {
	var snapshot Snapshot
	if _, err := time.LoadLocation(timeZone); err != nil {
		return snapshot, nil, "", err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return snapshot, nil, "", err
	}
	defer tx.Rollback()
	r, err := readRoute(ctx, tx, userID)
	if err != nil {
		return snapshot, nil, "", err
	}
	if r.Mode != "frozen" || r.BatchID != batch || r.FrozenAt == nil {
		return snapshot, nil, "", errors.New("export requires this batch's frozen source")
	}
	snapshot = Snapshot{SchemaVersion: 1, SourceSystem: "tabro-api", Route: r, TimeZone: timeZone, WalletUnit: "legacy_credit"}
	err = tx.QueryRowContext(ctx, `SELECT balance::text,total_recharged::text FROM users WHERE id=$1`, userID).Scan(&snapshot.Balance, &snapshot.TotalRecharged)
	if err != nil {
		return snapshot, nil, "", err
	}
	queries := []struct {
		dest  *[]json.RawMessage
		query string
	}{
		{&snapshot.Subscriptions, `SELECT jsonb_build_object('id',s.id,'group_id',s.group_id,'starts_at',s.starts_at,'expires_at',s.expires_at,'status',s.status,'deleted_at',s.deleted_at,'daily_window_start',s.daily_window_start,'weekly_window_start',s.weekly_window_start,'monthly_window_start',s.monthly_window_start,'daily_usage',s.daily_usage_usd::text,'weekly_usage',s.weekly_usage_usd::text,'monthly_usage',s.monthly_usage_usd::text,'daily_limit',g.daily_limit_usd::text,'weekly_limit',g.weekly_limit_usd::text,'monthly_limit',g.monthly_limit_usd::text,'rate_multiplier',g.rate_multiplier::text,'platform',g.platform) FROM user_subscriptions s JOIN groups g ON g.id=s.group_id WHERE s.user_id=$1 ORDER BY s.id`},
		{&snapshot.Credentials, `SELECT jsonb_build_object('external_credential_id',id::text,'oidc_managed',oidc_managed,'group_id',group_id,'status',status,'deleted_at',deleted_at,'expires_at',expires_at,'quota',quota::text,'quota_used',quota_used::text,'rate_limit_5h',rate_limit_5h::text,'rate_limit_1d',rate_limit_1d::text,'rate_limit_7d',rate_limit_7d::text,'usage_5h',usage_5h::text,'usage_1d',usage_1d::text,'usage_7d',usage_7d::text,'window_5h_start',window_5h_start,'window_1d_start',window_1d_start,'window_7d_start',window_7d_start) FROM api_keys WHERE user_id=$1 ORDER BY id`},
		{&snapshot.Payments, `SELECT jsonb_build_object('id',id::text,'order_type',order_type,'status',status,'amount',amount::text,'pay_amount',pay_amount::text,'fee_rate',fee_rate::text,'refund_amount',refund_amount::text,'payment_type',payment_type,'provider_instance_id',provider_instance_id,'payment_trade_no',payment_trade_no,'plan_id',plan_id,'subscription_group_id',subscription_group_id,'subscription_days',subscription_days,'paid_at',paid_at,'completed_at',completed_at,'refund_at',refund_at,'force_refund',force_refund) FROM payment_orders WHERE user_id=$1 ORDER BY id`},
		{&snapshot.PricingPolicies, `WITH relevant_groups AS (SELECT group_id AS id FROM api_keys WHERE user_id=$1 UNION SELECT group_id FROM user_subscriptions WHERE user_id=$1), relevant_channels AS (SELECT channel_id AS id FROM channel_groups WHERE group_id IN (SELECT id FROM relevant_groups)), policy_rows AS (
 SELECT 'group' AS kind,g.id AS id,to_jsonb(g)-ARRAY['name','description','created_at','updated_at'] AS body FROM groups g WHERE g.id IN (SELECT id FROM relevant_groups)
 UNION ALL SELECT 'user_rate',r.group_id,to_jsonb(r)-ARRAY['created_at','updated_at'] FROM user_group_rate_multipliers r WHERE user_id=$1
 UNION ALL SELECT 'channel',c.id,to_jsonb(c)-ARRAY['name','description','created_at','updated_at'] FROM channels c WHERE c.id IN (SELECT id FROM relevant_channels)
 UNION ALL SELECT 'channel_group',g.id,to_jsonb(g)-'created_at' FROM channel_groups g WHERE group_id IN (SELECT id FROM relevant_groups)
 UNION ALL SELECT 'model_price',p.id,to_jsonb(p)-ARRAY['created_at','updated_at'] FROM channel_model_pricing p WHERE p.channel_id IN (SELECT id FROM relevant_channels)
 UNION ALL SELECT 'price_interval',i.id,to_jsonb(i)-ARRAY['created_at','updated_at'] FROM channel_pricing_intervals i JOIN channel_model_pricing p ON p.id=i.pricing_id WHERE p.channel_id IN (SELECT id FROM relevant_channels)) SELECT jsonb_build_object('kind',kind,'definition',body) FROM policy_rows ORDER BY kind,id`},
		{&snapshot.Redemptions, `SELECT jsonb_build_object('id',id::text,'type',type,'value',value::text,'status',status,'used_at',used_at,'subscription_group_id',group_id,'validity_days',validity_days) FROM redeem_codes WHERE used_by=$1 ORDER BY id`},
	}
	for _, q := range queries {
		*q.dest = []json.RawMessage{}
		rows, e := tx.QueryContext(ctx, q.query, userID)
		if e != nil {
			return snapshot, nil, "", e
		}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				rows.Close()
				return snapshot, nil, "", e
			}
			*q.dest = append(*q.dest, json.RawMessage(b))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return snapshot, nil, "", e
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT jsonb_build_object('count',count(*),'max_id',COALESCE(max(id),0),'balance_cost_sum',COALESCE(sum(balance_cost),0)::text,'subscription_cost_sum',COALESCE(sum(subscription_cost),0)::text) FROM gateway_usage_ledger WHERE user_id=$1`, userID).Scan(&snapshot.LedgerWatermark)
	if err != nil {
		return snapshot, nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return snapshot, nil, "", err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot, nil, "", err
	}
	h := sha256.Sum256(b)
	return snapshot, b, hex.EncodeToString(h[:]), nil
}

func (s Source) Activate(ctx context.Context, userID int64, receipt TargetReceipt) error {
	if !receipt.Reconciled || receipt.ReceiptID == "" || len(receipt.SnapshotSHA256) != 64 {
		return errors.New("a reconciled target import receipt is required")
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
	if r.BatchID != receipt.BatchID || r.BillingAccountID != receipt.BillingAccountID {
		return errors.New("target import receipt belongs to a different batch/account")
	}
	if r.Mode == "central" && r.OwnerEpoch == receipt.NextOwnerEpoch {
		return audit(ctx, tx, r, r.BatchID, "activate", receipt)
	}
	if r.Mode != "frozen" || receipt.NextOwnerEpoch != r.OwnerEpoch+1 {
		return errors.New("activation requires frozen source and next owner epoch")
	}
	var exported bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_center_migration_audit WHERE local_user_id=$1 AND batch_id=$2 AND action='export' AND evidence->>'snapshot_sha256'=$3)`, userID, r.BatchID, receipt.SnapshotSHA256).Scan(&exported); err != nil {
		return err
	}
	if !exported {
		return errors.New("receipt does not match a recorded source export")
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_account_routes SET billing_mode='central',owner_epoch=$2,snapshot_sha256=$3,target_receipt_id=$4,updated_at=NOW() WHERE local_user_id=$1`, userID, receipt.NextOwnerEpoch, receipt.SnapshotSHA256, receipt.ReceiptID)
	if err != nil {
		return err
	}
	r.OwnerEpoch = receipt.NextOwnerEpoch
	if err = audit(ctx, tx, r, r.BatchID, "activate", receipt); err != nil {
		return err
	}
	return tx.Commit()
}
func (s Source) RecordExport(ctx context.Context, snapshot Snapshot, hash string) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockUser(ctx, tx, snapshot.Route.LocalUserID); err != nil {
		return err
	}
	r, err := readRoute(ctx, tx, snapshot.Route.LocalUserID)
	if err != nil {
		return err
	}
	if r.Mode != "frozen" || r.BatchID != snapshot.Route.BatchID {
		return errors.New("source ownership changed during export")
	}
	if err = audit(ctx, tx, r, r.BatchID, "export", map[string]string{"snapshot_sha256": hash, "time_zone": snapshot.TimeZone}); err != nil {
		return err
	}
	return tx.Commit()
}
