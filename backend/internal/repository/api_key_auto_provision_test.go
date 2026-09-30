package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func createAutoProvisionPublicRoute(t *testing.T, repo *apiKeyRepository) int64 {
	t.Helper()
	ctx := context.Background()
	group, err := repo.client.Group.Create().
		SetName("Public model route").
		SetStatus(service.StatusActive).
		SetIsExclusive(false).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetSortOrder(1).
		Save(ctx)
	require.NoError(t, err)
	_, err = repo.client.Account.Create().
		SetName("Available upstream").
		SetPlatform("anthropic").
		SetType("api_key").
		SetStatus(service.StatusActive).
		AddGroupIDs(group.ID).
		Save(ctx)
	require.NoError(t, err)
	return group.ID
}

func TestAutoProvisionOIDCGatewayIdentityCreatesAtomicallyAndReusesIdentity(t *testing.T) {
	repo, client := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()
	const issuer, subject = "https://idp.example.test", "stable-subject"

	// Without a usable public route the transaction must not leave a user/key.
	_, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:no-route")
	require.ErrorIs(t, err, service.ErrGatewayOIDCUnavailable)
	users, err := client.User.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, users)

	groupID := createAutoProvisionPublicRoute(t, repo)
	first, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:first")
	require.NoError(t, err)
	require.NotNil(t, first.User)
	require.True(t, first.User.APIOnly)
	require.True(t, first.User.IsActive())
	require.True(t, first.OIDCManaged)
	require.True(t, first.AuthBillingOnly)
	require.Equal(t, service.StatusAPIKeyAuthBillingOnly, first.Status)
	require.Equal(t, issuer, first.OIDCIssuer)
	require.Equal(t, subject, first.OIDCSubject)
	require.NotNil(t, first.GroupID)
	require.Equal(t, groupID, *first.GroupID)
	require.NotNil(t, first.Group)
	require.NotEqual(t, "stable-subject", first.User.Email)

	second, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:second")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	users, err = client.User.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, users)
	keys, err := client.APIKey.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, keys)
}

func TestAutoProvisionOIDCGatewayIdentityNeverRecreatesRevokedBinding(t *testing.T) {
	repo, client := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()
	createAutoProvisionPublicRoute(t, repo)
	const issuer, subject = "https://idp.example.test", "revoked-subject"
	key, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:revoked")
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, key.ID))

	_, err = repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:replacement")
	require.ErrorIs(t, err, service.ErrGatewayOIDCIdentityNotBound)
	users, err := client.User.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, users)
	keys, err := client.APIKey.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, keys)
}

func TestAutoProvisionOIDCGatewayIdentityDoesNotReplaceDisabledUser(t *testing.T) {
	repo, client := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()
	createAutoProvisionPublicRoute(t, repo)
	const issuer, subject = "https://idp.example.test", "disabled-user-subject"
	key, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:disabled-user")
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(key.UserID).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)

	resolved, err := repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:replacement")
	require.NoError(t, err)
	require.Equal(t, key.ID, resolved.ID)
	require.NotNil(t, resolved.User)
	require.False(t, resolved.User.IsActive())
	users, err := client.User.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, users)
}

func TestAutoProvisionOIDCGatewayIdentityConcurrentRequestsConverge(t *testing.T) {
	repo, client := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()
	createAutoProvisionPublicRoute(t, repo)
	const issuer, subject = "https://idp.example.test", "parallel-subject"
	results := make([]*service.APIKey, 8)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = repo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, "oidc-internal:parallel-"+string(rune('a'+index)))
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, results[0].ID, results[i].ID)
	}
	users, err := client.User.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, users)
}
