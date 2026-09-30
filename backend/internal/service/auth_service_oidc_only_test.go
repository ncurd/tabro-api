//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOIDCOnlyModeRejectsLocalAuthAndExistingRefreshTokens(t *testing.T) {
	ctx := context.Background()
	user := &User{ID: 180, Email: "local@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 1}
	svc, cache, _ := newAuthTokenMetadataService(user)
	settings := &oidcOnlySettingRepo{values: map[string]string{SettingKeyOIDCOnlyEnabled: "false"}}
	svc.settingService = NewSettingService(settings, &config.Config{})

	localPair, err := svc.GenerateTokenPair(ctx, user, "local-family")
	require.NoError(t, err)
	oidcPair, err := svc.GenerateOIDCTokenPair(ctx, user, 901, "oidc-family")
	require.NoError(t, err)

	settings.values[SettingKeyOIDCOnlyEnabled] = "true"
	_, _, err = svc.Login(ctx, user.Email, "password")
	require.ErrorIs(t, err, ErrOIDCOnlyLoginRequired)
	_, _, err = svc.RegisterWithVerification(ctx, "new@example.com", "password", "", "", "")
	require.ErrorIs(t, err, ErrOIDCOnlyLoginRequired)
	_, err = svc.RefreshTokenPair(ctx, localPair.RefreshToken)
	require.ErrorIs(t, err, ErrOIDCOnlyLoginRequired)
	require.Contains(t, cache.tokens, hashToken(localPair.RefreshToken))
	_, err = svc.RefreshToken(ctx, localPair.AccessToken)
	require.ErrorIs(t, err, ErrOIDCOnlyLoginRequired)

	rotatedOIDC, err := svc.RefreshTokenPair(ctx, oidcPair.RefreshToken)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, rotatedOIDC.AccessToken, AuthMethodOIDC, 901)
	refreshedOIDC, err := svc.RefreshToken(ctx, oidcPair.AccessToken)
	require.NoError(t, err)
	requireJWTMetadata(t, svc, refreshedOIDC, AuthMethodOIDC, 901)
}
