package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedulerFallbackCache struct {
	SchedulerCache
	mu       sync.Mutex
	snapshot []byte
	account  []byte
	reads    atomic.Int64
	writeErr error
}

func (c *schedulerFallbackCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.reads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshot == nil {
		return nil, false, nil
	}
	var accounts []*Account
	err := json.Unmarshal(c.snapshot, &accounts)
	return accounts, err == nil, err
}

func (c *schedulerFallbackCache) SetSnapshot(_ context.Context, _ SchedulerBucket, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	var err error
	c.snapshot, err = json.Marshal(accounts)
	return err
}

func (c *schedulerFallbackCache) GetAccount(context.Context, int64) (*Account, error) {
	c.reads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.account == nil {
		return nil, nil
	}
	var account *Account
	err := json.Unmarshal(c.account, &account)
	return account, err
}

func (c *schedulerFallbackCache) SetAccount(_ context.Context, account *Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	var err error
	c.account, err = json.Marshal(account)
	return err
}

type schedulerFallbackRepo struct {
	AccountRepository
	calls atomic.Int64
	load  func(context.Context) ([]Account, error)
}

func (r *schedulerFallbackRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, _ string) ([]Account, error) {
	r.calls.Add(1)
	return r.load(ctx)
}

func (r *schedulerFallbackRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	r.calls.Add(1)
	accounts, err := r.load(ctx)
	if err != nil || len(accounts) == 0 {
		return nil, err
	}
	return &accounts[0], nil
}

func TestSchedulerFallbackConcurrentMissesUseOneDBQuery(t *testing.T) {
	for _, accountOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "account"}[accountOnly], func(t *testing.T) {
			const workers = 32
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cache := &schedulerFallbackCache{}
			release := make(chan struct{})
			repo := &schedulerFallbackRepo{load: func(ctx context.Context) ([]Account, error) {
				select {
				case <-release:
					return []Account{{ID: 1, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "model"}}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.DbFallbackEnabled = true
			cfg.Gateway.Scheduling.DbFallbackMaxQPS = 1
			svc := NewSchedulerSnapshotService(cache, nil, repo, nil, cfg)
			groupID := int64(7)
			accounts := make([]*Account, workers)
			errs := make([]error, workers)
			var wg sync.WaitGroup
			for i := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if accountOnly {
						accounts[i], errs[i] = svc.GetAccount(ctx, 1)
						return
					}
					var list []Account
					list, _, errs[i] = svc.ListSchedulableAccounts(ctx, &groupID, PlatformOpenAI, false)
					if len(list) > 0 {
						accounts[i] = &list[0]
					}
				}()
			}
			require.Eventually(t, func() bool { return cache.reads.Load() >= workers+1 }, time.Second, time.Millisecond)
			close(release)
			wg.Wait()
			for i := range workers {
				require.NoError(t, errs[i])
				require.NotNil(t, accounts[i])
			}
			require.EqualValues(t, 1, repo.calls.Load(), "waiters must not exhaust the DB fallback limit")
			accounts[0].Credentials["model_mapping"].(map[string]any)["alias"] = "changed"
			require.Equal(t, "model", accounts[1].Credentials["model_mapping"].(map[string]any)["alias"])
			// Subsequent requests use the newly populated cache.
			if accountOnly {
				_, err := svc.GetAccount(ctx, 1)
				require.NoError(t, err)
			} else {
				_, _, err := svc.ListSchedulableAccounts(ctx, &groupID, PlatformOpenAI, false)
				require.NoError(t, err)
			}
			require.EqualValues(t, 1, repo.calls.Load())
		})
	}
}

func TestSchedulerFallbackCancelledWaiterDoesNotWaitForDB(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	started, release := make(chan struct{}), make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := svc.loadFallback(context.Background(), "test", func(context.Context) (any, error) {
			close(started)
			<-release
			return []Account{}, nil
		})
		leaderDone <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := svc.loadFallback(ctx, "test", func(context.Context) (any, error) {
		t.Error("waiter must share the running query")
		return nil, nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-leaderDone:
		t.Fatal("the leader should still be loading")
	default:
	}
	close(release)
	require.NoError(t, <-leaderDone)
}

type schedulerWaiterContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *schedulerWaiterContext) Done() <-chan struct{} {
	// loadFallback evaluates Done in its select after joining DoChan.
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestSchedulerFallbackRetriesCancelledLeaderForLiveCaller(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	started := make(chan struct{})
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan error, 1)
	go func() {
		_, err := svc.loadFallback(leaderCtx, "test", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		leaderDone <- err
	}()
	<-started
	liveCtx, cancelLive := context.WithTimeout(context.Background(), time.Second)
	defer cancelLive()
	waiterCtx := &schedulerWaiterContext{Context: liveCtx, waiting: make(chan struct{})}
	liveDone := make(chan error, 1)
	go func() {
		_, err := svc.loadFallback(waiterCtx, "test", func(context.Context) (any, error) {
			return []Account{{ID: 1}}, nil
		})
		liveDone <- err
	}()
	select {
	case <-waiterCtx.waiting:
	case <-liveCtx.Done():
		t.Fatal("live caller did not join the running query")
	}
	cancelLeader()
	require.ErrorIs(t, <-leaderDone, context.Canceled)
	require.NoError(t, <-liveDone)
}

func TestSchedulerFallbackDoesNotRetryDBErrors(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	var calls int
	_, err := svc.loadFallback(context.Background(), "test", func(context.Context) (any, error) {
		calls++
		return nil, context.DeadlineExceeded
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, calls)
}

func TestSchedulerAccountFallbackCacheWriteFailureIsBestEffort(t *testing.T) {
	cache := &schedulerFallbackCache{writeErr: errors.New("redis unavailable")}
	repo := &schedulerFallbackRepo{load: func(context.Context) ([]Account, error) {
		return []Account{{ID: 1}}, nil
	}}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	account, err := svc.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), account.ID)
}
