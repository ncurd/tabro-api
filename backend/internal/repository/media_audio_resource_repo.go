package repository

import (
	"context"
	"sort"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbmediagenerationjob "github.com/Wei-Shaw/sub2api/ent/mediagenerationjob"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.MediaAudioResourceRepository = (*mediaGenerationJobRepository)(nil)

func (r *mediaGenerationJobRepository) ListMediaResources(ctx context.Context, userID, apiKeyID int64, groupID *int64, kind string, limit, offset int) ([]*service.MediaGenerationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		jobs := make([]*service.MediaGenerationJob, 0)
		for _, job := range r.memory {
			if job.UserID != userID || job.APIKeyID != apiKeyID || job.Kind != kind || job.Status != service.MediaJobStatusSucceeded {
				continue
			}
			if (job.GroupID == nil) != (groupID == nil) || (job.GroupID != nil && *job.GroupID != *groupID) {
				continue
			}
			jobs = append(jobs, cloneMediaGenerationJob(job))
		}
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
				return jobs[i].PublicID > jobs[j].PublicID
			}
			return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
		})
		if offset >= len(jobs) {
			return []*service.MediaGenerationJob{}, nil
		}
		return jobs[offset:min(len(jobs), offset+limit)], nil
	}
	query := r.client.MediaGenerationJob.Query().Where(
		dbmediagenerationjob.UserIDEQ(userID), dbmediagenerationjob.APIKeyIDEQ(apiKeyID),
		dbmediagenerationjob.KindEQ(kind), dbmediagenerationjob.StatusEQ(service.MediaJobStatusSucceeded),
	)
	if groupID == nil {
		query.Where(dbmediagenerationjob.GroupIDIsNil())
	} else {
		query.Where(dbmediagenerationjob.GroupIDEQ(*groupID))
	}
	rows, err := query.Order(dbent.Desc(dbmediagenerationjob.FieldCreatedAt), dbent.Desc(dbmediagenerationjob.FieldPublicID)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]*service.MediaGenerationJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, mediaGenerationJobFromEnt(row))
	}
	return jobs, nil
}
