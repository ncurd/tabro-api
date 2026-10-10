//go:build billing_integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func seedGatewayBillingUsageQuery(t *testing.T, db *sql.DB, operation, issuer, subject, tenant, app, state, credits string, at time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO billing_center_operations(producer_client_id,origin_app_id,operation_id,actor_user_id,tenant_id,request_fingerprint,billing_mode,owner_epoch,state)
 VALUES('gateway-billing',$1,$2,$3,$4,$5,'central',1,$6)`, app, operation, subject, tenant, strings.Repeat("a", 64), state)
	require.NoError(t, err)
	// Every fixture deliberately shares local user, key and supplier IDs. They
	// cannot grant access across verified identities, workspaces or applications.
	_, err = db.Exec(`INSERT INTO gateway_usage_ledger(request_id,api_key_id,request_fingerprint,user_id,account_id,
 oidc_issuer,oidc_subject,oidc_tenant,model,requested_model,billing_mode,input_tokens,output_tokens,actual_cost,rate_multiplier,
 billing_producer_client_id,billing_origin_app_id,billing_operation_id,created_at)
 VALUES($1,3,$2,1,2,$3,$4,$5,'upstream-private-model','gpt-6-sol','central',100,20,$6,10,
 'gateway-billing',$7,$1,$8)`, operation, strings.Repeat("b", 64), issuer, subject, tenant, credits, app, at)
	require.NoError(t, err)
}

func TestBillingCenterUsageDetailsIsolateVerifiedIdentityAndGroupLocalCalendarDays(t *testing.T) {
	_, db := billingCenterTestRepository(t)
	gatewayBillingUsageSchema(t, db)
	ctx := context.Background()
	beforeMidnight := time.Date(2026, 10, 9, 15, 59, 59, 0, time.UTC)
	afterMidnight := beforeMidnight.Add(time.Second)
	seedGatewayBillingUsageQuery(t, db, "own-yesterday", "https://auth.example", "user-a", "tenant-a", "app-a", "settled", "0.1234567891", beforeMidnight)
	seedGatewayBillingUsageQuery(t, db, "own-today", "https://auth.example", "user-a", "tenant-a", "app-a", "settlement_pending", "0.0000000001", afterMidnight)
	seedGatewayBillingUsageQuery(t, db, "other-user", "https://auth.example", "user-b", "tenant-a", "app-a", "settled", "999", afterMidnight)
	seedGatewayBillingUsageQuery(t, db, "other-tenant", "https://auth.example", "user-a", "tenant-b", "app-a", "settled", "999", afterMidnight)
	seedGatewayBillingUsageQuery(t, db, "other-app", "https://auth.example", "user-a", "tenant-a", "app-b", "settled", "999", afterMidnight)
	seedGatewayBillingUsageQuery(t, db, "other-issuer", "https://other-auth.example", "user-a", "tenant-a", "app-a", "settled", "999", afterMidnight)
	q := service.GatewayBillingUsageQuery{Identity: service.GatewayBillingUsageIdentity{Issuer: "https://auth.example", Subject: "user-a", Tenant: "tenant-a", AppID: "app-a"},
		Start: time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC), Timezone: "Asia/Shanghai", Page: 1, PageSize: 1}
	repo := NewGatewayBillingUsageRepository(db)
	page, err := repo.ListGatewayBillingUsage(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "own-today", page.Items[0].OperationID)
	require.Equal(t, "gpt-6-sol", page.Items[0].Model)
	require.Equal(t, bc.Decimal("0.0000000001"), page.Items[0].Credits)
	require.Equal(t, bc.Decimal("10.0000"), page.Items[0].RateMultiplier)
	require.Nil(t, page.Items[0].SettledAt, "pending charges have no payment confirmation time")
	require.Len(t, page.Daily, 2)
	require.Equal(t, "2026-10-10", page.Daily[0].Date)
	require.Equal(t, bc.Decimal("0.0000000001"), page.Daily[0].PendingCredits)
	require.Equal(t, bc.Decimal("0"), page.Daily[0].SettledCredits)
	require.Equal(t, "2026-10-09", page.Daily[1].Date)
	require.Equal(t, bc.Decimal("0.1234567891"), page.Daily[1].SettledCredits)
	require.EqualValues(t, 1, page.Daily[1].RequestCount)
	q.Page = 2
	page, err = repo.ListGatewayBillingUsage(ctx, q)
	require.NoError(t, err)
	require.Equal(t, "own-yesterday", page.Items[0].OperationID)
	require.NotNil(t, page.Items[0].SettledAt, "paid charges report the API payment confirmation time")
	require.Len(t, page.Daily, 2, "daily totals must not depend on the selected item page")
	q.Page = 1
	q.OperationID = "other-tenant"
	page, err = repo.ListGatewayBillingUsage(ctx, q)
	require.NoError(t, err)
	require.Zero(t, page.Total)
	require.Empty(t, page.Items)
	require.Empty(t, page.Daily)
	q.OperationID = "own-today"
	page, err = repo.ListGatewayBillingUsage(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.Daily, 1)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_outbox`).Scan(&count))
	require.Zero(t, count, "a financial read never creates a bill or dispatches a supplier")
}

func TestBillingCenterUsageDetailMigrationOnlyBackfillsUnambiguousMeasuredEvidence(t *testing.T) {
	_, db := billingCenterTestRepository(t)
	// Run the pre-detail schema so the migration can be tested on historical data.
	for _, name := range []string{"114_add_gateway_usage_ledger.sql", "115_add_gateway_upstream_request_id.sql"} {
		data, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.Exec(string(data))
		require.NoError(t, err)
	}
	for _, op := range []string{"known", "wrong-amount", "ambiguous-a", "ambiguous-b"} {
		_, err := db.Exec(`INSERT INTO billing_center_operations(producer_client_id,origin_app_id,operation_id,actor_user_id,tenant_id,request_fingerprint,billing_mode,owner_epoch)
 VALUES('gateway-billing','app-a',$1,'user-a','tenant-a',$2,'central',1)`, op, strings.Repeat("a", 64))
		require.NoError(t, err)
		evidence := op
		amount := "0.5"
		if op == "wrong-amount" {
			amount = "0.6"
		}
		if strings.HasPrefix(op, "ambiguous") {
			evidence = "ambiguous"
		}
		_, err = db.Exec(`INSERT INTO billing_center_outbox(producer_client_id,origin_app_id,operation_id,event_id,kind,reservation_id,payload,payload_fingerprint)
 VALUES('gateway-billing','app-a',$1,$1,'settle','reservation',jsonb_build_object('evidence_reference',$2::text,'usage',jsonb_build_object('credit_amount',$3::text)),$4)`, op, "gateway_usage_ledger:"+evidence, amount, strings.Repeat("c", 64))
		require.NoError(t, err)
	}
	for _, request := range []string{"known", "wrong-amount", "ambiguous", "no-evidence"} {
		_, err := db.Exec(`INSERT INTO gateway_usage_ledger(request_id,api_key_id,request_fingerprint,user_id,account_id,oidc_issuer,oidc_subject,oidc_tenant,model,requested_model,billing_mode,actual_cost)
 VALUES($1,1,$2,1,2,'https://auth.example','user-a','tenant-a','gpt-6-sol','gpt-6-sol','central',0.5)`, request, strings.Repeat("b", 64))
		require.NoError(t, err)
	}
	data, err := migrations.FS.ReadFile("132_gateway_billing_usage_identity.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(data))
	require.NoError(t, err)
	_, err = db.Exec(string(data))
	require.NoError(t, err, "the association migration is idempotent")
	var mapped int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM gateway_usage_ledger WHERE billing_operation_id IS NOT NULL`).Scan(&mapped))
	require.Equal(t, 1, mapped)
	var operation string
	require.NoError(t, db.QueryRow(`SELECT billing_operation_id FROM gateway_usage_ledger WHERE request_id='known'`).Scan(&operation))
	require.Equal(t, "known", operation)
}

func TestBillingCenterUsageDetailsPreserveFrozenMultiplierAndSeparateWaivedConsumption(t *testing.T) {
	_, db := billingCenterTestRepository(t)
	gatewayBillingUsageSchema(t, db)
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	seedGatewayBillingUsageQuery(t, db, "settled", "https://auth.example", "user-a", "tenant-a", "app-a", "settled", "1.25", at)
	seedGatewayBillingUsageQuery(t, db, "pending", "https://auth.example", "user-a", "tenant-a", "app-a", "settlement_pending", "0.5", at.Add(time.Second))
	seedGatewayBillingUsageQuery(t, db, "waived", "https://auth.example", "user-a", "tenant-a", "app-a", "released", "2.75", at.Add(2*time.Second))
	_, err := db.Exec(`UPDATE billing_center_operations SET gateway_pricing_snapshot=$2::jsonb WHERE operation_id=$1`,
		"settled", `{"rate_multiplier":1.2345678901234567}`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE billing_center_operations SET gateway_pricing_snapshot=$2::jsonb WHERE operation_id=$1`,
		"waived", `{"rate_multiplier":"1e-7"}`)
	require.NoError(t, err)
	q := service.GatewayBillingUsageQuery{Identity: service.GatewayBillingUsageIdentity{Issuer: "https://auth.example", Subject: "user-a", Tenant: "tenant-a", AppID: "app-a"},
		Start: at.Add(-time.Hour), End: at.Add(time.Hour), Timezone: "Asia/Shanghai", Page: 1, PageSize: 50}
	page, err := NewGatewayBillingUsageRepository(db).ListGatewayBillingUsage(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	require.Equal(t, bc.Decimal("0.0000001"), page.Items[0].RateMultiplier, "scientific notation is normalized without a float conversion")
	require.Equal(t, bc.Decimal("10.0000"), page.Items[1].RateMultiplier, "historical rows without a snapshot retain the ledger rate")
	require.Equal(t, bc.Decimal("1.2345678901234567"), page.Items[2].RateMultiplier, "snapshot precision must not be truncated to the ledger's four decimal places")
	require.Len(t, page.Daily, 1)
	day := page.Daily[0]
	require.EqualValues(t, 3, day.RequestCount)
	require.Equal(t, bc.Decimal("4.5000000000"), day.Credits)
	require.Equal(t, bc.Decimal("1.2500000000"), day.SettledCredits)
	require.Equal(t, bc.Decimal("0.5000000000"), day.PendingCredits)
	require.Equal(t, bc.Decimal("2.7500000000"), day.WaivedCredits, "released consumption must never be shown as awaiting a future charge")
	encoded, err := json.Marshal(page)
	require.NoError(t, err, "all amounts must satisfy the billing decimal-string contract")
	require.Contains(t, string(encoded), `"rate_multiplier":"1.2345678901234567"`)
	require.Contains(t, string(encoded), `"waived_credits":"2.7500000000"`)
}
