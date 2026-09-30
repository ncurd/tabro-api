//go:build billing_integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestBillingCenterOperatorResolutionSupersedesImmutableOutboxAtomically(t *testing.T) {
	for _, action := range []string{"waive", "verified_usage"} {
		t.Run(action, func(t *testing.T) {
			repo, db := billingCenterTestRepository(t)
			migration, err := migrations.FS.ReadFile("126_billing_center_reconciliation_receipts.sql")
			require.NoError(t, err)
			_, err = db.Exec(string(migration))
			require.NoError(t, err)
			_, err = db.Exec(string(migration))
			require.NoError(t, err)
			ctx := context.Background()
			op := billingCenterTestDispatched(t, repo)
			event := billingCenterTestSettlement(t, op)
			_, err = repo.Enqueue(ctx, event)
			require.NoError(t, err)
			leased, err := repo.Claim(ctx, time.Minute, 1)
			require.NoError(t, err)
			require.Len(t, leased, 1)
			op, err = repo.Get(ctx, op.Key)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE billing_center_operations SET updated_at=NOW()-INTERVAL '1 minute'`)
			require.NoError(t, err)
			pending, err := repo.PendingReconciliations(ctx, 8)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			again, err := repo.PendingReconciliations(ctx, 8)
			require.NoError(t, err)
			require.Empty(t, again)
			target := "settled"
			if action == "waive" {
				target = "released"
			}
			now := time.Now().UTC()
			decision := bc.ReconciliationResolution{Case: bc.ReconciliationCase{CaseID: "case-" + op.OperationID, ReservationID: op.ReservationID, State: "resolved", Reason: "supplier_verified", EvidenceReference: "receipt:1", Version: 2, ResolutionAction: action, OperatorUserID: "operator", ResolvedAt: &now}, Reservation: billingCenterTestRemote(op, target, 3), ResolutionFingerprint: strings.Repeat("a", 64), ActorUserID: op.ActorUserID, TenantID: op.TenantID, ProducerClientID: op.ProducerClientID, OriginAppID: op.OriginAppID}
			invalid := decision
			invalid.TenantID = "unrelated"
			require.Error(t, repo.ApplyReconciliation(ctx, op, invalid))
			// If the terminal operation update fails, neither the operator receipt
			// nor superseding the in-flight event may commit.
			_, err = db.Exec(`CREATE FUNCTION reject_resolution() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state IN ('settled','released') THEN RAISE EXCEPTION 'injected transaction failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_resolution BEFORE UPDATE ON billing_center_operations FOR EACH ROW EXECUTE FUNCTION reject_resolution()`)
			require.NoError(t, err)
			require.Error(t, repo.ApplyReconciliation(ctx, op, decision))
			var receipts int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_reconciliation_receipts`).Scan(&receipts))
			require.Zero(t, receipts)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM billing_center_outbox`).Scan(&status))
			require.Equal(t, "processing", status)
			_, err = db.Exec(`DROP TRIGGER fail_resolution ON billing_center_operations`)
			require.NoError(t, err)
			var wg sync.WaitGroup
			errorsCh := make(chan error, 16)
			for range 16 {
				wg.Add(1)
				go func() { defer wg.Done(); errorsCh <- repo.ApplyReconciliation(ctx, op, decision) }()
			}
			wg.Wait()
			close(errorsCh)
			for e := range errorsCh {
				require.NoError(t, e)
			}
			stored, err := repo.Get(ctx, op.Key)
			require.NoError(t, err)
			require.Equal(t, bc.State(target), stored.State)
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_center_reconciliation_receipts`).Scan(&receipts))
			require.Equal(t, 1, receipts)
			var payload []byte
			var hash, caseID string
			var delivered *time.Time
			require.NoError(t, db.QueryRow(`SELECT status,payload,payload_fingerprint,superseded_by_case_id,delivered_at FROM billing_center_outbox`).Scan(&status, &payload, &hash, &caseID, &delivered))
			require.Equal(t, "superseded", status)
			require.Equal(t, decision.Case.CaseID, caseID)
			require.Nil(t, delivered)
			var original, actual any
			require.NoError(t, json.Unmarshal(event.Body, &original))
			require.NoError(t, json.Unmarshal(payload, &actual))
			require.Equal(t, original, actual)
			require.Equal(t, leased[0].Fingerprint, hash)
			// The old leased completion cannot mislabel a waived settlement delivered.
			require.Error(t, repo.Complete(ctx, leased[0], billingCenterTestRemote(op, "settled", 3)))
			changed := decision
			changed.ResolutionFingerprint = strings.Repeat("b", 64)
			require.Error(t, repo.ApplyReconciliation(ctx, op, changed))
		})
	}
}
