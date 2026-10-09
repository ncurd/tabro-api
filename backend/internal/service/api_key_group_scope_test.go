//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type keyScopeGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *keyScopeGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	if g := r.groups[id]; g != nil {
		return g, nil
	}
	return nil, ErrGroupNotFound
}

type keyScopeRepo struct {
	APIKeyRepository
	key     *APIKey
	updates int
}

func (r *keyScopeRepo) GetByID(context.Context, int64) (*APIKey, error) {
	k := *r.key
	return &k, nil
}
func (r *keyScopeRepo) Update(_ context.Context, key *APIKey) error {
	r.key, r.updates = key, r.updates+1
	return nil
}
func (r *keyScopeRepo) Create(_ context.Context, key *APIKey) error {
	r.key = key
	return nil
}

func scopeTestService() (*APIKeyService, *keyScopeRepo) {
	user := &User{ID: 7, Status: StatusActive}
	repo := &keyScopeRepo{key: &APIKey{ID: 9, UserID: 7, Key: "scope-key", GroupID: keyScopeInt64(1), Group: &Group{ID: 1}, Status: StatusActive}}
	groups := &keyScopeGroupRepo{groups: map[int64]*Group{
		1: {ID: 1, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
		2: {ID: 2, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
		3: {ID: 3, Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
		4: {ID: 4, Status: "inactive", SubscriptionType: SubscriptionTypeStandard},
	}}
	return newOIDCGatewayKeyService(repo, &oidcGatewayUserRepoStub{user: user}, groups, nil), repo
}

func keyScopeInt64(v int64) *int64 { return &v }

func TestAPIKeyScopeSelectedValidatesAllGroups(t *testing.T) {
	svc, repo := scopeTestService()
	for _, ids := range [][]int64{{}, {1, 3}, {1, 4}, {1, -1}, {1, 99}} {
		_, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "selected", GroupScope: APIKeyGroupScopeSelected, GroupIDs: ids})
		require.Error(t, err)
	}
	require.Equal(t, 0, repo.updates)
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "selected", GroupScope: APIKeyGroupScopeSelected, GroupIDs: []int64{2, 1, 2}})
	require.NoError(t, err)
	require.Nil(t, key.GroupID)
	require.Equal(t, []int64{2, 1}, key.GroupIDs)
}

func TestAPIKeyScopeCreateDefaultAndLegacy(t *testing.T) {
	svc, _ := scopeTestService()
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "default"})
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopePublic, key.GroupScope)
	require.Nil(t, key.GroupID)
	key, err = svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "legacy", GroupID: keyScopeInt64(1)})
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopeSingle, key.GroupScope)
	require.Equal(t, int64(1), *key.GroupID)
}

func TestAPIKeyScopeSimpleModePreservesOrdinaryDefault(t *testing.T) {
	svc, _ := scopeTestService()
	svc.cfg.RunMode = config.RunModeSimple
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "simple-default"})
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopeSingle, key.GroupScope)
	require.Nil(t, key.GroupID)
	for _, req := range []CreateAPIKeyRequest{
		{Name: "explicit-public", GroupScope: APIKeyGroupScopePublic},
		{Name: "explicit-selected", GroupScope: APIKeyGroupScopeSelected, GroupIDs: []int64{1, 2}},
	} {
		key, err = svc.Create(context.Background(), 7, req)
		require.NoError(t, err)
		require.Equal(t, req.GroupScope, key.GroupScope)
	}
	// Auth-backed identities remain public and can never inherit the simplified
	// installation's ungrouped/global ordinary-key permissions.
	repo := &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}
	svc = newOIDCGatewayKeyService(repo, nil, nil, nil)
	svc.cfg.RunMode = config.RunModeSimple
	key, err = svc.EnsureOIDCGatewayKey(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopePublic, key.GroupScope)
}

func TestAPIKeyScopeSelectedHonorsExistingPrivateEntitlement(t *testing.T) {
	svc, _ := scopeTestService()
	svc.userRepo.(*oidcGatewayUserRepoStub).user.AllowedGroups = []int64{3}
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "permitted-private", GroupScope: APIKeyGroupScopeSelected, GroupIDs: []int64{3}})
	require.NoError(t, err)
	require.Equal(t, []int64{3}, key.GroupIDs)
	groups := svc.groupRepo.(*keyScopeGroupRepo)
	groups.groups[5] = &Group{ID: 5, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}
	_, err = svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "unsubscribed", GroupScope: APIKeyGroupScopeSelected, GroupIDs: []int64{5}})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestAPIKeyScopeUpdateClearsBindingAndSupportsLegacy(t *testing.T) {
	svc, repo := scopeTestService()
	public := APIKeyGroupScopePublic
	repo.key.IPWhitelist = []string{"192.0.2.0/24"}
	repo.key.IPBlacklist = []string{"192.0.2.10"}
	key, err := svc.Update(context.Background(), 9, 7, UpdateAPIKeyRequest{GroupScope: &public})
	require.NoError(t, err)
	require.Nil(t, key.GroupID)
	require.Nil(t, key.Group)
	require.Equal(t, APIKeyGroupScopePublic, repo.key.GroupScope)
	require.Equal(t, []string{"192.0.2.0/24"}, key.IPWhitelist)
	require.Equal(t, []string{"192.0.2.10"}, key.IPBlacklist)
	key, err = svc.Update(context.Background(), 9, 7, UpdateAPIKeyRequest{GroupID: keyScopeInt64(2)})
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopeSingle, key.GroupScope)
	require.Equal(t, int64(2), *key.GroupID)
	selected := APIKeyGroupScopeSelected
	empty := []int64{}
	_, err = svc.Update(context.Background(), 9, 7, UpdateAPIKeyRequest{GroupScope: &selected, GroupIDs: &empty})
	require.Error(t, err)
	key, err = svc.Update(context.Background(), 9, 7, UpdateAPIKeyRequest{})
	require.NoError(t, err)
	require.Equal(t, APIKeyGroupScopeSingle, key.GroupScope)
}

func TestAPIKeyScopeCacheRoundTripIsolation(t *testing.T) {
	svc, _ := scopeTestService()
	key := &APIKey{ID: 9, UserID: 7, User: &User{ID: 7}, GroupScope: APIKeyGroupScopeSelected, GroupIDs: []int64{1, 2}, OIDCManaged: true, AuthBillingOnly: true}
	snapshot := svc.snapshotFromAPIKey(key)
	key.GroupIDs[0] = 99
	require.Equal(t, []int64{1, 2}, snapshot.GroupIDs)
	restored := svc.snapshotToAPIKey("scope-key", snapshot)
	require.Equal(t, APIKeyGroupScopeSelected, restored.GroupScope)
	require.True(t, restored.AuthBillingOnly)
	restored.GroupIDs[0] = 88
	require.Equal(t, []int64{1, 2}, snapshot.GroupIDs)
	require.Equal(t, APIKeyGroupScopeSingle, (&APIKey{}).EffectiveGroupScope())
}
