package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestListMediaResourcesFiltersOwnerAndPaginates(t *testing.T) {
	repo := &mediaGenerationJobRepository{memory: map[string]*service.MediaGenerationJob{}}
	groupID, otherGroupID := int64(3), int64(4)
	for _, job := range []*service.MediaGenerationJob{
		{PublicID: "voice_a", UserID: 1, APIKeyID: 2, GroupID: &groupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded, CreatedAt: time.Unix(100, 0)},
		{PublicID: "voice_b", UserID: 1, APIKeyID: 2, GroupID: &groupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded, CreatedAt: time.Unix(200, 0)},
		{PublicID: "voice_other_user", UserID: 8, APIKeyID: 2, GroupID: &groupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "voice_other_key", UserID: 1, APIKeyID: 8, GroupID: &groupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "voice_other_group", UserID: 1, APIKeyID: 2, GroupID: &otherGroupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "voice_nil_group", UserID: 1, APIKeyID: 2, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "voice_deleted", UserID: 1, APIKeyID: 2, GroupID: &groupID, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusCanceled},
		{PublicID: "video_other_kind", UserID: 1, APIKeyID: 2, GroupID: &groupID, Kind: service.MediaJobKindVideoGeneration, Status: service.MediaJobStatusSucceeded},
	} {
		require.NoError(t, repo.Create(context.Background(), job))
	}
	jobs, err := repo.ListMediaResources(context.Background(), 1, 2, &groupID, service.MediaJobKindVoiceClone, 20, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.Equal(t, "voice_b", jobs[0].PublicID)
	require.Equal(t, "voice_a", jobs[1].PublicID)
	jobs, err = repo.ListMediaResources(context.Background(), 1, 2, &groupID, service.MediaJobKindVoiceClone, 1, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "voice_a", jobs[0].PublicID)
	jobs, err = repo.ListMediaResources(context.Background(), 1, 2, nil, service.MediaJobKindVoiceClone, 20, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "voice_nil_group", jobs[0].PublicID)
	jobs, err = repo.ListMediaResources(context.Background(), 1, 2, &groupID, service.MediaJobKindVoiceClone, 20, 50)
	require.NoError(t, err)
	require.Empty(t, jobs)
}

func TestListMediaResourcesByGroupIDsKeepsOwnerAndPagination(t *testing.T) {
	repo := &mediaGenerationJobRepository{memory: map[string]*service.MediaGenerationJob{}}
	a, b, denied := int64(3), int64(4), int64(5)
	for _, job := range []*service.MediaGenerationJob{
		{PublicID: "old", UserID: 1, APIKeyID: 2, GroupID: &a, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded, CreatedAt: time.Unix(1, 0)},
		{PublicID: "new", UserID: 1, APIKeyID: 2, GroupID: &b, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded, CreatedAt: time.Unix(2, 0)},
		{PublicID: "private", UserID: 1, APIKeyID: 2, GroupID: &denied, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "other-user", UserID: 9, APIKeyID: 2, GroupID: &a, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "other-key", UserID: 1, APIKeyID: 9, GroupID: &a, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
		{PublicID: "ungrouped", UserID: 1, APIKeyID: 2, Kind: service.MediaJobKindVoiceClone, Status: service.MediaJobStatusSucceeded},
	} {
		require.NoError(t, repo.Create(context.Background(), job))
	}
	jobs, err := repo.ListMediaResourcesByGroupIDs(context.Background(), 1, 2, []int64{a, b}, service.MediaJobKindVoiceClone, 20, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.Equal(t, "new", jobs[0].PublicID)
	require.Equal(t, "old", jobs[1].PublicID)
	jobs, err = repo.ListMediaResourcesByGroupIDs(context.Background(), 1, 2, []int64{a, b}, service.MediaJobKindVoiceClone, 1, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "old", jobs[0].PublicID)
	jobs, err = repo.ListMediaResourcesByGroupIDs(context.Background(), 1, 2, nil, service.MediaJobKindVoiceClone, 20, 0)
	require.NoError(t, err)
	require.Empty(t, jobs, "an empty allowed group set must never widen access")
}
