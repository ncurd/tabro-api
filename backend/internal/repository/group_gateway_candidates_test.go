package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newGatewayCandidateGroupRepoSQLite(t *testing.T) (*groupRepository, *dbent.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:group_gateway_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	return &groupRepository{client: client, sql: db}, client
}

func TestGatewayGroupCandidatesRespectPublicAndExplicitScopes(t *testing.T) {
	repo, client := newGatewayCandidateGroupRepoSQLite(t)
	ctx := context.Background()
	create := func(name, platform, billing string, exclusive bool, sortOrder int) *dbent.Group {
		g, err := client.Group.Create().SetName(name).SetPlatform(platform).
			SetSubscriptionType(billing).SetIsExclusive(exclusive).SetSortOrder(sortOrder).Save(ctx)
		require.NoError(t, err)
		return g
	}
	shared := create("shared", service.PlatformAll, service.SubscriptionTypeStandard, false, -100)
	public := create("public", service.PlatformOpenAI, service.SubscriptionTypeStandard, false, 0)
	private := create("private", service.PlatformAnthropic, service.SubscriptionTypeStandard, true, -200)
	subscription := create("paid", service.PlatformGemini, service.SubscriptionTypeSubscription, false, -200)
	inactive := create("inactive", service.PlatformOpenAI, service.SubscriptionTypeStandard, false, -200)
	_, err := client.Group.UpdateOne(inactive).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	deleted := create("deleted", service.PlatformOpenAI, service.SubscriptionTypeStandard, false, -200)
	_, err = client.Group.UpdateOne(deleted).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	// Historical invalid universal rows cannot defeat access restrictions.
	invalid := create("invalid shared", service.PlatformAll, service.SubscriptionTypeStandard, true, -200)

	publicCandidates, err := repo.ListGatewayRoutingCandidates(ctx, nil, true)
	require.NoError(t, err)
	require.Len(t, publicCandidates, 2)
	require.Equal(t, shared.ID, publicCandidates[0].Group.ID)
	require.Equal(t, public.ID, publicCandidates[1].Group.ID)
	require.True(t, publicCandidates[0].Group.Hydrated)
	require.Empty(t, publicCandidates[0].Accounts, "the shared group must not inherit other groups' accounts")

	explicit, err := repo.ListGatewayRoutingCandidates(ctx, []int64{public.ID, private.ID, subscription.ID, inactive.ID, deleted.ID, invalid.ID}, false)
	require.NoError(t, err)
	ids := make([]int64, 0, len(explicit))
	for _, candidate := range explicit {
		ids = append(ids, candidate.Group.ID)
	}
	require.ElementsMatch(t, []int64{private.ID, subscription.ID, public.ID}, ids)

	intersection, err := repo.ListGatewayRoutingCandidates(ctx, []int64{private.ID, public.ID}, true)
	require.NoError(t, err)
	require.Len(t, intersection, 1)
	require.Equal(t, public.ID, intersection[0].Group.ID)
	none, err := repo.ListGatewayRoutingCandidates(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, none, "missing explicit scope must never grant all private groups")
}

func TestGatewayGroupCandidatesOnlyIncludeLinkedUsableProviderAccounts(t *testing.T) {
	repo, client := newGatewayCandidateGroupRepoSQLite(t)
	ctx := context.Background()
	g, err := client.Group.Create().SetName("all models").SetPlatform(service.PlatformAll).Save(ctx)
	require.NoError(t, err)
	create := func(name, platform string, priority int) *dbent.AccountCreate {
		return client.Account.Create().SetName(name).SetPlatform(platform).SetType(service.AccountTypeAPIKey).
			SetStatus(service.StatusActive).SetSchedulable(true).SetPriority(priority).AddGroupIDs(g.ID)
	}
	claude, err := create("claude", service.PlatformAnthropic, 10).Save(ctx)
	require.NoError(t, err)
	openai, err := create("openai", service.PlatformOpenAI, 1).
		SetCredentials(map[string]any{"model_mapping": map[string]any{"custom-alias": "gpt-4.1"}}).Save(ctx)
	require.NoError(t, err)
	_, err = create("stopped", service.PlatformOpenAI, 0).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	_, err = create("disabled", service.PlatformOpenAI, 0).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	_, err = create("limited", service.PlatformOpenAI, 0).SetRateLimitResetAt(time.Now().Add(time.Hour)).Save(ctx)
	require.NoError(t, err)
	_, err = create("temporarily stopped", service.PlatformGemini, 0).SetTempUnschedulableUntil(time.Now().Add(time.Hour)).Save(ctx)
	require.NoError(t, err)
	_, err = create("expired", service.PlatformOpenAI, 0).SetAutoPauseOnExpired(true).SetExpiresAt(time.Now().Add(-time.Hour)).Save(ctx)
	require.NoError(t, err)
	_, err = create("deleted", service.PlatformOpenAI, 0).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.Create().SetName("not linked").SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).SetStatus(service.StatusActive).SetSchedulable(true).Save(ctx)
	require.NoError(t, err)

	candidates, err := repo.ListGatewayRoutingCandidates(ctx, []int64{g.ID}, false)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Len(t, candidates[0].Accounts, 2)
	require.Equal(t, openai.ID, candidates[0].Accounts[0].ID)
	require.Equal(t, claude.ID, candidates[0].Accounts[1].ID)
	require.Equal(t, []int64{g.ID}, candidates[0].Accounts[0].GroupIDs)
	require.Equal(t, "gpt-4.1", candidates[0].Accounts[0].GetModelMapping()["custom-alias"])

	// A universal group does not bypass the administrator's OAuth restriction.
	_, err = client.Group.UpdateOne(g).SetRequireOauthOnly(true).Save(ctx)
	require.NoError(t, err)
	candidates, err = repo.ListGatewayRoutingCandidates(ctx, []int64{g.ID}, false)
	require.NoError(t, err)
	require.Empty(t, candidates[0].Accounts)
}

func TestUniversalDefaultMigrationIsIdempotentAndPreservesExistingPrivatePools(t *testing.T) {
	for _, name := range []string{"enterprise private", "默认分组"} {
		t.Run(name, func(t *testing.T) {
			repo, client := newGatewayCandidateGroupRepoSQLite(t)
			ctx := context.Background()
			private, err := client.Group.Create().SetName(name).SetPlatform(service.PlatformOpenAI).
				SetIsExclusive(true).SetSubscriptionType(service.SubscriptionTypeSubscription).
				SetRateMultiplier(3).Save(ctx)
			require.NoError(t, err)
			_, err = client.Account.Create().SetName("private upstream").SetPlatform(service.PlatformOpenAI).
				SetType(service.AccountTypeAPIKey).AddGroupIDs(private.ID).Save(ctx)
			require.NoError(t, err)

			migration, err := migrations.FS.ReadFile("131_seed_universal_default_group.sql")
			require.NoError(t, err)
			// Only translate PostgreSQL's timestamp function for this portable
			// repository test; the INSERT and its permission/name guards are real.
			statement := strings.ReplaceAll(string(migration), "NOW()", "CURRENT_TIMESTAMP")
			_, err = repo.sql.ExecContext(ctx, statement)
			require.NoError(t, err)
			_, err = repo.sql.ExecContext(ctx, statement)
			require.NoError(t, err)

			shared, err := client.Group.Query().Where(group.PlatformEQ(service.PlatformAll)).WithAccounts().Only(ctx)
			require.NoError(t, err)
			if name == "默认分组" {
				require.Equal(t, "默认分组（全模型）", shared.Name)
			} else {
				require.Equal(t, "默认分组", shared.Name)
			}
			require.False(t, shared.IsExclusive)
			require.Equal(t, service.SubscriptionTypeStandard, shared.SubscriptionType)
			require.Equal(t, service.StatusActive, shared.Status)
			require.Equal(t, -100, shared.SortOrder)
			require.True(t, shared.AllowMessagesDispatch)
			require.Empty(t, shared.Edges.Accounts, "installing the default must never publish a private upstream")
			unchanged, err := client.Group.Get(ctx, private.ID)
			require.NoError(t, err)
			require.Equal(t, private.Name, unchanged.Name)
			require.True(t, unchanged.IsExclusive)
			require.Equal(t, service.SubscriptionTypeSubscription, unchanged.SubscriptionType)
			require.Equal(t, private.RateMultiplier, unchanged.RateMultiplier)
			count, err := client.Group.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, count)
		})
	}
}
