//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type oidcProvisionRepoStub struct {
	*oidcGatewayKeyRepoStub
	wrongOwner bool
	bindCalls  int
}

func (r *oidcProvisionRepoStub) GetByID(_ context.Context, id int64) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range r.keys {
		if key.ID == id {
			clone := *key
			if r.wrongOwner {
				clone.UserID++
			}
			return &clone, nil
		}
	}
	return nil, ErrAPIKeyNotFound
}

func (r *oidcProvisionRepoStub) BindOIDCIdentity(_ context.Context, id int64, issuer, subject string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range r.keys {
		if key.OIDCIssuer == issuer && key.OIDCSubject == subject && key.ID != id {
			return ErrOIDCGatewayIdentityConflict
		}
	}
	for _, key := range r.keys {
		if key.ID != id {
			continue
		}
		if key.OIDCIssuer != "" && (key.OIDCIssuer != issuer || key.OIDCSubject != subject) {
			return ErrOIDCGatewayIdentityConflict
		}
		key.OIDCIssuer, key.OIDCSubject = issuer, subject
		r.bindCalls++
		return nil
	}
	return ErrAPIKeyNotFound
}

func (r *oidcProvisionRepoStub) GetByOIDCIdentity(_ context.Context, issuer, subject string) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range r.keys {
		if key.OIDCIssuer == issuer && key.OIDCSubject == subject {
			clone := *key
			return &clone, nil
		}
	}
	return nil, ErrAPIKeyNotFound
}

func newOIDCProvisionService(user *User) (*APIKeyService, *oidcProvisionRepoStub) {
	repo := &oidcProvisionRepoStub{oidcGatewayKeyRepoStub: &oidcGatewayKeyRepoStub{keys: make(map[string]*APIKey)}}
	svc := newOIDCGatewayKeyService(repo, &oidcGatewayUserRepoStub{user: user}, nil, nil)
	return svc, repo
}

func TestProvisionOIDCGatewayIdentityForActiveAPIOnlyUser(t *testing.T) {
	ctx := context.Background()
	svc, repo := newOIDCProvisionService(&User{ID: 7, Status: StatusActive, APIOnly: true})
	const issuer = "https://idp.example.com/realms/tabro"
	key, err := svc.ProvisionOIDCGatewayIdentity(ctx, 7, "  "+issuer+"  ", "  user-7  ")
	require.NoError(t, err)
	require.Equal(t, int64(7), key.UserID)
	require.True(t, key.OIDCManaged)
	require.Equal(t, issuer, key.OIDCIssuer)
	require.Equal(t, "user-7", key.OIDCSubject)
	require.Equal(t, 1, repo.creates)

	again, err := svc.ProvisionOIDCGatewayIdentity(ctx, 7, issuer, "user-7")
	require.NoError(t, err)
	require.Equal(t, key.ID, again.ID)
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, repo.bindCalls)

	_, err = svc.ProvisionOIDCGatewayIdentity(ctx, 7, issuer, "different-subject")
	require.ErrorIs(t, err, ErrOIDCGatewayIdentityConflict)
}

func TestProvisionOIDCGatewayIdentityRejectsInactiveOrInvalidBeforeCreatingKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    *User
		issuer  string
		subject string
		reason  string
	}{
		{name: "inactive", user: &User{ID: 7, Status: StatusDisabled}, issuer: "https://idp.example.com", subject: "user-7", reason: "USER_NOT_ACTIVE"},
		{name: "unknown", user: nil, issuer: "https://idp.example.com", subject: "user-7", reason: "USER_NOT_FOUND"},
		{name: "invalid issuer", user: &User{ID: 7, Status: StatusActive}, issuer: "https://idp.example.com?bad=1", subject: "user-7", reason: "INVALID_OIDC_ISSUER"},
		{name: "empty subject", user: &User{ID: 7, Status: StatusActive}, issuer: "https://idp.example.com", subject: " ", reason: "INVALID_OIDC_IDENTITY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newOIDCProvisionService(tc.user)
			_, err := svc.ProvisionOIDCGatewayIdentity(context.Background(), 7, tc.issuer, tc.subject)
			require.Error(t, err)
			require.Equal(t, tc.reason, infraerrors.Reason(err))
			require.Zero(t, repo.creates)
		})
	}
}

func TestProvisionOIDCGatewayIdentityRejectsKeyOwnedByAnotherUser(t *testing.T) {
	svc, repo := newOIDCProvisionService(&User{ID: 7, Status: StatusActive})
	repo.wrongOwner = true
	_, err := svc.ProvisionOIDCGatewayIdentity(context.Background(), 7, "https://idp.example.com", "user-7")
	require.ErrorIs(t, err, ErrOIDCGatewayKeyConflict)
	require.Zero(t, repo.bindCalls)
}

func TestProvisionOIDCGatewayIdentityRejectsAlreadyBoundOtherPayerBeforeCreatingKey(t *testing.T) {
	svc, repo := newOIDCProvisionService(&User{ID: 7, Status: StatusActive})
	repo.keys["other-key"] = &APIKey{
		ID:          81,
		UserID:      8,
		Key:         "other-key",
		OIDCManaged: true,
		OIDCIssuer:  "https://idp.example.com",
		OIDCSubject: "user-7",
	}
	_, err := svc.ProvisionOIDCGatewayIdentity(context.Background(), 7, "https://idp.example.com", "user-7")
	require.ErrorIs(t, err, ErrOIDCGatewayIdentityConflict)
	require.Zero(t, repo.creates)
	require.Zero(t, repo.bindCalls)
}
