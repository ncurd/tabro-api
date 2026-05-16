package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMediaGenerationJobRepository_CreateAndGetByPublicID(t *testing.T) {
	repo := NewMediaGenerationJobRepository(nil)
	ctx := context.Background()
	now := time.Now().UTC()

	job := &service.MediaGenerationJob{
		PublicID:       "vidjob_test",
		Kind:           service.MediaJobKindVideoGeneration,
		Provider:       service.MediaProviderDashScope,
		Platform:       service.PlatformDashScope,
		Status:         service.MediaJobStatusQueued,
		UpstreamTaskID: "task-1",
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		Model:          "happyhorse-1.0-r2v",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	require.NoError(t, repo.Create(ctx, job))
	got, err := repo.GetByPublicID(ctx, "vidjob_test")

	require.NoError(t, err)
	require.Equal(t, "vidjob_test", got.PublicID)
	require.Equal(t, "task-1", got.UpstreamTaskID)
}

func TestMediaGenerationJobRepository_UpdateFromUpstream(t *testing.T) {
	repo := NewMediaGenerationJobRepository(nil)
	ctx := context.Background()
	now := time.Now().UTC()
	completedAt := now.Add(2 * time.Minute)
	expiresAt := now.Add(24 * time.Hour)

	require.NoError(t, repo.Create(ctx, &service.MediaGenerationJob{
		PublicID:       "vidjob_update",
		Kind:           service.MediaJobKindVideoGeneration,
		Provider:       service.MediaProviderVolcengineArk,
		Platform:       service.PlatformVolcengineArk,
		Status:         service.MediaJobStatusQueued,
		UpstreamTaskID: "task-update",
		UserID:         10,
		APIKeyID:       20,
		AccountID:      30,
		Model:          "seedance-2.0",
		CreatedAt:      now,
		UpdatedAt:      now,
	}))

	got, err := repo.UpdateFromUpstream(ctx, "vidjob_update", service.MediaGenerationJobUpdate{
		Status:               service.MediaJobStatusSucceeded,
		UpstreamStatus:       "SUCCEEDED",
		UpstreamRequestID:    "req-1",
		UpstreamResponseJSON: []byte(`{"task_status":"SUCCEEDED"}`),
		ResultURL:            "https://example.com/video.mp4",
		ResultContentType:    "video/mp4",
		ExpiresAt:            &expiresAt,
		VideoDurationSeconds: 5,
		VideoResolution:      "720p",
		VideoRatio:           "16:9",
		VideoCount:           1,
		CompletedAt:          &completedAt,
	})

	require.NoError(t, err)
	require.Equal(t, service.MediaJobStatusSucceeded, got.Status)
	require.Equal(t, "SUCCEEDED", got.UpstreamStatus)
	require.Equal(t, "req-1", got.UpstreamRequestID)
	require.JSONEq(t, `{"task_status":"SUCCEEDED"}`, string(got.UpstreamResponseJSON))
	require.Equal(t, "https://example.com/video.mp4", got.ResultURL)
	require.Equal(t, "video/mp4", got.ResultContentType)
	require.Equal(t, expiresAt, *got.ExpiresAt)
	require.Equal(t, 5, got.VideoDurationSeconds)
	require.Equal(t, "720p", got.VideoResolution)
	require.Equal(t, "16:9", got.VideoRatio)
	require.Equal(t, 1, got.VideoCount)
	require.Equal(t, completedAt, *got.CompletedAt)
	require.True(t, got.UpdatedAt.After(now) || got.UpdatedAt.Equal(now))
}

func TestMediaGenerationJobRepository_MarkUsageRecordedIsIdempotent(t *testing.T) {
	repo := NewMediaGenerationJobRepository(nil)
	ctx := context.Background()
	now := time.Now().UTC()
	recordedAt := now.Add(time.Minute)

	require.NoError(t, repo.Create(ctx, &service.MediaGenerationJob{
		PublicID:  "audjob_usage",
		Kind:      service.MediaJobKindAudioSpeech,
		Provider:  service.MediaProviderAzureSpeech,
		Platform:  service.PlatformAzureSpeech,
		Status:    service.MediaJobStatusSucceeded,
		UserID:    1,
		APIKeyID:  2,
		AccountID: 3,
		Model:     "tts-1",
		CreatedAt: now,
		UpdatedAt: now,
	}))

	recorded, err := repo.MarkUsageRecorded(ctx, "audjob_usage", recordedAt)
	require.NoError(t, err)
	require.True(t, recorded)

	recorded, err = repo.MarkUsageRecorded(ctx, "audjob_usage", recordedAt.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, recorded)

	got, err := repo.GetByPublicID(ctx, "audjob_usage")
	require.NoError(t, err)
	require.Equal(t, recordedAt, *got.UsageRecordedAt)
}
