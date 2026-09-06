package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type adminOIDCIdentityRepoStub struct {
	APIKeyRepository
	key       *APIKey
	getErr    error
	bindErr   error
	getCalls  int
	bindCalls int
	boundID   int64
	boundIss  string
	boundSub  string
}

func (r *adminOIDCIdentityRepoStub) GetByID(_ context.Context, id int64) (*APIKey, error) {
	r.getCalls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.key == nil || r.key.ID != id {
		return nil, ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}

func (r *adminOIDCIdentityRepoStub) BindOIDCIdentity(_ context.Context, id int64, issuer, subject string) error {
	r.bindCalls++
	r.boundID = id
	r.boundIss = issuer
	r.boundSub = subject
	return r.bindErr
}

func (r *adminOIDCIdentityRepoStub) GetByOIDCIdentity(context.Context, string, string) (*APIKey, error) {
	panic("unexpected GetByOIDCIdentity call")
}

func TestAdminService_AdminBindAPIKeyOIDCIdentity_Success(t *testing.T) {
	repo := &adminOIDCIdentityRepoStub{
		key: &APIKey{ID: 42, UserID: 7, Key: "sk-billing", Name: "billing", Status: StatusActive},
	}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	got, err := svc.AdminBindAPIKeyOIDCIdentity(
		context.Background(),
		42,
		"  https://idp.example.com/realms/tabro  ",
		"  stable-subject-123  ",
	)

	require.NoError(t, err)
	require.Equal(t, 1, repo.getCalls)
	require.Equal(t, 1, repo.bindCalls)
	require.Equal(t, int64(42), repo.boundID)
	require.Equal(t, "https://idp.example.com/realms/tabro", repo.boundIss)
	require.Equal(t, "stable-subject-123", repo.boundSub)
	require.Equal(t, repo.boundIss, got.OIDCIssuer)
	require.Equal(t, repo.boundSub, got.OIDCSubject)
}

func TestAdminService_AdminBindAPIKeyOIDCIdentity_ValidatesInputBeforeLookup(t *testing.T) {
	tooLong := strings.Repeat("a", 513)
	tests := []struct {
		name    string
		keyID   int64
		issuer  string
		subject string
		reason  string
	}{
		{name: "nonpositive key id", keyID: 0, issuer: "https://idp.example.com", subject: "sub", reason: "INVALID_OIDC_IDENTITY"},
		{name: "blank issuer", keyID: 1, issuer: "  ", subject: "sub", reason: "INVALID_OIDC_IDENTITY"},
		{name: "blank subject", keyID: 1, issuer: "https://idp.example.com", subject: "  ", reason: "INVALID_OIDC_IDENTITY"},
		{name: "issuer too long", keyID: 1, issuer: "https://" + tooLong + ".example.com", subject: "sub", reason: "INVALID_OIDC_IDENTITY"},
		{name: "subject too long", keyID: 1, issuer: "https://idp.example.com", subject: tooLong, reason: "INVALID_OIDC_IDENTITY"},
		{name: "relative issuer", keyID: 1, issuer: "/realms/tabro", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "unsupported scheme", keyID: 1, issuer: "ftp://idp.example.com", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "missing hostname", keyID: 1, issuer: "https://:8443/realms/tabro", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "userinfo", keyID: 1, issuer: "https://user@idp.example.com", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "query", keyID: 1, issuer: "https://idp.example.com/realms/tabro?tenant=one", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "empty query", keyID: 1, issuer: "https://idp.example.com/realms/tabro?", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
		{name: "fragment", keyID: 1, issuer: "https://idp.example.com/realms/tabro#issuer", subject: "sub", reason: "INVALID_OIDC_ISSUER"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &adminOIDCIdentityRepoStub{key: &APIKey{ID: 1}}
			svc := &adminServiceImpl{apiKeyRepo: repo}

			got, err := svc.AdminBindAPIKeyOIDCIdentity(context.Background(), tc.keyID, tc.issuer, tc.subject)

			require.Nil(t, got)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, tc.reason, infraerrors.Reason(err))
			require.Zero(t, repo.getCalls)
			require.Zero(t, repo.bindCalls)
		})
	}
}

func TestAdminService_AdminBindAPIKeyOIDCIdentity_PropagatesLookupAndBindingErrors(t *testing.T) {
	t.Run("key not found", func(t *testing.T) {
		repo := &adminOIDCIdentityRepoStub{getErr: ErrAPIKeyNotFound}
		svc := &adminServiceImpl{apiKeyRepo: repo}

		got, err := svc.AdminBindAPIKeyOIDCIdentity(context.Background(), 42, "https://idp.example.com", "sub")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrAPIKeyNotFound)
		require.Zero(t, repo.bindCalls)
	})

	t.Run("immutable or unique conflict", func(t *testing.T) {
		repo := &adminOIDCIdentityRepoStub{
			key:     &APIKey{ID: 42},
			bindErr: ErrOIDCGatewayIdentityConflict,
		}
		svc := &adminServiceImpl{apiKeyRepo: repo}

		got, err := svc.AdminBindAPIKeyOIDCIdentity(context.Background(), 42, "https://idp.example.com", "sub")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrOIDCGatewayIdentityConflict)
		require.Equal(t, 1, repo.bindCalls)
	})

	t.Run("repository does not implement identity operations", func(t *testing.T) {
		repo := &adminOIDCUnsupportedRepo{
			key: &APIKey{ID: 42},
		}
		svc := &adminServiceImpl{apiKeyRepo: repo}

		got, err := svc.AdminBindAPIKeyOIDCIdentity(context.Background(), 42, "https://idp.example.com", "sub")

		require.Nil(t, got)
		require.Error(t, err)
		require.Equal(t, http.StatusInternalServerError, infraerrors.Code(err))
		require.Equal(t, "OIDC_IDENTITY_REPOSITORY_UNAVAILABLE", infraerrors.Reason(err))
	})
}

type adminOIDCUnsupportedRepo struct {
	APIKeyRepository
	key *APIKey
}

func (r *adminOIDCUnsupportedRepo) GetByID(_ context.Context, _ int64) (*APIKey, error) {
	if r.key == nil {
		return nil, errors.New("missing key")
	}
	clone := *r.key
	return &clone, nil
}
