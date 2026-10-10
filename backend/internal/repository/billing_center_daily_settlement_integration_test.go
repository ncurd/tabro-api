//go:build billing_integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMeasuredOIDCChargeUpgradeReleasesUnusedHoldWithoutChangingEvidence(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	cutoff := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	key := bc.Key{ProducerClientID: "gateway-billing", OriginAppID: "agent", OperationID: uuid.NewString()}
	request := bc.ReserveRequest{OperationID: key.OperationID, BillingAccountID: "payer", BalanceID: "balance", PriceVersionID: "price", OwnerEpoch: 7, ServiceTier: "default", MaximumUsage: map[string]bc.Decimal{"credit_amount": "200"}}
	intent, err := bc.NewScheduledIntent(key, "user", "tenant", "central", request, cutoff, json.RawMessage(`{"billing_model":"gpt-test","rate_multiplier":10}`))
	require.NoError(t, err)
	measured, _, err := repo.CreateIntent(ctx, intent)
	require.NoError(t, err)
	require.NoError(t, repo.BindReservation(ctx, key, measured.Version, billingCenterTestRemote(measured, "reserved", 1)))
	measured, err = repo.Get(ctx, key)
	require.NoError(t, err)
	claimedDispatch, err := repo.ClaimDispatch(ctx, key, measured.Version, "attempt")
	require.NoError(t, err)
	require.True(t, claimedDispatch)
	permit, err := repo.ConfirmDispatched(ctx, key, measured.Version+1, "attempt", billingCenterTestRemote(measured, "dispatched", 2))
	require.NoError(t, err)
	require.True(t, permit)
	measured, err = repo.Get(ctx, key)
	require.NoError(t, err)
	event, err := bc.NewSettlement(measured.Key, measured.ReservationID, bc.SettleRequest{
		TransitionRequest: bc.TransitionRequest{EventID: "settle:" + measured.OperationID, ExpectedVersion: measured.RemoteVersion},
		UsageItemID:       measured.OperationID, Usage: map[string]bc.Decimal{"credit_amount": "0.125"},
		EvidenceReference: "gateway_usage_ledger:request", UsageComplete: true,
	})
	require.NoError(t, err)
	_, err = repo.Enqueue(ctx, event)
	require.NoError(t, err)
	var queuedAt time.Time
	require.NoError(t, db.QueryRow(`SELECT available_at FROM billing_center_outbox WHERE operation_id=$1`, measured.OperationID).Scan(&queuedAt))
	require.False(t, queuedAt.After(time.Now()), "old in-flight credit usage is due as soon as it is measured")
	// The migration must also accelerate rows queued by the old binary.
	_, err = db.Exec(`UPDATE billing_center_outbox SET available_at=$2 WHERE operation_id=$1`, measured.OperationID, cutoff)
	require.NoError(t, err)
	var expectedFingerprint string
	require.NoError(t, db.QueryRow(`SELECT payload_fingerprint FROM billing_center_outbox WHERE operation_id=$1`, measured.OperationID).Scan(&expectedFingerprint))
	unknown := billingCenterTestDispatched(t, repo, cutoff)
	require.NoError(t, repo.MarkReconciliation(ctx, unknown.Key, unknown.Version))
	otherProduct := billingCenterTestDispatched(t, repo, cutoff)
	_, err = repo.Enqueue(ctx, billingCenterTestSettlement(t, otherProduct))
	require.NoError(t, err)

	migration, err := migrations.FS.ReadFile("131_complete_measured_oidc_charges.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	claimed, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, measured.Key, claimed[0].Key)
	require.JSONEq(t, string(event.Body), string(claimed[0].Body))
	require.Equal(t, expectedFingerprint, claimed[0].Fingerprint)
	unchanged, err := repo.Get(ctx, unknown.Key)
	require.NoError(t, err)
	require.Equal(t, bc.ReconciliationRequired, unchanged.State)
	stored, err := repo.Get(ctx, measured.Key)
	require.NoError(t, err)
	require.Equal(t, measured.RequestFingerprint, stored.RequestFingerprint)
	require.True(t, stored.SettlementNotBefore.Equal(cutoff))
}

func TestBillingCenterDailySettlementDefersUsageButReleasesImmediately(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	cutoff := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	op := billingCenterTestDispatched(t, repo, cutoff)
	event := billingCenterTestSettlement(t, op)
	inserted, err := repo.Enqueue(ctx, event)
	require.NoError(t, err)
	require.True(t, inserted)
	var availableAt time.Time
	require.NoError(t, db.QueryRow(`SELECT available_at FROM billing_center_outbox WHERE operation_id=$1`, op.OperationID).Scan(&availableAt))
	require.True(t, cutoff.Equal(availableAt))

	// A new process reads the same cutoff; replay never makes the bill due early.
	restarted := NewBillingCenterRepository(db, "gateway-billing")
	stored, err := restarted.Get(ctx, op.Key)
	require.NoError(t, err)
	require.True(t, cutoff.Equal(stored.SettlementNotBefore))
	require.Equal(t, bc.SettlementPending, stored.State)
	inserted, err = restarted.Enqueue(ctx, event)
	require.NoError(t, err)
	require.False(t, inserted)
	claimed, err := restarted.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, claimed)

	// Known failed work must free its Auth hold at once, even before the cutoff.
	failed := billingCenterTestOperation(t, repo, cutoff)
	release, err := bc.NewRelease(failed.Key, failed.ReservationID, bc.ReleaseRequest{TransitionRequest: bc.TransitionRequest{EventID: "release:" + failed.OperationID, ExpectedVersion: failed.RemoteVersion}, Reason: "supplier_not_called", ConfirmedNoBillableWork: true})
	require.NoError(t, err)
	_, err = restarted.Enqueue(ctx, release)
	require.NoError(t, err)
	claimed, err = restarted.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, bc.ReleaseEvent, claimed[0].Kind)
	require.Equal(t, failed.Key, claimed[0].Key)
	require.NoError(t, restarted.Complete(ctx, claimed[0], billingCenterTestRemote(failed, "released", 2)))

	// Waiting for the daily cutoff is normal, not a reconciliation backlog.
	_, err = db.Exec(`UPDATE billing_center_operations SET updated_at=NOW()-INTERVAL '1 minute' WHERE operation_id=$1`, op.OperationID)
	require.NoError(t, err)
	pending, err := restarted.PendingReconciliations(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestBillingCenterDailySettlementPastCutoffRetriesSameBillAfterRestart(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	op := billingCenterTestDispatched(t, repo, time.Now().Add(-time.Minute))
	event := billingCenterTestSettlement(t, op)
	_, err := repo.Enqueue(ctx, event)
	require.NoError(t, err)
	claimed, err := repo.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	// An unavailable Auth server leaves the same frozen detail retryable.
	require.NoError(t, repo.Retry(ctx, claimed[0], 0, "delivery_unavailable", false))
	restarted := NewBillingCenterRepository(db, "gateway-billing")
	retry, err := restarted.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, retry, 1)
	require.Equal(t, claimed[0].ID, retry[0].ID)
	require.JSONEq(t, string(claimed[0].Body), string(retry[0].Body))
	require.Equal(t, 2, retry[0].Attempts)
	require.NoError(t, restarted.Complete(ctx, retry[0], billingCenterTestRemote(op, "settled", 3)))
	stored, err := restarted.Get(ctx, op.Key)
	require.NoError(t, err)
	require.Equal(t, bc.Settled, stored.State)
	inserted, err := restarted.Enqueue(ctx, event)
	require.NoError(t, err)
	require.False(t, inserted)
	claimed, err = restarted.Claim(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Empty(t, claimed)
}

func TestBillingCenterDailySettlementPersistsPriceAndRejectsChangedCutoff(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	key := bc.Key{ProducerClientID: "gateway-billing", OriginAppID: "agent", OperationID: uuid.NewString()}
	request := bc.ReserveRequest{OperationID: key.OperationID, BillingAccountID: "payer", BalanceID: "balance", PriceVersionID: "price", OwnerEpoch: 7, ServiceTier: "default", MaximumUsage: map[string]bc.Decimal{"credits": "25"}}
	cutoff := time.Now().Add(time.Hour)
	pricing := json.RawMessage(`{"multiplier":"2.5","unit_price":"0.1"}`)
	intent, err := bc.NewScheduledIntent(key, "user", "tenant", "central", request, cutoff, pricing)
	require.NoError(t, err)
	_, created, err := repo.CreateIntent(ctx, intent)
	require.NoError(t, err)
	require.True(t, created)
	restarted := NewBillingCenterRepository(db, "gateway-billing")
	stored, err := restarted.Get(ctx, key)
	require.NoError(t, err)
	require.JSONEq(t, string(pricing), string(stored.GatewayPricingSnapshot))
	replayed, err := bc.NewScheduledIntent(key, "user", "tenant", "central", request, stored.SettlementNotBefore, stored.GatewayPricingSnapshot)
	require.NoError(t, err)
	_, created, err = restarted.CreateIntent(ctx, replayed)
	require.NoError(t, err)
	require.False(t, created)
	changed, err := bc.NewScheduledIntent(key, "user", "tenant", "central", request, cutoff.Add(-time.Hour), pricing)
	require.NoError(t, err)
	_, _, err = restarted.CreateIntent(ctx, changed)
	require.ErrorIs(t, err, bc.ErrConflict)
	changed = intent
	changed.SettlementNotBefore = time.Time{}
	_, _, err = restarted.CreateIntent(ctx, changed)
	require.ErrorIs(t, err, bc.ErrConflict)
}
