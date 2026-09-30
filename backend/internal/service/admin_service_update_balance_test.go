//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type balanceUserRepoStub struct {
	*userRepoStub
	updateErr error
	updated   []*User
}

func (s *balanceUserRepoStub) Update(ctx context.Context, user *User) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if user == nil {
		return nil
	}
	clone := *user
	s.updated = append(s.updated, &clone)
	if s.userRepoStub != nil {
		s.userRepoStub.user = &clone
	}
	return nil
}

type balanceRedeemRepoStub struct {
	*redeemRepoStub
	created []*RedeemCode
}

func (s *balanceRedeemRepoStub) Create(ctx context.Context, code *RedeemCode) error {
	if code == nil {
		return nil
	}
	clone := *code
	s.created = append(s.created, &clone)
	return nil
}

type authCacheInvalidatorStub struct {
	userIDs  []int64
	groupIDs []int64
	keys     []string
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

func TestAdminService_UpdateUserBalance_InvalidatesAuthCache(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       redeemRepo,
		authCacheInvalidator: invalidator,
	}

	_, err := svc.UpdateUserBalance(context.Background(), 7, 5, "add", "")
	require.NoError(t, err)
	require.Equal(t, []int64{7}, invalidator.userIDs)
	require.Len(t, redeemRepo.created, 1)
}

func TestAdminService_UpdateUserBalance_NoChangeNoInvalidate(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       redeemRepo,
		authCacheInvalidator: invalidator,
	}

	_, err := svc.UpdateUserBalance(context.Background(), 7, 10, "set", "")
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs)
	require.Empty(t, redeemRepo.created)
}

func TestAdminService_UpdateUserPasswordIncrementsTokenVersion(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{
		ID:           8,
		Role:         RoleUser,
		Status:       StatusActive,
		TokenVersion: 7,
	}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	svc := &adminServiceImpl{userRepo: repo}

	updated, err := svc.UpdateUser(context.Background(), 8, &UpdateUserInput{Password: "new-strong-password"})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, int64(8), updated.TokenVersion)
	require.True(t, updated.CheckPassword("new-strong-password"))
	require.Len(t, repo.updated, 1)
	require.Equal(t, int64(8), repo.updated[0].TokenVersion)
}

func TestAdminService_UpdateUserAPIOnlyRevokesExistingWebTokens(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{ID: 9, Role: RoleUser, Status: StatusActive, TokenVersion: 3}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}
	apiOnly := true

	updated, err := svc.UpdateUser(context.Background(), 9, &UpdateUserInput{APIOnly: &apiOnly})
	require.NoError(t, err)
	require.True(t, updated.APIOnly)
	require.Equal(t, int64(4), updated.TokenVersion)
	require.Equal(t, []int64{9}, invalidator.userIDs)

	apiOnly = false
	updated, err = svc.UpdateUser(context.Background(), 9, &UpdateUserInput{APIOnly: &apiOnly})
	require.NoError(t, err)
	require.False(t, updated.APIOnly)
	require.Equal(t, int64(5), updated.TokenVersion)
}

func TestAdminService_UpdateUserCannotMakeAdminAPIOnly(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 1, Role: RoleAdmin, Status: StatusActive}}}
	svc := &adminServiceImpl{userRepo: repo}
	apiOnly := true

	_, err := svc.UpdateUser(context.Background(), 1, &UpdateUserInput{APIOnly: &apiOnly})
	require.Error(t, err)
	require.Empty(t, repo.updated)
}
