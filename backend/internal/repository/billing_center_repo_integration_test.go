//go:build billing_integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Run against an isolated local database only:
// BILLING_CENTER_TEST_DSN=postgres://.../billing_center_test?sslmode=disable
// go test -tags=billing_integration ./internal/repository -run TestBillingCenter
func billingCenterTestRepository(t *testing.T) (*BillingCenterRepository, *sql.DB) {
	t.Helper()
	raw := os.Getenv("BILLING_CENTER_TEST_DSN")
	if raw == "" {
		t.Skip("BILLING_CENTER_TEST_DSN is not configured")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "billing_center_test", strings.TrimPrefix(u.Path, "/"), "use a dedicated test database")
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname())
	admin, err := sql.Open("postgres", raw)
	require.NoError(t, err)
	schema := "billing_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	migration, err := migrations.FS.ReadFile("118_billing_center_operations.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	// The migration must be rerunnable.
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	resolutionMigration, err := migrations.FS.ReadFile("126_billing_center_reconciliation_receipts.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(resolutionMigration))
	require.NoError(t, err)
	scheduleMigration, err := migrations.FS.ReadFile("129_billing_center_daily_settlement.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(scheduleMigration))
	require.NoError(t, err)
	_, err = db.Exec(string(scheduleMigration))
	require.NoError(t, err)
	return NewBillingCenterRepository(db, "gateway-billing"), db
}

func billingCenterTestOperation(t *testing.T, repo *BillingCenterRepository, deadlines ...time.Time) bc.Operation {
	t.Helper()
	ctx := context.Background()
	key := bc.Key{ProducerClientID: "gateway-billing", OriginAppID: "agent", OperationID: uuid.NewString()}
	request := bc.ReserveRequest{OperationID: key.OperationID, BillingAccountID: "payer", BalanceID: "balance", PriceVersionID: "price", OwnerEpoch: 7, ServiceTier: "default", MaximumUsage: map[string]bc.Decimal{"input": "100000"}}
	intent, err := bc.NewIntent(key, "user", "tenant", "central", request)
	if len(deadlines) == 1 {
		intent, err = bc.NewScheduledIntent(key, "user", "tenant", "central", request, deadlines[0])
	}
	require.NoError(t, err)
	op, created, err := repo.CreateIntent(ctx, intent)
	require.NoError(t, err)
	require.True(t, created)
	remote := billingCenterTestRemote(op, "reserved", 1)
	require.NoError(t, repo.BindReservation(ctx, key, op.Version, remote))
	op, err = repo.Get(ctx, key)
	require.NoError(t, err)
	return op
}
func billingCenterTestRemote(op bc.Operation, state string, version int64) bc.Reservation {
	id := op.ReservationID
	if id == "" {
		id = "reservation-" + op.OperationID
	}
	return bc.Reservation{OperationID: op.OperationID, ReservationID: id, State: state, Version: version, BillingAccountID: "payer", OwnerEpoch: 7, WalletUnit: "credit", ReservedAmount: "1.25", SettledAmount: "0.75", PriceVersionID: "price", ProductVersion: "product", BalanceID: "balance", PeriodIDs: []string{"period"}, ExpiresAt: time.Now().Add(time.Hour).UTC()}
}
func billingCenterTestDispatched(t *testing.T, repo *BillingCenterRepository, deadlines ...time.Time) bc.Operation {
	t.Helper()
	op := billingCenterTestOperation(t, repo, deadlines...)
	ctx := context.Background()
	claimed, err := repo.ClaimDispatch(ctx, op.Key, op.Version, "attempt")
	require.NoError(t, err)
	require.True(t, claimed)
	permit, err := repo.ConfirmDispatched(ctx, op.Key, op.Version+1, "attempt", billingCenterTestRemote(op, "dispatched", 2))
	require.NoError(t, err)
	require.True(t, permit)
	op, err = repo.Get(ctx, op.Key)
	require.NoError(t, err)
	return op
}
func billingCenterTestSettlement(t *testing.T, op bc.Operation) bc.Event {
	t.Helper()
	event, err := bc.NewSettlement(op.Key, op.ReservationID, bc.SettleRequest{TransitionRequest: bc.TransitionRequest{EventID: "event-" + op.OperationID, ExpectedVersion: op.RemoteVersion}, UsageItemID: "usage", Usage: map[string]bc.Decimal{"input": "100"}, EvidenceReference: "provider_usage:1", UsageComplete: true})
	require.NoError(t, err)
	return event
}

func TestBillingCenterDispatchCASIsSingleUseAndNeverReclaimsUncertainAttempt(t *testing.T) {
	repo, _ := billingCenterTestRepository(t)
	op := billingCenterTestOperation(t, repo)
	ctx := context.Background()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.ClaimDispatch(ctx, op.Key, op.Version, "attempt")
			require.NoError(t, err)
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	current, err := repo.Get(ctx, op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.Dispatching, current.State)
	ok, err := repo.ClaimDispatch(ctx, op.Key, current.Version, "replacement")
	require.NoError(t, err)
	require.False(t, ok)
	permit, err := repo.ConfirmDispatched(ctx, op.Key, current.Version, "attempt", billingCenterTestRemote(op, "dispatched", 2))
	require.NoError(t, err)
	require.True(t, permit)
	permit, err = repo.ConfirmDispatched(ctx, op.Key, current.Version, "attempt", billingCenterTestRemote(op, "dispatched", 2))
	require.NoError(t, err)
	require.False(t, permit)
}

func TestBillingCenterOutboxJoinsProviderTransactionAndDeduplicatesConcurrentUsage(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	op := billingCenterTestDispatched(t, repo)
	event := billingCenterTestSettlement(t, op)
	_, err := db.Exec(`CREATE TABLE provider_effects (event_id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	inserted, err := repo.EnqueueTx(ctx, tx, event)
	require.NoError(t, err)
	require.True(t, inserted)
	_, err = tx.Exec(`INSERT INTO provider_effects VALUES($1)`, event.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM billing_center_outbox`).Scan(&count))
	require.Zero(t, count)
	opAfter, err := repo.Get(ctx, op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.Dispatched, opAfter.State)
	var effects atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer tx.Rollback()
			inserted, err := repo.EnqueueTx(ctx, tx, event)
			require.NoError(t, err)
			if inserted {
				_, err = tx.Exec(`INSERT INTO provider_effects VALUES($1)`, event.ID)
				require.NoError(t, err)
				effects.Add(1)
			}
			require.NoError(t, tx.Commit())
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, effects.Load())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM provider_effects`).Scan(&count))
	require.Equal(t, 1, count)
	changed := event
	changed.Body = []byte(strings.Replace(string(event.Body), `"100"`, `"101"`, 1))
	_, err = repo.Enqueue(ctx, changed)
	require.ErrorIs(t, err, bc.ErrConflict)
	changed = event
	changed.ID = "different-event"
	changed.Body = []byte(strings.Replace(string(event.Body), event.ID, changed.ID, 1))
	_, err = repo.Enqueue(ctx, changed)
	require.ErrorIs(t, err, bc.ErrConflict)
}

func TestBillingCenterLeaseRecoveryFencesOldWorkerAndPreservesCommittedResult(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	op := billingCenterTestDispatched(t, repo)
	event := billingCenterTestSettlement(t, op)
	_, err := repo.Enqueue(ctx, event)
	require.NoError(t, err)
	first, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, first, 1)
	empty, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, empty)
	_, err = db.Exec(`UPDATE billing_center_outbox SET lease_until=NOW()-INTERVAL '1 second'`)
	require.NoError(t, err)
	recovered, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.NotEqual(t, first[0].LeaseToken, recovered[0].LeaseToken)
	require.Equal(t, 2, recovered[0].Attempts)
	remote := billingCenterTestRemote(op, "settled", 3)
	require.ErrorIs(t, repo.Complete(ctx, first[0], remote), bc.ErrLeaseLost)
	require.ErrorIs(t, repo.Retry(ctx, first[0], 0, "stale", false), bc.ErrLeaseLost)
	require.NoError(t, repo.Complete(ctx, recovered[0], remote))
	final, err := repo.Get(ctx, op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.Settled, final.State)
	empty, err = repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, empty)
	// Replay after local acknowledgement also observes the same immutable event.
	inserted, err := repo.Enqueue(ctx, event)
	require.NoError(t, err)
	require.False(t, inserted)
}

func TestBillingCenterPermanentFailureIsRetainedForReconciliationAndProducerIsolated(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	op := billingCenterTestDispatched(t, repo)
	_, err := repo.Enqueue(ctx, billingCenterTestSettlement(t, op))
	require.NoError(t, err)
	other := NewBillingCenterRepository(db, "different-producer")
	none, err := other.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, none)
	events, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, repo.Retry(ctx, events[0], 0, "delivery_rejected", true))
	op, err = repo.Get(ctx, op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.ReconciliationRequired, op.State)
	none, err = repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, none)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM billing_center_outbox`).Scan(&status))
	require.Equal(t, "blocked", status)
}
