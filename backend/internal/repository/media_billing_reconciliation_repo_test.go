package repository

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMediaBillingReconciliationLeasesAndRecoversAllSuccessKinds(t *testing.T) {
	r := NewMediaGenerationJobRepository(nil).(*mediaGenerationJobRepository)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, kind := range []string{service.MediaJobKindVideoGeneration, service.MediaJobKindAudioSpeech, "audio_transcription", "voice_clone"} {
		require.NoError(t, r.Create(ctx, &service.MediaGenerationJob{PublicID: kind, Kind: kind, Status: service.MediaJobStatusSucceeded, BillingSnapshotJSON: []byte(`{"version":1}`)}))
	}
	require.NoError(t, r.Create(ctx, &service.MediaGenerationJob{PublicID: "deleted_voice", Kind: "voice_clone", Status: service.MediaJobStatusCanceled, AudioVoice: "provider-voice", BillingSnapshotJSON: []byte(`{"version":1}`)}))
	require.NoError(t, r.Create(ctx, &service.MediaGenerationJob{PublicID: "legacy", Kind: service.MediaJobKindVideoGeneration, Status: service.MediaJobStatusSucceeded}))
	jobs, err := r.ClaimMediaReconciliation(ctx, now, now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, jobs, 5)
	second, err := r.ClaimMediaReconciliation(ctx, now, now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, second)
	_, err = r.MarkUsageRecorded(ctx, service.MediaJobKindVideoGeneration, now)
	require.NoError(t, err)
	afterCrash, err := r.ClaimMediaReconciliation(ctx, now.Add(2*time.Minute), now.Add(3*time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, afterCrash, 4)
}
func TestMediaBillingReconciliationUsesSkipLockedTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(sql.OpenDB(dialect.Postgres, db)))
	r := NewMediaGenerationJobRepository(client).(*mediaGenerationJobRepository)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "media_generation_jobs" .* FOR UPDATE SKIP LOCKED`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()
	now := time.Now().UTC()
	jobs, err := r.ClaimMediaReconciliation(context.Background(), now, now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Empty(t, jobs)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestMediaBillingResultCannotRegressAfterSuccess(t *testing.T) {
	r := NewMediaGenerationJobRepository(nil)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, &service.MediaGenerationJob{PublicID: "job", Kind: service.MediaJobKindVideoGeneration, Status: service.MediaJobStatusRunning}))
	_, err := r.UpdateFromUpstream(ctx, "job", service.MediaGenerationJobUpdate{Status: service.MediaJobStatusSucceeded, UpstreamResponseJSON: []byte(`{"duration":5}`)})
	require.NoError(t, err)
	got, err := r.UpdateFromUpstream(ctx, "job", service.MediaGenerationJobUpdate{Status: service.MediaJobStatusRunning, UpstreamResponseJSON: []byte(`{"status":"running"}`)})
	require.NoError(t, err)
	require.Equal(t, service.MediaJobStatusSucceeded, got.Status)
	require.JSONEq(t, `{"duration":5}`, string(got.UpstreamResponseJSON))
}
