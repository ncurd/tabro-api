//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminUniversalGroupMustRemainPublicStandard(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exclusive    bool
		subscription string
	}{
		{"exclusive", true, SubscriptionTypeStandard},
		{"subscription", false, SubscriptionTypeSubscription},
	} {
		t.Run("create "+tc.name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name: "默认分组", Platform: PlatformAll, IsExclusive: tc.exclusive, SubscriptionType: tc.subscription,
			})
			require.Error(t, err)
			require.Equal(t, "INVALID_UNIVERSAL_GROUP", infraerrors.Reason(err))
			require.Nil(t, repo.created)
		})
		t.Run("update "+tc.name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{getByID: &Group{ID: 3, Platform: PlatformAll, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard}}
			svc := &adminServiceImpl{groupRepo: repo}
			exclusive := tc.exclusive
			_, err := svc.UpdateGroup(context.Background(), 3, &UpdateGroupInput{IsExclusive: &exclusive, SubscriptionType: tc.subscription})
			require.Error(t, err)
			require.Equal(t, "INVALID_UNIVERSAL_GROUP", infraerrors.Reason(err))
			require.Nil(t, repo.updated)
		})
	}
	// Existing per-platform subscriptions and private groups remain valid.
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "private", Platform: PlatformOpenAI, IsExclusive: true, SubscriptionType: SubscriptionTypeSubscription})
	require.NoError(t, err)
	require.True(t, repo.created.IsExclusive)
}

func TestAdminUniversalGroupKeepsMessagesDispatchConfiguration(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	g, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "默认分组", Platform: PlatformAll, RateMultiplier: 1, AllowMessagesDispatch: true,
		DefaultMappedModel: "gpt-4.1", RequireOAuthOnly: true, RequirePrivacySet: true,
	})
	require.NoError(t, err)
	require.False(t, g.IsExclusive)
	require.Equal(t, SubscriptionTypeStandard, g.SubscriptionType)
	require.True(t, g.AllowMessagesDispatch)
	require.Equal(t, "gpt-4.1", g.DefaultMappedModel)
	require.True(t, g.RequireOAuthOnly)
	require.True(t, g.RequirePrivacySet)

	g.ID = 4
	repo.getByID = g
	dispatch := false
	_, err = svc.UpdateGroup(context.Background(), g.ID, &UpdateGroupInput{AllowMessagesDispatch: &dispatch})
	require.NoError(t, err)
	require.False(t, repo.updated.AllowMessagesDispatch, "an explicit administrator disable must remain disabled")
}

type universalCopyGroupRepo struct {
	groupRepoStubForAdmin
	sources map[int64]*Group
	bound   []int64
	cleared bool
}

func (r *universalCopyGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	return r.sources[id], nil
}
func (r *universalCopyGroupRepo) GetAccountIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	return []int64{51, 52}, nil
}
func (r *universalCopyGroupRepo) BindAccountsToGroup(_ context.Context, _ int64, ids []int64) error {
	r.bound = append([]int64(nil), ids...)
	return nil
}
func (r *universalCopyGroupRepo) DeleteAccountGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	r.cleared = true
	return 0, nil
}

func TestAdminUniversalGroupCopiesOnlyExplicitlySelectedProviderGroups(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
			sources := map[int64]*Group{
				1: {ID: 1, Platform: PlatformOpenAI, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard},
				2: {ID: 2, Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard},
			}
			repo := &universalCopyGroupRepo{sources: sources}
			svc := &adminServiceImpl{groupRepo: repo}
			var err error
			if update {
				repo.getByID = &Group{ID: 4, Platform: PlatformAll, SubscriptionType: SubscriptionTypeStandard}
				_, err = svc.UpdateGroup(context.Background(), 4, &UpdateGroupInput{CopyAccountsFromGroupIDs: []int64{1, 2}})
				require.True(t, repo.cleared)
			} else {
				_, err = svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "shared", Platform: PlatformAll, CopyAccountsFromGroupIDs: []int64{1, 2}})
			}
			require.NoError(t, err)
			require.Equal(t, []int64{51, 52}, repo.bound)
			require.True(t, sources[1].IsExclusive, "copying chosen accounts never publicizes the source group")
		})
	}

	// Single-provider groups retain their existing cross-platform copy guard.
	repo := &universalCopyGroupRepo{sources: map[int64]*Group{2: {ID: 2, Platform: PlatformAnthropic}}}
	_, err := (&adminServiceImpl{groupRepo: repo}).CreateGroup(context.Background(), &CreateGroupInput{Name: "openai", Platform: PlatformOpenAI, CopyAccountsFromGroupIDs: []int64{2}})
	require.ErrorContains(t, err, "platform mismatch")
	require.Nil(t, repo.bound)
}

type universalAccountRepo struct {
	AccountRepository
	account     *Account
	created     bool
	updated     bool
	bound       bool
	bulkUpdated bool
}

func (r *universalAccountRepo) Create(_ context.Context, _ *Account) error {
	r.created = true
	return nil
}
func (r *universalAccountRepo) Update(_ context.Context, _ *Account) error {
	r.updated = true
	return nil
}
func (r *universalAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}
func (r *universalAccountRepo) GetByIDs(_ context.Context, _ []int64) ([]*Account, error) {
	return []*Account{r.account}, nil
}
func (r *universalAccountRepo) BindGroups(_ context.Context, _ int64, _ []int64) error {
	r.bound = true
	return nil
}
func (r *universalAccountRepo) BulkUpdate(_ context.Context, _ []int64, _ AccountBulkUpdate) (int64, error) {
	r.bulkUpdated = true
	return 1, nil
}

func TestUniversalOAuthOnlyBindingFailsBeforeAccountWrites(t *testing.T) {
	for _, path := range []string{"account create", "account update", "admin create", "admin update", "admin bulk update"} {
		t.Run(path, func(t *testing.T) {
			groups := &groupRepoStubForAdmin{getByID: &Group{ID: 8, Name: "all oauth", Platform: PlatformAll, SubscriptionType: SubscriptionTypeStandard, RequireOAuthOnly: true}}
			accounts := &universalAccountRepo{account: &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}
			ids := []int64{8}
			var err error
			switch path {
			case "account create":
				_, err = NewAccountService(accounts, groups).Create(context.Background(), CreateAccountRequest{Name: "key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: ids})
			case "account update":
				_, err = NewAccountService(accounts, groups).Update(context.Background(), 9, UpdateAccountRequest{GroupIDs: &ids})
			case "admin create":
				_, err = (&adminServiceImpl{accountRepo: accounts, groupRepo: groups}).CreateAccount(context.Background(), &CreateAccountInput{Name: "key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: ids, SkipMixedChannelCheck: true})
			case "admin update":
				_, err = (&adminServiceImpl{accountRepo: accounts, groupRepo: groups}).UpdateAccount(context.Background(), 9, &UpdateAccountInput{GroupIDs: &ids, SkipMixedChannelCheck: true})
			case "admin bulk update":
				_, err = (&adminServiceImpl{accountRepo: accounts, groupRepo: groups}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{9}, GroupIDs: &ids, SkipMixedChannelCheck: true})
			}
			require.Error(t, err)
			require.Equal(t, "ACCOUNT_OAUTH_REQUIRED", infraerrors.Reason(err))
			require.False(t, accounts.created)
			require.False(t, accounts.updated)
			require.False(t, accounts.bulkUpdated)
			require.False(t, accounts.bound)
		})
	}
}

func TestUniversalRoutingPlatformCannotBecomeAnUpstreamAccount(t *testing.T) {
	for _, admin := range []bool{false, true} {
		accounts := &universalAccountRepo{}
		var err error
		if admin {
			_, err = (&adminServiceImpl{accountRepo: accounts}).CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformAll})
		} else {
			_, err = NewAccountService(accounts, nil).Create(context.Background(), CreateAccountRequest{Platform: PlatformAll})
		}
		require.Equal(t, "INVALID_ACCOUNT_PLATFORM", infraerrors.Reason(err))
		require.False(t, accounts.created)
	}
}
