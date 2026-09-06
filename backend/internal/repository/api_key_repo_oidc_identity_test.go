package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newOIDCIdentityAPIKeyRepoSQLite(t *testing.T) (*apiKeyRepository, *dbent.Client) {
	t.Helper()

	dsn := fmt.Sprintf("file:api_key_repo_oidc_identity_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	// Production migration 112 owns this partial uniqueness rule. Ent's portable
	// schema describes the lookup index but cannot express the PostgreSQL partial
	// unique index, so mirror it explicitly in this SQLite repository test.
	_, err = db.Exec(`
CREATE UNIQUE INDEX idx_api_keys_oidc_identity_active_unique
ON api_keys (oidc_issuer, oidc_subject)
WHERE deleted_at IS NULL
  AND oidc_issuer IS NOT NULL
  AND oidc_subject IS NOT NULL
`)
	require.NoError(t, err)

	return &apiKeyRepository{client: client}, client
}

func TestAPIKeyRepository_BindOIDCIdentity_IsImmutableUniqueAndIdempotent(t *testing.T) {
	repo, client := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "oidc-identity-binding@test.com")

	first := &service.APIKey{
		UserID: user.ID,
		Key:    "sk-oidc-binding-first",
		Name:   "first",
		Status: service.StatusActive,
	}
	second := &service.APIKey{
		UserID: user.ID,
		Key:    "sk-oidc-binding-second",
		Name:   "second",
		Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Create(ctx, second))

	const (
		issuer  = "https://idp.example.com/realms/tabro"
		subject = "stable-subject-123"
	)
	require.NoError(t, repo.BindOIDCIdentity(ctx, first.ID, "  "+issuer+"  ", "  "+subject+"  "))

	got, err := repo.GetByOIDCIdentity(ctx, issuer, subject)
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ID)
	require.Equal(t, issuer, got.OIDCIssuer)
	require.Equal(t, subject, got.OIDCSubject)

	// PUT semantics are idempotent when the exact binding is repeated.
	require.NoError(t, repo.BindOIDCIdentity(ctx, first.ID, issuer, subject))

	// A key's identity cannot be moved after its first binding.
	err = repo.BindOIDCIdentity(ctx, first.ID, issuer, "different-subject")
	require.ErrorIs(t, err, service.ErrOIDCGatewayIdentityConflict)

	// An active identity cannot be assigned to another billing key.
	err = repo.BindOIDCIdentity(ctx, second.ID, issuer, subject)
	require.ErrorIs(t, err, service.ErrOIDCGatewayIdentityConflict)

	stillBound, err := repo.GetByOIDCIdentity(ctx, issuer, subject)
	require.NoError(t, err)
	require.Equal(t, first.ID, stillBound.ID)

	// Soft deletion releases the partial unique binding for an explicitly
	// provisioned replacement key.
	require.NoError(t, repo.Delete(ctx, first.ID))
	require.NoError(t, repo.BindOIDCIdentity(ctx, second.ID, issuer, subject))

	replacement, err := repo.GetByOIDCIdentity(ctx, issuer, subject)
	require.NoError(t, err)
	require.Equal(t, second.ID, replacement.ID)
}

func TestAPIKeyRepository_BindOIDCIdentity_RejectsInvalidOrMissingKey(t *testing.T) {
	repo, _ := newOIDCIdentityAPIKeyRepoSQLite(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		id      int64
		issuer  string
		subject string
	}{
		{name: "invalid id", id: 0, issuer: "https://idp.example.com", subject: "sub"},
		{name: "blank issuer", id: 1, issuer: " ", subject: "sub"},
		{name: "blank subject", id: 1, issuer: "https://idp.example.com", subject: " "},
		{name: "missing key", id: 999999, issuer: "https://idp.example.com", subject: "sub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.BindOIDCIdentity(ctx, tc.id, tc.issuer, tc.subject)
			require.ErrorIs(t, err, service.ErrOIDCGatewayIdentityConflict)
		})
	}
}
