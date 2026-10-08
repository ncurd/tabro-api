//go:build billing_integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestBillingCenterConnectionChangesRequireAllProducersToFinish(t *testing.T) {
	repo, db := billingCenterTestRepository(t)
	ctx := context.Background()
	_, err := db.Exec(`CREATE TABLE users(id bigint PRIMARY KEY); INSERT INTO users(id) VALUES(1)`)
	require.NoError(t, err)
	identityMigration, err := migrations.FS.ReadFile("119_billing_center_account_routes.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(identityMigration))
	require.NoError(t, err)

	newProducer := NewBillingCenterRepository(db, "new-producer")
	pending, err := newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.False(t, pending)
	// Daily settlement is still unfinished even before its scheduled due time,
	// and a newly selected producer must see the old producer's frozen operation.
	op := billingCenterTestDispatched(t, repo, time.Now().Add(24*time.Hour))
	event := billingCenterTestSettlement(t, op)
	_, err = repo.Enqueue(ctx, event)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.True(t, pending)
	_, err = db.Exec(`UPDATE billing_center_operations SET state='settled'; UPDATE billing_center_outbox SET status='delivered'`)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.False(t, pending)
	_, err = db.Exec(`UPDATE billing_center_outbox SET status='superseded'`)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.False(t, pending, "a reconciliation-superseded event is terminal")

	// Even with terminal operations, a blocked or undelivered detail must not be
	// abandoned by a connector identity change.
	_, err = db.Exec(`UPDATE billing_center_outbox SET status='blocked'`)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.True(t, pending)
	_, err = db.Exec(`UPDATE billing_center_outbox SET status='delivered';
 INSERT INTO billing_center_shadow_observations
 (producer_client_id,origin_app_id,operation_id,local_user_id,product_key,request_payload_hash,quote)
 VALUES('historical','app','shadow',1,'test','hash','{}')`)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.True(t, pending)
	_, err = db.Exec(`UPDATE billing_center_shadow_observations SET central_estimate='{}'`)
	require.NoError(t, err)
	pending, err = newProducer.HasPendingBillingOperations(ctx)
	require.NoError(t, err)
	require.False(t, pending)
}
