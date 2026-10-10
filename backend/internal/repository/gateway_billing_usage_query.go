package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type gatewayBillingUsageRepository struct{ db *sql.DB }

func NewGatewayBillingUsageRepository(db *sql.DB) service.GatewayBillingUsageRepository {
	return &gatewayBillingUsageRepository{db: db}
}

// Both the immutable billing operation and its atomic usage evidence must agree
// with every verified identity component. Sharing a local key never shares usage.
const gatewayBillingUsageFrom = ` FROM gateway_usage_ledger g
 JOIN billing_center_operations o ON o.producer_client_id=g.billing_producer_client_id
 AND o.origin_app_id=g.billing_origin_app_id AND o.operation_id=g.billing_operation_id
 WHERE g.oidc_issuer=$1 AND g.oidc_subject=$2 AND g.oidc_tenant=$3
 AND o.actor_user_id=$2 AND o.tenant_id=$3 AND o.origin_app_id=$4
 AND g.billing_mode='central' AND o.billing_mode='central'
 AND g.created_at >= $5 AND g.created_at < $6
 AND ($7='' OR o.operation_id=$7)`

func (r *gatewayBillingUsageRepository) ListGatewayBillingUsage(ctx context.Context, q service.GatewayBillingUsageQuery) (_ *service.GatewayBillingUsagePage, err error) {
	identity := q.Identity
	for _, value := range []string{identity.Issuer, identity.Subject, identity.Tenant, identity.AppID} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("verified billing identity is required")
		}
	}
	if !q.Start.Before(q.End) || q.Page < 1 || q.Page > 10000 || q.PageSize < 1 || q.PageSize > 100 || q.Timezone == "" {
		return nil, fmt.Errorf("invalid billing usage query")
	}
	args := []any{identity.Issuer, identity.Subject, identity.Tenant, identity.AppID, q.Start, q.End, q.OperationID}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	page := &service.GatewayBillingUsagePage{Unit: "credit", Timezone: q.Timezone, Page: q.Page, PageSize: q.PageSize,
		Items: make([]service.GatewayBillingUsageItem, 0), Daily: make([]service.GatewayBillingUsageDay, 0)}
	if err = tx.QueryRowContext(ctx, `SELECT count(*)`+gatewayBillingUsageFrom, args...).Scan(&page.Total); err != nil {
		return nil, err
	}
	itemsArgs := append(append([]any{}, args...), q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT g.id::text,o.operation_id,g.request_id,
 COALESCE(NULLIF(g.requested_model,''),g.model),COALESCE(g.service_tier,''),
 g.input_tokens,g.output_tokens,g.cache_creation_tokens,g.cache_read_tokens,g.image_count,
 COALESCE(g.media_type,''),g.actual_cost::text,
 COALESCE((o.gateway_pricing_snapshot->>'rate_multiplier')::numeric,g.rate_multiplier)::text,
 o.state,g.created_at,CASE WHEN o.state='settled' THEN o.updated_at END`+
		gatewayBillingUsageFrom+` ORDER BY g.created_at DESC,g.id DESC LIMIT $8 OFFSET $9`, itemsArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item service.GatewayBillingUsageItem
		var confirmedAt sql.NullTime
		if err = rows.Scan(&item.ID, &item.OperationID, &item.RequestID, &item.Model, &item.ServiceTier,
			&item.InputTokens, &item.OutputTokens, &item.CacheCreationTokens, &item.CacheReadTokens, &item.ImageCount,
			&item.MediaType, &item.Credits, &item.RateMultiplier, &item.State, &item.CreatedAt, &confirmedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if confirmedAt.Valid {
			item.SettledAt = &confirmedAt.Time
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	dailyArgs := append(append([]any{}, args...), q.Timezone)
	rows, err = tx.QueryContext(ctx, `SELECT to_char(g.created_at AT TIME ZONE $8,'YYYY-MM-DD'),count(*),
 sum(g.actual_cost)::text,
 COALESCE(sum(g.actual_cost) FILTER (WHERE o.state='settled'),0)::text,
 COALESCE(sum(g.actual_cost) FILTER (WHERE o.state NOT IN ('settled','released')),0)::text,
 COALESCE(sum(g.actual_cost) FILTER (WHERE o.state='released'),0)::text`+
		gatewayBillingUsageFrom+` GROUP BY 1 ORDER BY 1 DESC`, dailyArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day service.GatewayBillingUsageDay
		if err = rows.Scan(&day.Date, &day.RequestCount, &day.Credits, &day.SettledCredits, &day.PendingCredits, &day.WaivedCredits); err != nil {
			_ = rows.Close()
			return nil, err
		}
		page.Daily = append(page.Daily, day)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return page, nil
}
