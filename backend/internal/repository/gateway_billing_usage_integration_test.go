//go:build billing_integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func gatewayBillingUsageSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE users(id bigint PRIMARY KEY,balance numeric NOT NULL DEFAULT 100,updated_at timestamptz DEFAULT NOW(),deleted_at timestamptz);
 CREATE TABLE accounts(id bigint PRIMARY KEY,extra jsonb NOT NULL DEFAULT '{}',updated_at timestamptz DEFAULT NOW(),deleted_at timestamptz);
 INSERT INTO users(id) VALUES(1);INSERT INTO accounts(id) VALUES(2);`)
	require.NoError(t, err)
	for _, name := range []string{"071_add_usage_billing_dedup.sql", "073_add_usage_billing_dedup_archive.sql", "114_add_gateway_usage_ledger.sql", "115_add_gateway_upstream_request_id.sql", "119_billing_center_account_routes.sql", "121_billing_center_workspace_routes.sql"} {
		data, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.Exec(string(data))
		require.NoError(t, err)
	}
}

func TestBillingCenterActualUsageTransactionIsAtomicAndNeverDebitsCustomer(t *testing.T) {
	center, db := billingCenterTestRepository(t)
	gatewayBillingUsageSchema(t, db)
	op := billingCenterTestDispatched(t, center)
	event := billingCenterTestSettlement(t, op)
	usageRepo := NewUsageBillingRepository(nil, db)
	command := service.UsageBillingCommand{RequestID: op.OperationID, APIKeyID: 3, UserID: 1, AccountID: 2, AccountType: service.AccountTypeAPIKey, Model: "gpt-6-sol", RequestedModel: "gpt-6-sol", BillingMode: "central", InputTokens: 100, OutputTokens: 20, AccountQuotaCost: 1.25, CentralEvent: &event}
	// Force evidence persistence failure after enqueue and supplier quota update.
	_, err := db.Exec(`ALTER TABLE gateway_usage_ledger ADD CONSTRAINT reject_test_evidence CHECK(input_tokens<0)`)
	require.NoError(t, err)
	_, err = usageRepo.Apply(context.Background(), &command)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_outbox`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_billing_dedup`).Scan(&count))
	require.Zero(t, count)
	var quota string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(extra->>'quota_used','0') FROM accounts WHERE id=2`).Scan(&quota))
	require.Equal(t, "0", quota)
	_, err = db.Exec(`ALTER TABLE gateway_usage_ledger DROP CONSTRAINT reject_test_evidence`)
	require.NoError(t, err)
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := command
			result, err := usageRepo.Apply(context.Background(), &cmd)
			require.NoError(t, err)
			if result != nil && result.Applied {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, applied.Load())
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=1`).Scan(&balance))
	require.Equal(t, "100", balance)
	require.NoError(t, db.QueryRow(`SELECT extra->>'quota_used' FROM accounts WHERE id=2`).Scan(&quota))
	require.Equal(t, "1.25", quota)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM gateway_usage_ledger WHERE balance_cost=0 AND subscription_cost=0 AND api_key_quota_cost=0 AND api_key_rate_limit_cost=0`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_outbox WHERE status='pending'`).Scan(&count))
	require.Equal(t, 1, count)
	stored, err := center.Get(context.Background(), op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.SettlementPending, stored.State)
}

func TestBillingCenterRoutesAndShadowKeepFrozenEstimateWithoutCenterCharge(t *testing.T) {
	center, db := billingCenterTestRepository(t)
	gatewayBillingUsageSchema(t, db)
	_, err := db.Exec(`INSERT INTO billing_center_account_routes(local_user_id,billing_mode,oidc_issuer,actor_user_id,tenant_id,origin_app_id,billing_account_id,owner_epoch)
 VALUES(1,'shadow','https://auth.example','actor','tenant','agent','payer',7)`)
	require.NoError(t, err)
	route, err := center.GetBillingRoute(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "shadow", route.Mode)
	key := bc.Key{ProducerClientID: "gateway-billing", OriginAppID: "agent", OperationID: "shadow-op"}
	execution := &bc.Execution{Key: key, Mode: "shadow", ProductKey: "ai:gpt-6-sol", RequestPayloadHash: "abc", Quote: bc.Quote{QuoteID: "frozen-quote", Request: bc.ReserveRequest{OperationID: key.OperationID, PriceVersionID: "v1"}, EstimatedAmount: "9", WalletUnit: "credit"}}
	require.NoError(t, center.RecordBillingShadow(context.Background(), *route, execution))
	require.NoError(t, center.RecordBillingShadow(context.Background(), *route, execution))
	usageRepo := NewUsageBillingRepository(nil, db)
	cmd := &service.UsageBillingCommand{RequestID: key.OperationID, APIKeyID: 3, UserID: 1, AccountID: 2, AccountType: service.AccountTypeAPIKey, Model: "gpt-6-sol", InputTokens: 3, OutputTokens: 2, BalanceCost: 0.5, ActualCost: 0.5}
	result, err := usageRepo.Apply(bc.WithExecution(context.Background(), execution), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	pending, err := center.PendingBillingShadows(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, bc.Decimal("3"), pending[0].Usage["input_tokens"])
	require.NoError(t, center.CompleteBillingShadow(context.Background(), pending[0], bc.Estimate{Amount: "0.55", WalletUnit: "credit", PriceVersionID: "v1"}))
	pending, err = center.PendingBillingShadows(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_operations`).Scan(&count))
	require.Zero(t, count)
	var actual, estimate string
	require.NoError(t, db.QueryRow(`SELECT local_actual_cost::text,central_estimate->>'amount' FROM billing_center_shadow_observations`).Scan(&actual, &estimate))
	require.Equal(t, "0.5000000000", actual)
	require.Equal(t, "0.55", estimate)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=1`).Scan(&balance))
	require.Equal(t, 99.5, balance)
	var safe map[string]any
	encoded, err := json.Marshal(execution.Quote)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &safe))
	require.NotContains(t, safe, "subject_proof")
}
