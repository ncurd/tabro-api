package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func ProvideGatewayBillingRepository(db *sql.DB, cfg *config.Config) service.GatewayBillingRepository {
	return NewBillingCenterRepository(db, cfg.BillingCenter.ProducerClientID)
}
func (r *BillingCenterRepository) GetBillingRoute(ctx context.Context, userID int64) (*service.GatewayBillingRoute, error) {
	var route service.GatewayBillingRoute
	err := r.db.QueryRowContext(ctx, `SELECT local_user_id,billing_mode,oidc_issuer,actor_user_id,tenant_id,origin_app_id,billing_account_id,owner_epoch FROM billing_center_account_routes WHERE local_user_id=$1`, userID).Scan(&route.LocalUserID, &route.Mode, &route.Issuer, &route.ActorUserID, &route.TenantID, &route.OriginAppID, &route.BillingAccountID, &route.OwnerEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &route, nil
}
func (r *BillingCenterRepository) RecordBillingShadow(ctx context.Context, route service.GatewayBillingRoute, e *bc.Execution) error {
	if e == nil || e.Mode != "shadow" || e.Key.ProducerClientID != r.producer || e.Quote.QuoteID == "" {
		return bc.ErrConflict
	}
	snapshot, err := json.Marshal(e.Quote)
	if err != nil {
		return err
	}
	if err = bc.ValidatePersistentJSON(snapshot); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO billing_center_shadow_observations(producer_client_id,origin_app_id,operation_id,local_user_id,product_key,request_payload_hash,quote)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, e.Key.ProducerClientID, e.Key.OriginAppID, e.Key.OperationID, route.LocalUserID, e.ProductKey, e.RequestPayloadHash, string(snapshot))
	if err != nil {
		return err
	}
	var hash string
	var localUserID int64
	err = r.db.QueryRowContext(ctx, `SELECT request_payload_hash,local_user_id FROM billing_center_shadow_observations WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3`, e.Key.ProducerClientID, e.Key.OriginAppID, e.Key.OperationID).Scan(&hash, &localUserID)
	if err != nil {
		return err
	}
	if hash != e.RequestPayloadHash || localUserID != route.LocalUserID {
		return bc.ErrConflict
	}
	return nil
}

func (r *BillingCenterRepository) PendingBillingShadows(ctx context.Context, limit int) ([]service.GatewayBillingShadow, error) {
	if limit <= 0 || limit > 100 {
		return nil, bc.ErrConflict
	}
	rows, err := r.db.QueryContext(ctx, `SELECT producer_client_id,origin_app_id,operation_id,quote,usage FROM billing_center_shadow_observations
 WHERE producer_client_id=$1 AND usage IS NOT NULL AND central_estimate IS NULL ORDER BY created_at LIMIT $2`, r.producer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []service.GatewayBillingShadow{}
	for rows.Next() {
		var row service.GatewayBillingShadow
		var quote, usage []byte
		if err := rows.Scan(&row.Key.ProducerClientID, &row.Key.OriginAppID, &row.Key.OperationID, &quote, &usage); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(quote, &row.Quote); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(usage, &row.Usage); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
func (r *BillingCenterRepository) CompleteBillingShadow(ctx context.Context, row service.GatewayBillingShadow, estimate bc.Estimate) error {
	if row.Key.ProducerClientID != r.producer || estimate.PriceVersionID != row.Quote.Request.PriceVersionID || estimate.WalletUnit != row.Quote.WalletUnit {
		return bc.ErrConflict
	}
	encoded, err := json.Marshal(estimate)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE billing_center_shadow_observations SET central_estimate=$4,updated_at=NOW() WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND quote->>'quote_id'=$5 AND central_estimate IS NULL`, row.Key.ProducerClientID, row.Key.OriginAppID, row.Key.OperationID, string(encoded), row.Quote.QuoteID)
	return err
}

func (r *BillingCenterRepository) RecordBillingShadowFailure(ctx context.Context, route service.GatewayBillingRoute, key bc.Key, hash, stage string) error {
	if route.Mode != "shadow" || key.ProducerClientID != r.producer || stage != "quote_failed" {
		return bc.ErrConflict
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO billing_center_shadow_failures(producer_client_id,origin_app_id,operation_id,local_user_id,request_payload_hash,stage)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, key.ProducerClientID, key.OriginAppID, key.OperationID, route.LocalUserID, hash, stage)
	return err
}

func (r *BillingCenterRepository) MarkBillingShadowEstimateFailure(ctx context.Context, row service.GatewayBillingShadow) error {
	if row.Key.ProducerClientID != r.producer {
		return bc.ErrConflict
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO billing_center_shadow_failures(producer_client_id,origin_app_id,operation_id,local_user_id,request_payload_hash,stage)
 SELECT producer_client_id,origin_app_id,operation_id,local_user_id,request_payload_hash,'estimate_failed' FROM billing_center_shadow_observations
 WHERE producer_client_id=$1 AND origin_app_id=$2 AND operation_id=$3 AND central_estimate IS NULL ON CONFLICT DO NOTHING`, row.Key.ProducerClientID, row.Key.OriginAppID, row.Key.OperationID)
	return err
}

func (r *BillingCenterRepository) GetBillingWorkspaceRoute(ctx context.Context, issuer, tenant string) (*service.GatewayBillingRoute, error) {
	var route service.GatewayBillingRoute
	err := r.db.QueryRowContext(ctx, `SELECT oidc_issuer,tenant_id,billing_account_id,owner_epoch,billing_mode FROM billing_center_workspace_routes WHERE oidc_issuer=$1 AND tenant_id=$2`, issuer, tenant).Scan(&route.Issuer, &route.TenantID, &route.BillingAccountID, &route.OwnerEpoch, &route.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &route, nil
}

func (r *BillingCenterRepository) GetBillingCredential(ctx context.Context, apiKeyID, userID int64) (*service.GatewayBillingCredential, error) {
	var c service.GatewayBillingCredential
	err := r.db.QueryRowContext(ctx, `SELECT b.api_key_id,b.local_user_id,b.credential_binding_id,b.credential_version,b.oidc_issuer,b.actor_user_id,b.tenant_id
 FROM billing_center_credential_bindings b JOIN api_keys k ON k.id=b.api_key_id AND k.user_id=b.local_user_id
 WHERE b.api_key_id=$1 AND b.local_user_id=$2 AND b.enabled=TRUE AND k.deleted_at IS NULL`, apiKeyID, userID).Scan(&c.APIKeyID, &c.LocalUserID, &c.BindingID, &c.Version, &c.Issuer, &c.ActorUserID, &c.TenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
