package repository

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbmediagenerationjob "github.com/Wei-Shaw/sub2api/ent/mediagenerationjob"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type mediaGenerationJobRepository struct {
	client *dbent.Client
	mu     sync.Mutex
	memory map[string]*service.MediaGenerationJob
}

func NewMediaGenerationJobRepository(client *dbent.Client) service.MediaGenerationJobRepository {
	return &mediaGenerationJobRepository{
		client: client,
		memory: map[string]*service.MediaGenerationJob{},
	}
}

func (r *mediaGenerationJobRepository) Create(ctx context.Context, job *service.MediaGenerationJob) error {
	if job == nil {
		return nil
	}
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.memory[job.PublicID] = cloneMediaGenerationJob(job)
		return nil
	}

	create := r.client.MediaGenerationJob.Create().
		SetPublicID(job.PublicID).
		SetKind(job.Kind).
		SetProvider(job.Provider).
		SetPlatform(job.Platform).
		SetStatus(job.Status).
		SetUserID(job.UserID).
		SetAPIKeyID(job.APIKeyID).
		SetNillableGroupID(job.GroupID).
		SetAccountID(job.AccountID).
		SetModel(job.Model).
		SetAudioCharacterCount(job.AudioCharacterCount).
		SetVideoDurationSeconds(job.VideoDurationSeconds).
		SetVideoCount(job.VideoCount).
		SetNillableExpiresAt(job.ExpiresAt).
		SetNillableUsageRecordedAt(job.UsageRecordedAt).
		SetNillableSubmittedAt(job.SubmittedAt).
		SetNillableCompletedAt(job.CompletedAt)
	if !job.CreatedAt.IsZero() {
		create.SetCreatedAt(job.CreatedAt)
	}
	if !job.UpdatedAt.IsZero() {
		create.SetUpdatedAt(job.UpdatedAt)
	}
	if job.UpstreamStatus != "" {
		create.SetUpstreamStatus(job.UpstreamStatus)
	}
	if job.UpstreamTaskID != "" {
		create.SetUpstreamTaskID(job.UpstreamTaskID)
	}
	if job.UpstreamRequestID != "" {
		create.SetUpstreamRequestID(job.UpstreamRequestID)
	}
	if len(job.RequestJSON) > 0 {
		create.SetRequestJSON(json.RawMessage(job.RequestJSON))
	}
	if len(job.UpstreamResponseJSON) > 0 {
		create.SetUpstreamResponseJSON(json.RawMessage(job.UpstreamResponseJSON))
	}
	if job.ResultURL != "" {
		create.SetResultURL(job.ResultURL)
	}
	if job.ResultContentType != "" {
		create.SetResultContentType(job.ResultContentType)
	}
	if job.AudioVoice != "" {
		create.SetAudioVoice(job.AudioVoice)
	}
	if job.AudioFormat != "" {
		create.SetAudioFormat(job.AudioFormat)
	}
	if job.VideoResolution != "" {
		create.SetVideoResolution(job.VideoResolution)
	}
	if job.VideoRatio != "" {
		create.SetVideoRatio(job.VideoRatio)
	}
	if job.ErrorCode != "" {
		create.SetErrorCode(job.ErrorCode)
	}
	if job.ErrorMessage != "" {
		create.SetErrorMessage(job.ErrorMessage)
	}

	entJob, err := create.Save(ctx)
	if err != nil {
		return err
	}
	*job = *mediaGenerationJobFromEnt(entJob)
	return nil
}

func (r *mediaGenerationJobRepository) GetByPublicID(ctx context.Context, publicID string) (*service.MediaGenerationJob, error) {
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		job, ok := r.memory[publicID]
		if !ok {
			return nil, nil
		}
		return cloneMediaGenerationJob(job), nil
	}

	entJob, err := r.client.MediaGenerationJob.Query().
		Where(dbmediagenerationjob.PublicIDEQ(publicID)).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mediaGenerationJobFromEnt(entJob), nil
}

func (r *mediaGenerationJobRepository) UpdateFromUpstream(
	ctx context.Context,
	publicID string,
	update service.MediaGenerationJobUpdate,
) (*service.MediaGenerationJob, error) {
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		job, ok := r.memory[publicID]
		if !ok {
			return nil, nil
		}
		applyMediaGenerationJobUpdate(job, update, time.Now().UTC())
		return cloneMediaGenerationJob(job), nil
	}

	entJob, err := r.client.MediaGenerationJob.Query().
		Where(dbmediagenerationjob.PublicIDEQ(publicID)).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	builder := r.client.MediaGenerationJob.UpdateOne(entJob).
		SetUpdatedAt(time.Now().UTC()).
		SetNillableExpiresAt(update.ExpiresAt).
		SetVideoDurationSeconds(update.VideoDurationSeconds).
		SetVideoCount(update.VideoCount).
		SetNillableCompletedAt(update.CompletedAt)
	if update.Status != "" {
		builder.SetStatus(update.Status)
	}
	if update.UpstreamStatus != "" {
		builder.SetUpstreamStatus(update.UpstreamStatus)
	}
	if update.UpstreamRequestID != "" {
		builder.SetUpstreamRequestID(update.UpstreamRequestID)
	}
	if update.UpstreamResponseJSON != nil {
		builder.SetUpstreamResponseJSON(json.RawMessage(update.UpstreamResponseJSON))
	}
	if update.ResultURL != "" {
		builder.SetResultURL(update.ResultURL)
	}
	if update.ResultContentType != "" {
		builder.SetResultContentType(update.ResultContentType)
	}
	if update.VideoResolution != "" {
		builder.SetVideoResolution(update.VideoResolution)
	}
	if update.VideoRatio != "" {
		builder.SetVideoRatio(update.VideoRatio)
	}
	if update.ErrorCode != "" {
		builder.SetErrorCode(update.ErrorCode)
	}
	if update.ErrorMessage != "" {
		builder.SetErrorMessage(update.ErrorMessage)
	}

	entJob, err = builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return mediaGenerationJobFromEnt(entJob), nil
}

func (r *mediaGenerationJobRepository) MarkUsageRecorded(ctx context.Context, publicID string, at time.Time) (bool, error) {
	if r.client == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		job, ok := r.memory[publicID]
		if !ok {
			return false, nil
		}
		if job.UsageRecordedAt != nil {
			return false, nil
		}
		recordedAt := at
		job.UsageRecordedAt = &recordedAt
		job.UpdatedAt = time.Now().UTC()
		return true, nil
	}

	affected, err := r.client.MediaGenerationJob.Update().
		Where(
			dbmediagenerationjob.PublicIDEQ(publicID),
			dbmediagenerationjob.UsageRecordedAtIsNil(),
		).
		SetUsageRecordedAt(at).
		SetUpdatedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func applyMediaGenerationJobUpdate(job *service.MediaGenerationJob, update service.MediaGenerationJobUpdate, now time.Time) {
	if update.Status != "" {
		job.Status = update.Status
	}
	if update.UpstreamStatus != "" {
		job.UpstreamStatus = update.UpstreamStatus
	}
	if update.UpstreamRequestID != "" {
		job.UpstreamRequestID = update.UpstreamRequestID
	}
	if update.UpstreamResponseJSON != nil {
		job.UpstreamResponseJSON = append([]byte(nil), update.UpstreamResponseJSON...)
	}
	if update.ResultURL != "" {
		job.ResultURL = update.ResultURL
	}
	if update.ResultContentType != "" {
		job.ResultContentType = update.ResultContentType
	}
	if update.ExpiresAt != nil {
		expiresAt := *update.ExpiresAt
		job.ExpiresAt = &expiresAt
	}
	job.VideoDurationSeconds = update.VideoDurationSeconds
	if update.VideoResolution != "" {
		job.VideoResolution = update.VideoResolution
	}
	if update.VideoRatio != "" {
		job.VideoRatio = update.VideoRatio
	}
	job.VideoCount = update.VideoCount
	if update.ErrorCode != "" {
		job.ErrorCode = update.ErrorCode
	}
	if update.ErrorMessage != "" {
		job.ErrorMessage = update.ErrorMessage
	}
	if update.CompletedAt != nil {
		completedAt := *update.CompletedAt
		job.CompletedAt = &completedAt
	}
	job.UpdatedAt = now
}

func mediaGenerationJobFromEnt(job *dbent.MediaGenerationJob) *service.MediaGenerationJob {
	if job == nil {
		return nil
	}
	return &service.MediaGenerationJob{
		ID:                   job.ID,
		PublicID:             job.PublicID,
		Kind:                 job.Kind,
		Provider:             job.Provider,
		Platform:             job.Platform,
		Status:               job.Status,
		UpstreamStatus:       stringValue(job.UpstreamStatus),
		UpstreamTaskID:       stringValue(job.UpstreamTaskID),
		UpstreamRequestID:    stringValue(job.UpstreamRequestID),
		UserID:               job.UserID,
		APIKeyID:             job.APIKeyID,
		GroupID:              int64Pointer(job.GroupID),
		AccountID:            job.AccountID,
		Model:                job.Model,
		RequestJSON:          append([]byte(nil), job.RequestJSON...),
		UpstreamResponseJSON: append([]byte(nil), job.UpstreamResponseJSON...),
		ResultURL:            stringValue(job.ResultURL),
		ResultContentType:    stringValue(job.ResultContentType),
		ExpiresAt:            timePointer(job.ExpiresAt),
		AudioVoice:           stringValue(job.AudioVoice),
		AudioFormat:          stringValue(job.AudioFormat),
		AudioCharacterCount:  job.AudioCharacterCount,
		VideoDurationSeconds: job.VideoDurationSeconds,
		VideoResolution:      stringValue(job.VideoResolution),
		VideoRatio:           stringValue(job.VideoRatio),
		VideoCount:           job.VideoCount,
		ErrorCode:            stringValue(job.ErrorCode),
		ErrorMessage:         stringValue(job.ErrorMessage),
		UsageRecordedAt:      timePointer(job.UsageRecordedAt),
		CreatedAt:            job.CreatedAt,
		UpdatedAt:            job.UpdatedAt,
		SubmittedAt:          timePointer(job.SubmittedAt),
		CompletedAt:          timePointer(job.CompletedAt),
	}
}

func cloneMediaGenerationJob(job *service.MediaGenerationJob) *service.MediaGenerationJob {
	if job == nil {
		return nil
	}
	clone := *job
	clone.RequestJSON = append([]byte(nil), job.RequestJSON...)
	clone.UpstreamResponseJSON = append([]byte(nil), job.UpstreamResponseJSON...)
	clone.GroupID = int64Pointer(job.GroupID)
	clone.ExpiresAt = timePointer(job.ExpiresAt)
	clone.UsageRecordedAt = timePointer(job.UsageRecordedAt)
	clone.SubmittedAt = timePointer(job.SubmittedAt)
	clone.CompletedAt = timePointer(job.CompletedAt)
	return &clone
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func int64Pointer(v *int64) *int64 {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func timePointer(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}
