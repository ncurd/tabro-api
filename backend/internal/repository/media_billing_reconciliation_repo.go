package repository

import (
	"context"
	"sort"
	"time"

	"entgo.io/ent/dialect/sql"
	dbmediagenerationjob "github.com/Wei-Shaw/sub2api/ent/mediagenerationjob"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Claim uses a lease rather than an in-process queue, so a restarted instance
// resumes outstanding jobs and replicas do not normally poll the same task.
func (r *mediaGenerationJobRepository) ClaimMediaReconciliation(ctx context.Context, now, leaseUntil time.Time, limit int) ([]*service.MediaGenerationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		jobs := make([]*service.MediaGenerationJob, 0)
		for _, j := range r.memory {
			if len(j.BillingSnapshotJSON) == 0 || j.UsageRecordedAt != nil || (j.NextPollAt != nil && j.NextPollAt.After(now)) || (j.Status != service.MediaJobStatusSucceeded && !(j.Kind == "voice_clone" && j.Status == service.MediaJobStatusCanceled && j.AudioVoice != "") && (j.Kind != service.MediaJobKindVideoGeneration || j.UpstreamTaskID == "" || j.Status == service.MediaJobStatusFailed || j.Status == service.MediaJobStatusCanceled)) {
				continue
			}
			jobs = append(jobs, j)
		}
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].NextPollAt == nil {
				return jobs[j].NextPollAt != nil
			}
			if jobs[j].NextPollAt == nil {
				return false
			}
			return jobs[i].NextPollAt.Before(*jobs[j].NextPollAt)
		})
		if len(jobs) > limit {
			jobs = jobs[:limit]
		}
		out := make([]*service.MediaGenerationJob, 0, len(jobs))
		for _, j := range jobs {
			j.NextPollAt = timePointer(&leaseUntil)
			out = append(out, cloneMediaGenerationJob(j))
		}
		return out, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	jobs, err := tx.MediaGenerationJob.Query().Where(
		dbmediagenerationjob.BillingSnapshotJSONNotNil(), dbmediagenerationjob.UsageRecordedAtIsNil(),
		dbmediagenerationjob.Or(dbmediagenerationjob.NextPollAtIsNil(), dbmediagenerationjob.NextPollAtLTE(now)),
		dbmediagenerationjob.Or(
			dbmediagenerationjob.StatusEQ(service.MediaJobStatusSucceeded),
			dbmediagenerationjob.And(dbmediagenerationjob.KindEQ("voice_clone"), dbmediagenerationjob.StatusEQ(service.MediaJobStatusCanceled), dbmediagenerationjob.AudioVoiceNotNil(), dbmediagenerationjob.AudioVoiceNEQ("")),
			dbmediagenerationjob.And(dbmediagenerationjob.KindEQ(service.MediaJobKindVideoGeneration), dbmediagenerationjob.UpstreamTaskIDNotNil(), dbmediagenerationjob.UpstreamTaskIDNEQ(""), dbmediagenerationjob.StatusIn(service.MediaJobStatusQueued, service.MediaJobStatusRunning, service.MediaJobStatusUnknown)),
		),
	).Order(dbmediagenerationjob.ByNextPollAt(sql.OrderNullsFirst())).Limit(limit).ForUpdate(sql.WithLockAction(sql.SkipLocked)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.MediaGenerationJob, 0, len(jobs))
	for _, j := range jobs {
		if err := tx.MediaGenerationJob.UpdateOneID(j.ID).SetNextPollAt(leaseUntil).Exec(ctx); err != nil {
			return nil, err
		}
		out = append(out, mediaGenerationJobFromEnt(j))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *mediaGenerationJobRepository) RescheduleMediaReconciliation(ctx context.Context, publicID string, at *time.Time) error {
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if j := r.memory[publicID]; j != nil {
			j.NextPollAt = timePointer(at)
		}
		return nil
	}
	b := r.client.MediaGenerationJob.Update().Where(dbmediagenerationjob.PublicIDEQ(publicID))
	if at == nil {
		b.ClearNextPollAt()
	} else {
		b.SetNextPollAt(*at)
	}
	return b.Exec(ctx)
}
