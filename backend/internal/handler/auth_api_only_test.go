package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

type apiOnlyLoginCacheStub struct {
	service.TotpCache
	userID       int64
	deleteCalled bool
}

func (s *apiOnlyLoginCacheStub) GetLoginSession(context.Context, string) (*service.TotpLoginSession, error) {
	return &service.TotpLoginSession{UserID: s.userID}, nil
}

func (s *apiOnlyLoginCacheStub) GetVerifyAttempts(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *apiOnlyLoginCacheStub) ClearVerifyAttempts(context.Context, int64) error {
	return nil
}

func (s *apiOnlyLoginCacheStub) DeleteLoginSession(context.Context, string) error {
	s.deleteCalled = true
	return nil
}

type apiOnlyTotpEncryptor struct{}

func (apiOnlyTotpEncryptor) Encrypt(secret string) (string, error) { return secret, nil }
func (apiOnlyTotpEncryptor) Decrypt(secret string) (string, error) { return secret, nil }

func TestLogin2FARejectsUserChangedToAPIOnlyBeforeCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "JBSWY3DPEHPK3PXP"
	encryptedSecret := secret
	user := &service.User{
		ID: 88, Email: "api-only@example.com", Role: service.RoleUser,
		Status: service.StatusActive, APIOnly: true,
		TotpEnabled: true, TotpSecretEncrypted: &encryptedSecret,
	}
	repo := &oidcLoginUserRepoStub{usersByEmail: map[string]*service.User{user.Email: user}}
	cache := &apiOnlyLoginCacheStub{userID: user.ID}
	settings := service.NewSettingService(&oidcOnlyHandlerSettingRepo{values: map[string]string{}}, &config.Config{})
	h := &AuthHandler{
		totpService: service.NewTotpService(repo, apiOnlyTotpEncryptor{}, cache, settings, nil, nil),
		userService: service.NewUserService(repo, nil, nil, nil),
		settingSvc:  settings,
	}
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	router := gin.New()
	router.POST("/login/2fa", h.Login2FA)
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(`{"temp_token":"pending","totp_code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_CREDENTIALS")
	require.False(t, cache.deleteCalled)
}
