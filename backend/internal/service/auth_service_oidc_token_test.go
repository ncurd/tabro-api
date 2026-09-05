//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type authTokenUserRepoStub struct {
	UserRepository
	usersByID     map[int64]*User
	usersByEmail  map[string]*User
	getByEmailErr error
	createCalls   int
	updateCalls   int
}

func (s *authTokenUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	if user, ok := s.usersByID[id]; ok {
		return user, nil
	}
	return nil, ErrUserNotFound
}

func (s *authTokenUserRepoStub) GetByEmail(_ context.Context, email string) (*User, error) {
	if s.getByEmailErr != nil {
		return nil, s.getByEmailErr
	}
	if user, ok := s.usersByEmail[email]; ok {
		return user, nil
	}
	return nil, ErrUserNotFound
}

func (s *authTokenUserRepoStub) Create(_ context.Context, user *User) error {
	s.createCalls++
	if s.usersByID == nil {
		s.usersByID = make(map[int64]*User)
	}
	if s.usersByEmail == nil {
		s.usersByEmail = make(map[string]*User)
	}
	s.usersByID[user.ID] = user
	s.usersByEmail[user.Email] = user
	return nil
}

func (s *authTokenUserRepoStub) Update(_ context.Context, user *User) error {
	s.updateCalls++
	return nil
}

type authTokenRefreshCacheStub struct {
	RefreshTokenCache
	tokens map[string]*RefreshTokenData
}

func newAuthTokenRefreshCacheStub() *authTokenRefreshCacheStub {
	return &authTokenRefreshCacheStub{tokens: make(map[string]*RefreshTokenData)}
}

func (s *authTokenRefreshCacheStub) StoreRefreshToken(_ context.Context, tokenHash string, data *RefreshTokenData, _ time.Duration) error {
	copyData := *data
	s.tokens[tokenHash] = &copyData
	return nil
}

func (s *authTokenRefreshCacheStub) GetRefreshToken(_ context.Context, tokenHash string) (*RefreshTokenData, error) {
	data, ok := s.tokens[tokenHash]
	if !ok {
		return nil, ErrRefreshTokenNotFound
	}
	copyData := *data
	return &copyData, nil
}

func (s *authTokenRefreshCacheStub) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	delete(s.tokens, tokenHash)
	return nil
}

func (s *authTokenRefreshCacheStub) DeleteTokenFamily(_ context.Context, familyID string) error {
	for tokenHash, data := range s.tokens {
		if data.FamilyID == familyID {
			delete(s.tokens, tokenHash)
		}
	}
	return nil
}

func (s *authTokenRefreshCacheStub) AddToUserTokenSet(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

func (s *authTokenRefreshCacheStub) AddToFamilyTokenSet(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

func newAuthTokenMetadataService(user *User) (*AuthService, *authTokenRefreshCacheStub, *authTokenUserRepoStub) {
	repo := &authTokenUserRepoStub{
		usersByID:    map[int64]*User{user.ID: user},
		usersByEmail: map[string]*User{user.Email: user},
	}
	cache := newAuthTokenRefreshCacheStub()
	cfg := &config.Config{JWT: config.JWTConfig{
		Secret:                 "oidc-token-metadata-test-secret",
		ExpireHour:             1,
		RefreshTokenExpireDays: 7,
	}}
	return NewAuthService(nil, repo, nil, cache, cfg, nil, nil, nil, nil, nil, nil), cache, repo
}

func requireJWTMetadata(t *testing.T, svc *AuthService, token, authMethod string, billingAPIKeyID int64) {
	t.Helper()
	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)
	require.Equal(t, authMethod, claims.AuthMethod)
	require.Equal(t, billingAPIKeyID, claims.BillingAPIKeyID)
}

func requireJWTFieldsAbsent(t *testing.T, token string) {
	t.Helper()
	mapClaims := jwt.MapClaims{}
	parsed, _, err := new(jwt.Parser).ParseUnverified(token, mapClaims)
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	require.NotContains(t, claims, "auth_method")
	require.NotContains(t, claims, "billing_api_key_id")
}

func TestAuthServiceGenerateToken_DefaultMetadataIsEmpty(t *testing.T) {
	user := &User{ID: 11, Email: "user@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 3}
	svc, _, _ := newAuthTokenMetadataService(user)

	token, err := svc.GenerateToken(user)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, token, "", 0)
	requireJWTFieldsAbsent(t, token)
}

func TestAuthServiceGenerateTokenPair_DefaultMetadataIsEmpty(t *testing.T) {
	user := &User{ID: 12, Email: "plain@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 1}
	svc, cache, _ := newAuthTokenMetadataService(user)

	pair, err := svc.GenerateTokenPair(context.Background(), user, "plain-family")
	require.NoError(t, err)
	requireJWTMetadata(t, svc, pair.AccessToken, "", 0)
	requireJWTFieldsAbsent(t, pair.AccessToken)

	data := cache.tokens[hashToken(pair.RefreshToken)]
	require.NotNil(t, data)
	require.Empty(t, data.AuthMethod)
	require.Zero(t, data.BillingAPIKeyID)
}

func TestAuthServiceRefreshTokenPair_LegacyMetadataRemainsEmpty(t *testing.T) {
	user := &User{ID: 120, Email: "legacy-pair@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 1}
	svc, cache, _ := newAuthTokenMetadataService(user)
	legacyRefreshToken := refreshTokenPrefix + "legacy-record-without-metadata"
	cache.tokens[hashToken(legacyRefreshToken)] = &RefreshTokenData{
		UserID:       user.ID,
		TokenVersion: user.TokenVersion,
		FamilyID:     "legacy-pair-family",
		CreatedAt:    time.Now().Add(-time.Hour),
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	rotated, err := svc.RefreshTokenPair(context.Background(), legacyRefreshToken)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, rotated.AccessToken, "", 0)
	requireJWTFieldsAbsent(t, rotated.AccessToken)
	require.NotContains(t, cache.tokens, hashToken(legacyRefreshToken))

	data := cache.tokens[hashToken(rotated.RefreshToken)]
	require.NotNil(t, data)
	require.Empty(t, data.AuthMethod)
	require.Zero(t, data.BillingAPIKeyID)
	require.Equal(t, "legacy-pair-family", data.FamilyID)
}

func TestAuthServiceGenerateOIDCTokenPair_SetsMetadata(t *testing.T) {
	user := &User{ID: 13, Email: "oidc@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 2}
	svc, cache, _ := newAuthTokenMetadataService(user)

	pair, err := svc.GenerateOIDCTokenPair(context.Background(), user, 901, "oidc-family")
	require.NoError(t, err)
	requireJWTMetadata(t, svc, pair.AccessToken, AuthMethodOIDC, 901)

	data := cache.tokens[hashToken(pair.RefreshToken)]
	require.NotNil(t, data)
	require.Equal(t, AuthMethodOIDC, data.AuthMethod)
	require.Equal(t, int64(901), data.BillingAPIKeyID)
	require.Equal(t, "oidc-family", data.FamilyID)
}

func TestAuthServiceGenerateOIDCTokenPair_RejectsInvalidBillingAPIKeyID(t *testing.T) {
	user := &User{ID: 130, Email: "invalid-key@example.com", Role: RoleUser, Status: StatusActive}
	svc, cache, _ := newAuthTokenMetadataService(user)

	for _, billingAPIKeyID := range []int64{0, -1} {
		pair, err := svc.GenerateOIDCTokenPair(context.Background(), user, billingAPIKeyID, "invalid-key-family")
		require.Nil(t, pair)
		require.EqualError(t, err, "billing api key id must be positive")
	}
	require.Empty(t, cache.tokens)
}

func TestAuthServiceRefreshTokenPair_PreservesOIDCMetadata(t *testing.T) {
	user := &User{ID: 14, Email: "rotate@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 4}
	svc, cache, _ := newAuthTokenMetadataService(user)

	original, err := svc.GenerateOIDCTokenPair(context.Background(), user, 902, "rotation-family")
	require.NoError(t, err)
	rotated, err := svc.RefreshTokenPair(context.Background(), original.RefreshToken)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, rotated.AccessToken, AuthMethodOIDC, 902)
	require.Equal(t, user.Role, rotated.UserRole)
	require.NotEqual(t, original.RefreshToken, rotated.RefreshToken)
	require.NotContains(t, cache.tokens, hashToken(original.RefreshToken))

	data := cache.tokens[hashToken(rotated.RefreshToken)]
	require.NotNil(t, data)
	require.Equal(t, AuthMethodOIDC, data.AuthMethod)
	require.Equal(t, int64(902), data.BillingAPIKeyID)
	require.Equal(t, "rotation-family", data.FamilyID)
}

func TestAuthServiceRefreshToken_PreservesOIDCMetadata(t *testing.T) {
	user := &User{ID: 15, Email: "legacy-refresh@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 5}
	svc, _, _ := newAuthTokenMetadataService(user)

	pair, err := svc.GenerateOIDCTokenPair(context.Background(), user, 903, "legacy-family")
	require.NoError(t, err)
	refreshedAccessToken, err := svc.RefreshToken(context.Background(), pair.AccessToken)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, refreshedAccessToken, AuthMethodOIDC, 903)
}

func TestAuthServiceLoginOrRegisterOAuthUser_ExistingUserDoesNotRequireTokenCache(t *testing.T) {
	user := &User{ID: 16, Email: "existing@example.com", Username: "existing", Role: RoleUser, Status: StatusActive}
	repo := &authTokenUserRepoStub{usersByEmail: map[string]*User{user.Email: user}}
	svc := NewAuthService(nil, repo, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)

	got, err := svc.LoginOrRegisterOAuthUser(context.Background(), user.Email, "ignored", "")
	require.NoError(t, err)
	require.Same(t, user, got)
	require.Zero(t, repo.createCalls)
	require.Zero(t, repo.updateCalls)
}

func TestAuthServiceLoginOrRegisterOAuthWithTokenPair_RemainsUnscoped(t *testing.T) {
	user := &User{ID: 17, Email: "oauth-pair@example.com", Username: "existing", Role: RoleUser, Status: StatusActive}
	svc, cache, repo := newAuthTokenMetadataService(user)

	pair, got, err := svc.LoginOrRegisterOAuthWithTokenPair(context.Background(), user.Email, "ignored", "")
	require.NoError(t, err)
	require.Same(t, user, got)
	requireJWTMetadata(t, svc, pair.AccessToken, "", 0)
	requireJWTFieldsAbsent(t, pair.AccessToken)
	data := cache.tokens[hashToken(pair.RefreshToken)]
	require.NotNil(t, data)
	require.Empty(t, data.AuthMethod)
	require.Zero(t, data.BillingAPIKeyID)
	require.Zero(t, repo.createCalls)
	require.Zero(t, repo.updateCalls)
}

func TestAuthServiceLoginExistingAdminOAuthUser(t *testing.T) {
	t.Run("accepts active existing admin without mutation", func(t *testing.T) {
		admin := &User{ID: 21, Email: "admin@example.com", Username: "admin", Role: RoleAdmin, Status: StatusActive}
		repo := &authTokenUserRepoStub{usersByEmail: map[string]*User{admin.Email: admin}}
		svc := NewAuthService(nil, repo, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)

		got, err := svc.LoginExistingAdminOAuthUser(context.Background(), "  "+admin.Email+"  ")
		require.NoError(t, err)
		require.Same(t, admin, got)
		require.Zero(t, repo.createCalls)
		require.Zero(t, repo.updateCalls)
	})

	t.Run("rejects ordinary user with generic authentication error", func(t *testing.T) {
		user := &User{ID: 22, Email: "user@example.com", Role: RoleUser, Status: StatusActive}
		repo := &authTokenUserRepoStub{usersByEmail: map[string]*User{user.Email: user}}
		svc := NewAuthService(nil, repo, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)

		got, err := svc.LoginExistingAdminOAuthUser(context.Background(), user.Email)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrInvalidCredentials)
		require.Zero(t, repo.createCalls)
		require.Zero(t, repo.updateCalls)
	})

	t.Run("rejects missing user with generic authentication error", func(t *testing.T) {
		repo := &authTokenUserRepoStub{usersByEmail: map[string]*User{}}
		svc := NewAuthService(nil, repo, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)

		got, err := svc.LoginExistingAdminOAuthUser(context.Background(), "missing@example.com")
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrInvalidCredentials)
		require.Zero(t, repo.createCalls)
		require.Zero(t, repo.updateCalls)
	})

	t.Run("rejects inactive admin with generic authentication error", func(t *testing.T) {
		admin := &User{ID: 23, Email: "inactive@example.com", Role: RoleAdmin, Status: StatusDisabled}
		repo := &authTokenUserRepoStub{usersByEmail: map[string]*User{admin.Email: admin}}
		svc := NewAuthService(nil, repo, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)

		got, err := svc.LoginExistingAdminOAuthUser(context.Background(), admin.Email)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	})
}
