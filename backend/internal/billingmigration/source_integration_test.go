//go:build billing_integration

package billingmigration

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func sourceFixture(t *testing.T) (Source, *sql.DB) {
	t.Helper()
	raw := os.Getenv("BILLING_CENTER_TEST_DSN")
	if raw == "" {
		t.Skip("BILLING_CENTER_TEST_DSN required")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "billing_center_test", strings.TrimPrefix(u.Path, "/"))
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, u.Hostname())
	admin, err := sql.Open("postgres", raw)
	require.NoError(t, err)
	schema := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	for _, name := range []string{"001_init.sql", "003_subscription.sql", "005_schema_parity.sql", "012_add_user_subscription_soft_delete.sql", "045_add_api_key_quota.sql", "047_add_user_group_rate_multipliers.sql", "056_add_api_key_last_used_at.sql", "064_add_api_key_rate_limits.sql", "081_create_channels.sql", "082_refactor_channel_pricing.sql", "092_payment_orders.sql", "101_add_balance_notify_fields.sql", "102_add_balance_notify_threshold_type.sql", "109_add_oidc_managed_api_key.sql", "112_bind_gateway_oidc_identity.sql", "114_add_gateway_usage_ledger.sql", "117_media_generation_billing.sql", "118_billing_center_operations.sql", "119_billing_center_account_routes.sql", "120_billing_center_financial_fence.sql", "121_billing_center_workspace_routes.sql", "122_billing_center_credential_bindings.sql", "124_billing_center_operator_audit.sql", "125_billing_center_redeem_fence.sql"} {
		b, e := migrations.FS.ReadFile(name)
		require.NoError(t, e)
		_, e = db.Exec(string(b))
		require.NoError(t, e, name)
	}
	_, err = db.Exec(`CREATE TABLE billing_usage_entries(user_id bigint,applied boolean);
 INSERT INTO users(id,email,password_hash,balance,total_recharged) VALUES(1,'source@example.test','unused',-12.12345678,99.12345678),(2,'other@example.test','unused',3,3);
 INSERT INTO groups(id,name,daily_limit_usd,weekly_limit_usd,monthly_limit_usd) VALUES(1,'existing',10,50,150);
 INSERT INTO api_keys(id,user_id,key,name,quota,quota_used,usage_5h,window_5h_start) VALUES(1,1,'not-exported-secret','existing',20,1.00000001,0.00000001,'2026-09-23T00:00:00Z');
 INSERT INTO user_subscriptions(id,user_id,group_id,starts_at,expires_at,daily_window_start,weekly_window_start,monthly_window_start,daily_usage_usd,weekly_usage_usd,monthly_usage_usd) VALUES(1,1,1,'2026-09-01Z','2027-01-01Z','2026-09-22T16:00:00Z','2026-09-20T16:00:00Z','2026-09-01T16:00:00Z',0.1234567891,5,10);
 INSERT INTO payment_orders(id,user_id,amount,pay_amount,refund_amount,status,order_type,expires_at,completed_at) VALUES(1,1,10,7,2,'PARTIALLY_REFUNDED','balance','2026-09-22Z','2026-09-21Z'),(2,1,99,89,0,'COMPLETED','subscription','2026-09-22Z','2026-09-21Z');
 INSERT INTO channels(id,name) VALUES(1,'policy');
 INSERT INTO channel_groups(channel_id,group_id) VALUES(1,1);
 INSERT INTO channel_model_pricing(id,channel_id,models,input_price) VALUES(1,1,'["model"]',0.000000123456);
 INSERT INTO user_group_rate_multipliers(user_id,group_id,rate_multiplier) VALUES(1,1,1.1234);
 INSERT INTO redeem_codes(id,code,type,value,status,used_by) VALUES(1,'not-exported-code','balance',5,'used',1);`)
	require.NoError(t, err)
	return Source{db}, db
}
func fixtureMapping() Mapping {
	return Mapping{LocalUserID: 1, Issuer: "https://auth.example.test/", ActorUserID: "actor-1", TenantID: "personal-1", OriginAppID: "app", BillingAccountID: "account-1", OwnerEpoch: 4, IdentityEvidence: "reviewed-source-identity-2026-09"}
}
func fixtureDrainEvidence() DrainEvidence {
	return DrainEvidence{true, true, true, true, "reviewed-stopped-workers-and-empty-inflight"}
}

func TestSourceFreezeExportAndActivatePreserveExactAmountsAndFenceAllLegacyWriters(t *testing.T) {
	s, db := sourceFixture(t)
	ctx := context.Background()
	m := fixtureMapping()
	require.NoError(t, s.Map(ctx, m))
	require.NoError(t, s.Map(ctx, m))
	_, err := db.Exec(`UPDATE billing_center_account_routes SET billing_mode='central' WHERE local_user_id=1`)
	require.Error(t, err)
	require.NoError(t, s.Drain(ctx, 1, "batch-1", "reviewed"))
	require.Error(t, s.Freeze(ctx, 1, "batch-1", DrainEvidence{}))
	require.NoError(t, s.Freeze(ctx, 1, "batch-1", fixtureDrainEvidence()))
	require.NoError(t, s.Freeze(ctx, 1, "batch-1", fixtureDrainEvidence()))
	queries := []string{
		`UPDATE users SET balance=balance+1 WHERE id=1`,
		`UPDATE users SET total_recharged=total_recharged+1 WHERE id=1`,
		`UPDATE user_subscriptions SET daily_usage_usd=0 WHERE id=1`,
		`UPDATE user_subscriptions SET expires_at=expires_at+interval '1 day' WHERE id=1`,
		`DELETE FROM user_subscriptions WHERE id=1`,
		`UPDATE api_keys SET quota_used=0 WHERE id=1`,
		`UPDATE api_keys SET usage_5h=0,window_5h_start=NOW() WHERE id=1`,
		`UPDATE api_keys SET user_id=2 WHERE id=1`,
		`UPDATE payment_orders SET refund_amount=3 WHERE id=1`,
		`INSERT INTO payment_orders(user_id,amount,pay_amount,expires_at) VALUES(1,1,1,NOW())`,
		`UPDATE redeem_codes SET value=6 WHERE id=1`,
		`INSERT INTO redeem_codes(code,type,value,status,used_by) VALUES('new','balance',1,'used',1)`,
		`UPDATE groups SET daily_limit_usd=999 WHERE id=1`,
		`UPDATE channel_model_pricing SET input_price=9 WHERE id=1`,
		`UPDATE user_group_rate_multipliers SET rate_multiplier=2 WHERE user_id=1`,
		`DELETE FROM channel_groups WHERE channel_id=1`,
		`INSERT INTO channel_pricing_intervals(pricing_id,input_price) VALUES(1,0.5)`,
		`UPDATE billing_center_account_routes SET billing_mode='local' WHERE local_user_id=1`,
		`DELETE FROM billing_center_account_routes WHERE local_user_id=1`,
	}
	for _, query := range queries {
		_, err = db.Exec(query)
		require.Error(t, err, query)
	}
	_, err = db.Exec(`UPDATE users SET username='profile edit' WHERE id=1; UPDATE api_keys SET last_used_at=NOW(),status='disabled' WHERE id=1; UPDATE users SET balance=balance+1 WHERE id=2`)
	require.NoError(t, err)
	snapshot, b, hash, err := s.Export(ctx, 1, "batch-1", "Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, "-12.12345678", snapshot.Balance)
	require.Equal(t, "99.12345678", snapshot.TotalRecharged)
	require.Contains(t, string(b), `"daily_usage":"0.1234567891"`)
	require.NotContains(t, string(b), "not-exported")
	require.NotContains(t, string(b), "source@example")
	_, second, hash2, err := s.Export(ctx, 1, "batch-1", "Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, b, second)
	require.Equal(t, hash, hash2)
	receipt := TargetReceipt{"target-receipt", "batch-1", hash, "account-1", 5, true}
	require.Error(t, s.Activate(ctx, 1, receipt))
	require.NoError(t, s.RecordExport(ctx, snapshot, hash))
	require.NoError(t, s.RecordExport(ctx, snapshot, hash))
	require.NoError(t, s.Activate(ctx, 1, receipt))
	require.NoError(t, s.Activate(ctx, 1, receipt))
	_, err = db.Exec(`UPDATE users SET balance=999 WHERE id=1`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE billing_center_migration_audit SET evidence='{}'`)
	require.Error(t, err)
	var mode string
	var epoch int64
	require.NoError(t, db.QueryRow(`SELECT billing_mode,owner_epoch FROM billing_center_account_routes WHERE local_user_id=1`).Scan(&mode, &epoch))
	require.Equal(t, "central", mode)
	require.EqualValues(t, 5, epoch)
}

func TestSourceFreezeWaitsForOldTransactionAndRejectsPendingResponsibilities(t *testing.T) {
	s, db := sourceFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Map(ctx, fixtureMapping()))
	require.NoError(t, s.Drain(ctx, 1, "batch-race", "reviewed"))
	_, err := db.Exec(`UPDATE payment_orders SET status='REFUNDING' WHERE id=1`)
	require.NoError(t, err)
	require.ErrorContains(t, s.Freeze(ctx, 1, "batch-race", fixtureDrainEvidence()), "responsibilities")
	_, err = db.Exec(`UPDATE payment_orders SET status='PARTIALLY_REFUNDED' WHERE id=1`)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE users SET balance=-13.12345678 WHERE id=1`)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- s.Freeze(ctx, 1, "batch-race", fixtureDrainEvidence()) }()
	select {
	case err := <-done:
		t.Fatalf("freeze passed an uncommitted legacy writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	// Serializable PostgreSQL may abort the first snapshot after waiting. A new
	// invocation safely resumes the same batch with no financial duplication.
	if err = <-done; err != nil {
		require.NoError(t, s.Freeze(ctx, 1, "batch-race", fixtureDrainEvidence()))
	}
	snapshot, _, _, err := s.Export(ctx, 1, "batch-race", "Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, "-13.12345678", snapshot.Balance)
}

func TestLegacyRepeatableReadSnapshotCannotBypassNewlyInstalledOrFrozenAuthority(t *testing.T) {
	for _, beforeMap := range []bool{true, false} {
		t.Run(map[bool]string{true: "new_authority", false: "frozen_authority"}[beforeMap], func(t *testing.T) {
			s, db := sourceFixture(t)
			ctx := context.Background()
			if !beforeMap {
				require.NoError(t, s.Map(ctx, fixtureMapping()))
			}
			old, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			require.NoError(t, err)
			defer old.Rollback()
			var previous string
			require.NoError(t, old.QueryRow(`SELECT balance::text FROM users WHERE id=1`).Scan(&previous))
			if beforeMap {
				require.NoError(t, s.Map(ctx, fixtureMapping()))
			}
			require.NoError(t, s.Drain(ctx, 1, "snapshot-race", "reviewed"))
			require.NoError(t, s.Freeze(ctx, 1, "snapshot-race", fixtureDrainEvidence()))
			_, err = old.Exec(`UPDATE user_subscriptions SET daily_usage_usd=daily_usage_usd+1 WHERE id=1`)
			require.Error(t, err, "an old snapshot may not overwrite the frozen export")
		})
	}
}

func TestRedeemTransferFencesEveryLegacyConsumerAndSurvivesRepeatedExport(t *testing.T) {
	s, db := sourceFixture(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO redeem_codes(id,code,type,value,status) VALUES(22,'test-private-unredeemed','balance',4.12345678,'unused')`)
	require.NoError(t, err)
	old, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	require.NoError(t, err)
	defer old.Rollback()
	var previous string
	require.NoError(t, old.QueryRow(`SELECT status FROM redeem_codes WHERE id=22`).Scan(&previous))
	data, hash, err := s.ExportRedeem(ctx, 22, "transfer-22", "operator-review")
	require.NoError(t, err)
	require.Contains(t, string(data), `"value":"4.12345678"`)
	repeat, hash2, err := s.ExportRedeem(ctx, 22, "transfer-22", "operator-review")
	require.NoError(t, err)
	require.Equal(t, data, repeat)
	require.Equal(t, hash, hash2)
	_, err = old.Exec(`UPDATE redeem_codes SET used_by=2,status='used' WHERE id=22`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE redeem_codes SET used_by=2,status='used' WHERE id=22`)
	require.Error(t, err)
	_, err = db.Exec(`DELETE FROM billing_center_redeem_authority WHERE source_code_id=22`)
	require.Error(t, err)
	_, _, err = s.ExportRedeem(ctx, 22, "different-transfer", "operator-review")
	require.Error(t, err)
	var evidence string
	require.NoError(t, db.QueryRow(`SELECT evidence::text FROM billing_center_redeem_authority WHERE source_code_id=22`).Scan(&evidence))
	require.NotContains(t, evidence, "test-private-unredeemed")
}
func TestOperatorRegistrationsRequireMatchingOwnershipAndImmutableReplays(t *testing.T) {
	s, _ := sourceFixture(t)
	ctx := context.Background()
	m := fixtureMapping()
	require.NoError(t, s.Map(ctx, m))
	require.NoError(t, s.Drain(ctx, 1, "bindings", "review"))
	require.NoError(t, s.Freeze(ctx, 1, "bindings", fixtureDrainEvidence()))
	workspace := WorkspaceRegistration{EventID: "workspace", Issuer: m.Issuer, TenantID: m.TenantID, AccountID: m.BillingAccountID, OwnerEpoch: 5, Mode: "central", TargetReceipt: "auth-receipt", OperatorReference: "review"}
	require.Error(t, s.RegisterWorkspace(ctx, workspace))
	binding := CredentialRegistration{EventID: "binding", APIKeyID: 1, LocalUserID: 1, BindingID: "registry", Version: 1, Issuer: m.Issuer, ActorUserID: m.ActorUserID, TenantID: m.TenantID, Enabled: true, TargetReceipt: "registry-receipt", OperatorReference: "review"}
	require.NoError(t, s.RegisterCredential(ctx, binding))
	require.NoError(t, s.RegisterCredential(ctx, binding))
	bad := binding
	bad.Version = 2
	require.Error(t, s.RegisterCredential(ctx, bad))
	snap, _, hash, err := s.Export(ctx, 1, "bindings", "UTC")
	require.NoError(t, err)
	require.NoError(t, s.RecordExport(ctx, snap, hash))
	require.NoError(t, s.Activate(ctx, 1, TargetReceipt{"auth-receipt", "bindings", hash, m.BillingAccountID, 5, true}))
	require.NoError(t, s.RegisterWorkspace(ctx, workspace))
	require.NoError(t, s.RegisterWorkspace(ctx, workspace))
	badWorkspace := workspace
	badWorkspace.EventID = "replace"
	badWorkspace.AccountID = "other"
	badWorkspace.ExpectedEpoch = 5
	require.Error(t, s.RegisterWorkspace(ctx, badWorkspace))
}
