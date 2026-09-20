package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mediaSchedulerBucketCache struct {
	SchedulerCache
	buckets []SchedulerBucket
}

func (c *mediaSchedulerBucketCache) TryLockBucket(_ context.Context, bucket SchedulerBucket, _ time.Duration) (bool, error) {
	c.buckets = append(c.buckets, bucket)
	// Another worker holds the lock; capturing attempts verifies invalidation dispatch.
	return false, nil
}

func TestSchedulerMediaPlatformsDefaultAndGroupRebuild(t *testing.T) {
	cache := &mediaSchedulerBucketCache{}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	defaults, err := svc.defaultBuckets(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.rebuildByGroupIDs(context.Background(), []int64{42}, "group_changed", nil))
	for _, platform := range []string{PlatformDashScope, PlatformVolcengineArk, PlatformAzureSpeech} {
		for _, mode := range []string{SchedulerModeSingle, SchedulerModeForced} {
			require.Contains(t, defaults, SchedulerBucket{GroupID: 0, Platform: platform, Mode: mode})
			require.Contains(t, cache.buckets, SchedulerBucket{GroupID: 42, Platform: platform, Mode: mode})
		}
	}
}
