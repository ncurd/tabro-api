//go:build unit

package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type oidcGatewayKeyRepoStub struct {
	APIKeyRepository
	mu              sync.Mutex
	keys            map[string]*APIKey
	listed          []APIKey
	createErrForKey func(*APIKey) error
	createHook      func(*APIKey)
	creates         int
}

func (r *oidcGatewayKeyRepoStub) GetByKeyForAuth(_ context.Context, key string) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.keys[key]
	if !ok {
		return nil, ErrAPIKeyNotFound
	}
	clone := *value
	return &clone, nil
}

func (r *oidcGatewayKeyRepoStub) ListByUserID(_ context.Context, _ int64, _ pagination.PaginationParams, _ APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]APIKey(nil), r.listed...), &pagination.PaginationResult{}, nil
}

func (r *oidcGatewayKeyRepoStub) ListKeysByUserID(_ context.Context, userID int64) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.listed))
	for i := range r.listed {
		if r.listed[i].UserID == userID {
			keys = append(keys, r.listed[i].Key)
		}
	}
	return keys, nil
}

func (r *oidcGatewayKeyRepoStub) Create(_ context.Context, key *APIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	if r.createHook != nil {
		r.createHook(key)
	}
	if r.createErrForKey != nil {
		if err := r.createErrForKey(key); err != nil {
			return err
		}
	}
	key.ID = int64(100 + r.creates)
	clone := *key
	clone.User = &User{ID: key.UserID, Status: StatusActive}
	if r.keys == nil {
		r.keys = make(map[string]*APIKey)
	}
	r.keys[key.Key] = &clone
	r.listed = append(r.listed, clone)
	return nil
}

type oidcGatewayUserRepoStub struct {
	UserRepository
	user *User
}

func (r *oidcGatewayUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, ErrUserNotFound
	}
	return r.user, nil
}

type oidcGatewayGroupRepoStub struct {
	GroupRepository
	groups []Group
}

func (r *oidcGatewayGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	return append([]Group(nil), r.groups...), nil
}

type oidcGatewaySubRepoStub struct {
	UserSubscriptionRepository
}

func (r *oidcGatewaySubRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	return nil, nil
}

func newOIDCGatewayKeyService(repo APIKeyRepository, users UserRepository, groups GroupRepository, subs UserSubscriptionRepository) *APIKeyService {
	cfg := &config.Config{}
	cfg.JWT.Secret = "oidc-gateway-key-test-secret"
	return NewAPIKeyService(repo, users, groups, subs, nil, nil, cfg)
}

func TestEnsureOIDCGatewayKeyReusesExistingWithoutOverwritingPolicy(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)
	keyValue := oidcGatewayKeyPrefix + "existing-managed-key"
	groupID := int64(42)
	existing := &APIKey{
		ID:          91,
		UserID:      7,
		Key:         keyValue,
		OIDCManaged: true,
		Name:        "renamed by user",
		GroupID:     &groupID,
		Status:      StatusAPIKeyDisabled,
		Quota:       12.5,
		QuotaUsed:   3,
		User:        &User{ID: 7, Status: StatusActive},
		Group:       &Group{ID: groupID, Status: StatusActive, Platform: PlatformOpenAI, Hydrated: true},
	}
	repo.keys[keyValue] = existing
	repo.listed = []APIKey{*existing}

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(91), got.ID)
	require.Equal(t, StatusAPIKeyDisabled, got.Status)
	require.Equal(t, 12.5, got.Quota)
	require.Equal(t, 0, repo.creates)
}

func TestEnsureOIDCGatewayKeyUsesNewestGroupedKey(t *testing.T) {
	groupID := int64(23)
	repo := &oidcGatewayKeyRepoStub{
		keys: make(map[string]*APIKey),
		listed: []APIKey{
			{ID: 11, UserID: 8, Status: StatusAPIKeyActive, GroupID: &groupID, Key: "newest"},
			{ID: 10, UserID: 8, Status: StatusAPIKeyActive, Key: "ungrouped"},
		},
	}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 8)
	require.NoError(t, err)
	require.NotNil(t, got.GroupID)
	require.Equal(t, groupID, *got.GroupID)
	require.Equal(t, oidcGatewayKeyName, repo.keys[got.Key].Name)
	require.Equal(t, StatusAPIKeyActive, got.Status)
	require.Equal(t, 1, repo.creates)
}

func TestEnsureOIDCGatewayKeyFallsBackToFirstAvailableGroupWithCapacity(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	user := &User{ID: 9, Status: StatusActive}
	groups := []Group{
		{ID: 31, Status: StatusActive, ActiveAccountCount: 0},
		{ID: 32, Status: StatusActive, ActiveAccountCount: 2},
	}
	svc := newOIDCGatewayKeyService(
		repo,
		&oidcGatewayUserRepoStub{user: user},
		&oidcGatewayGroupRepoStub{groups: groups},
		&oidcGatewaySubRepoStub{},
	)

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotNil(t, got.GroupID)
	require.Equal(t, int64(32), *got.GroupID)
}

func TestEnsureOIDCGatewayKeyCreatesUngroupedWhenNoGroupsExist(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 10)
	require.NoError(t, err)
	require.Nil(t, got.GroupID)
}

func TestEnsureOIDCGatewayKeyRejectsOwnerCollision(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)
	keyValue := oidcGatewayKeyPrefix + "corrupt-owner-key"
	listed := APIKey{ID: 99, UserID: 11, Key: keyValue, OIDCManaged: true, Name: oidcGatewayKeyName, Status: StatusAPIKeyActive}
	repo.listed = []APIKey{listed}
	repo.keys[keyValue] = &APIKey{
		ID:          99,
		UserID:      999,
		Key:         keyValue,
		OIDCManaged: true,
		Status:      StatusAPIKeyActive,
		User:        &User{ID: 999, Status: StatusActive},
	}

	_, err := svc.EnsureOIDCGatewayKey(context.Background(), 11)
	require.ErrorIs(t, err, ErrOIDCGatewayKeyConflict)
}

func TestEnsureOIDCGatewayKeyRetriesRandomKeyCollision(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)
	var candidates []string
	repo.createHook = func(key *APIKey) {
		candidates = append(candidates, key.Key)
	}
	repo.createErrForKey = func(*APIKey) error {
		if repo.creates == 1 {
			return ErrAPIKeyExists
		}
		return nil
	}

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, 2, repo.creates)
	require.Len(t, candidates, 2)
	require.NotEqual(t, candidates[0], candidates[1])
	require.Equal(t, candidates[1], got.Key)
}

func TestEnsureOIDCGatewayKeyReturnsConcurrentDatabaseWinner(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)
	winner := APIKey{
		ID:          151,
		UserID:      15,
		Key:         oidcGatewayKeyPrefix + "winner-from-another-instance",
		OIDCManaged: true,
		Name:        oidcGatewayKeyName,
		Status:      StatusAPIKeyActive,
		User:        &User{ID: 15, Status: StatusActive},
	}
	repo.createErrForKey = func(*APIKey) error {
		if repo.creates == 1 {
			repo.keys[winner.Key] = &winner
			repo.listed = append(repo.listed, winner)
			return ErrAPIKeyExists
		}
		return nil
	}

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), winner.UserID)
	require.NoError(t, err)
	require.Equal(t, winner.ID, got.ID)
	require.Equal(t, winner.Key, got.Key)
	require.Equal(t, 1, repo.creates)
}

func TestEnsureOIDCGatewayKeyConcurrentCallsCreateOnce(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)
	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	repo.createHook = func(*APIKey) {
		close(createStarted)
		<-releaseCreate
	}

	results := make(chan *APIKey, 2)
	errorsCh := make(chan error, 2)
	go func() {
		key, err := svc.EnsureOIDCGatewayKey(context.Background(), 13)
		results <- key
		errorsCh <- err
	}()
	<-createStarted
	go func() {
		key, err := svc.EnsureOIDCGatewayKey(context.Background(), 13)
		results <- key
		errorsCh <- err
	}()
	close(releaseCreate)

	first := <-results
	second := <-results
	require.NoError(t, <-errorsCh)
	require.NoError(t, <-errorsCh)
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, 1, repo.creates)
}

func TestEnsureOIDCGatewayKeyReusesRenamedManagedKey(t *testing.T) {
	oldKey := &APIKey{
		ID:          140,
		UserID:      14,
		Key:         oidcGatewayKeyPrefix + "random-persistent-value",
		OIDCManaged: true,
		Name:        "renamed by user",
		Status:      StatusAPIKeyDisabled,
		Quota:       7,
		User:        &User{ID: 14, Status: StatusActive},
	}
	repo := &oidcGatewayKeyRepoStub{
		keys:   map[string]*APIKey{oldKey.Key: oldKey},
		listed: []APIKey{*oldKey},
	}
	svc := newOIDCGatewayKeyService(repo, nil, nil, nil)

	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 14)
	require.NoError(t, err)
	require.Equal(t, oldKey.ID, got.ID)
	require.Equal(t, StatusAPIKeyDisabled, got.Status)
	require.Equal(t, float64(7), got.Quota)
	require.Zero(t, repo.creates)
}

func TestEnsureOIDCGatewayKeyDoesNotDerivePersistentKeyFromJWTSecret(t *testing.T) {
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	got, err := svc.EnsureOIDCGatewayKey(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got.Key, oidcGatewayKeyPrefix))
	require.Len(t, strings.TrimPrefix(got.Key, oidcGatewayKeyPrefix), 64)
}
