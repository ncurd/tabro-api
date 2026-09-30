//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminAuthJWTValidatesTokenVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret", ExpireHour: 1}}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)

	admin := &service.User{
		ID:           1,
		Email:        "admin@example.com",
		Role:         service.RoleAdmin,
		Status:       service.StatusActive,
		TokenVersion: 2,
		Concurrency:  1,
	}

	userRepo := &stubUserRepo{
		getByID: func(ctx context.Context, id int64) (*service.User, error) {
			if id != admin.ID {
				return nil, service.ErrUserNotFound
			}
			clone := *admin
			return &clone, nil
		},
	}
	userService := service.NewUserService(userRepo, nil, nil, nil)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAdminAuthMiddleware(authService, userService, nil)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	t.Run("token_version_mismatch_rejected", func(t *testing.T) {
		token, err := authService.GenerateToken(&service.User{
			ID:           admin.ID,
			Email:        admin.Email,
			Role:         admin.Role,
			TokenVersion: admin.TokenVersion - 1,
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Body.String(), "TOKEN_REVOKED")
	})

	t.Run("token_version_match_allows", func(t *testing.T) {
		token, err := authService.GenerateToken(&service.User{
			ID:           admin.ID,
			Email:        admin.Email,
			Role:         admin.Role,
			TokenVersion: admin.TokenVersion,
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("websocket_token_version_mismatch_rejected", func(t *testing.T) {
		token, err := authService.GenerateToken(&service.User{
			ID:           admin.ID,
			Email:        admin.Email,
			Role:         admin.Role,
			TokenVersion: admin.TokenVersion - 1,
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Protocol", "sub2api-admin, jwt."+token)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Body.String(), "TOKEN_REVOKED")
	})

	t.Run("websocket_token_version_match_allows", func(t *testing.T) {
		token, err := authService.GenerateToken(&service.User{
			ID:           admin.ID,
			Email:        admin.Email,
			Role:         admin.Role,
			TokenVersion: admin.TokenVersion,
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Protocol", "sub2api-admin, jwt."+token)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestAdminAuthOIDCOnlyRejectsLocalJWTAndKeepsRecoveryKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "oidc-only-admin-test-secret"
	const recoveryKey = "sk-admin-recovery-test-key"
	cfg := &config.Config{JWT: config.JWTConfig{Secret: secret, ExpireHour: 1}}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	admin := &service.User{
		ID:           1,
		Email:        "admin@example.com",
		Role:         service.RoleAdmin,
		Status:       service.StatusActive,
		TokenVersion: 2,
	}
	userRepo := &stubUserRepo{
		getByID: func(_ context.Context, id int64) (*service.User, error) {
			if id != admin.ID {
				return nil, service.ErrUserNotFound
			}
			return admin, nil
		},
		getFirstAdmin: func(context.Context) (*service.User, error) { return admin, nil },
	}
	userService := service.NewUserService(userRepo, nil, nil, nil)
	settingService := service.NewSettingService(&oidcOnlySettingRepo{enabled: true, adminKey: recoveryKey}, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAdminAuthMiddleware(authService, userService, settingService)))
	router.GET("/admin", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"auth_method": c.GetString("auth_method")})
	})

	localToken := signJWTWithAuthMethod(t, authService, admin, "", secret)
	oidcToken := signJWTWithAuthMethod(t, authService, admin, service.AuthMethodOIDC, secret)
	for _, tc := range []struct {
		name       string
		bearer     string
		websocket  bool
		adminKey   string
		wantStatus int
		wantMethod string
	}{
		{name: "local_bearer", bearer: localToken, wantStatus: http.StatusUnauthorized},
		{name: "local_websocket", bearer: localToken, websocket: true, wantStatus: http.StatusUnauthorized},
		{name: "oidc_bearer", bearer: oidcToken, wantStatus: http.StatusOK, wantMethod: service.AuthMethodOIDC},
		{name: "oidc_websocket", bearer: oidcToken, websocket: true, wantStatus: http.StatusOK, wantMethod: service.AuthMethodOIDC},
		{name: "recovery_key", adminKey: recoveryKey, wantStatus: http.StatusOK, wantMethod: "admin_api_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/admin", nil)
			if tc.websocket {
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Sec-WebSocket-Protocol", "sub2api-admin, jwt."+tc.bearer)
			} else if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			if tc.adminKey != "" {
				req.Header.Set("x-api-key", tc.adminKey)
			}
			router.ServeHTTP(w, req)
			require.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusUnauthorized {
				require.Contains(t, w.Body.String(), "OIDC_ONLY_LOGIN_REQUIRED")
			} else {
				require.Contains(t, w.Body.String(), `"auth_method":"`+tc.wantMethod+`"`)
			}
		})
	}
}

func TestAdminAuthAPIOnlyRejectsPreviouslyIssuedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "api-only-admin-test-secret", ExpireHour: 1}}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	admin := &service.User{ID: 61, Email: "admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive}
	token, err := authService.GenerateToken(admin)
	require.NoError(t, err)
	admin.APIOnly = true

	userRepo := &stubUserRepo{getByID: func(_ context.Context, id int64) (*service.User, error) {
		if id != admin.ID {
			return nil, service.ErrUserNotFound
		}
		return admin, nil
	}}
	userService := service.NewUserService(userRepo, nil, nil, nil)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAdminAuthMiddleware(authService, userService, nil)))
	router.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, websocket := range []bool{false, true} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		if websocket {
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Sec-WebSocket-Protocol", "sub2api-admin, jwt."+token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_TOKEN")
	}
}

type stubUserRepo struct {
	getByID       func(ctx context.Context, id int64) (*service.User, error)
	getFirstAdmin func(ctx context.Context) (*service.User, error)
}

func (s *stubUserRepo) Create(ctx context.Context, user *service.User) error {
	panic("unexpected Create call")
}

func (s *stubUserRepo) GetByID(ctx context.Context, id int64) (*service.User, error) {
	if s.getByID == nil {
		panic("GetByID not stubbed")
	}
	return s.getByID(ctx, id)
}

func (s *stubUserRepo) GetByEmail(ctx context.Context, email string) (*service.User, error) {
	panic("unexpected GetByEmail call")
}

func (s *stubUserRepo) GetFirstAdmin(ctx context.Context) (*service.User, error) {
	if s.getFirstAdmin == nil {
		panic("GetFirstAdmin not stubbed")
	}
	return s.getFirstAdmin(ctx)
}

func (s *stubUserRepo) Update(ctx context.Context, user *service.User) error {
	panic("unexpected Update call")
}

func (s *stubUserRepo) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

func (s *stubUserRepo) List(ctx context.Context, params pagination.PaginationParams) ([]service.User, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *stubUserRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters service.UserListFilters) ([]service.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *stubUserRepo) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	panic("unexpected UpdateBalance call")
}

func (s *stubUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) error {
	panic("unexpected DeductBalance call")
}

func (s *stubUserRepo) UpdateConcurrency(ctx context.Context, id int64, amount int) error {
	panic("unexpected UpdateConcurrency call")
}

func (s *stubUserRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	panic("unexpected ExistsByEmail call")
}

func (s *stubUserRepo) RemoveGroupFromAllowedGroups(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups call")
}

func (s *stubUserRepo) RemoveGroupFromUserAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups call")
}

func (s *stubUserRepo) AddGroupToAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	panic("unexpected AddGroupToAllowedGroups call")
}

func (s *stubUserRepo) UpdateTotpSecret(ctx context.Context, userID int64, encryptedSecret *string) error {
	panic("unexpected UpdateTotpSecret call")
}

func (s *stubUserRepo) EnableTotp(ctx context.Context, userID int64) error {
	panic("unexpected EnableTotp call")
}

func (s *stubUserRepo) DisableTotp(ctx context.Context, userID int64) error {
	panic("unexpected DisableTotp call")
}
