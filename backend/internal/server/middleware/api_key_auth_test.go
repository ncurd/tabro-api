//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestGatewayAuthRejectsLocalTabroOIDCTokenOnAllCredentialHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "gateway-oidc-token-test-secret"
	cfg.JWT.AccessTokenExpireMinutes = 60

	user := &service.User{
		ID:           77,
		Email:        "oidc@example.com",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Concurrency:  4,
		TokenVersion: 3,
	}
	groupID := int64(8)
	billingKey := &service.APIKey{
		ID:          501,
		UserID:      user.ID,
		Key:         "hidden-oidc-billing-key",
		OIDCManaged: true,
		Status:      service.StatusAPIKeyActive,
		GroupID:     &groupID,
		User:        user,
		Group: &service.Group{
			ID:       groupID,
			Status:   service.StatusActive,
			Platform: service.PlatformAnthropic,
			Hydrated: true,
		},
	}
	getByKeyCalls := 0
	repo := &stubApiKeyRepo{
		getByKey: func(context.Context, string) (*service.APIKey, error) {
			getByKeyCalls++
			return nil, service.ErrAPIKeyNotFound
		},
		getByID: func(_ context.Context, id int64) (*service.APIKey, error) {
			if id != billingKey.ID {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *billingKey
			return &clone, nil
		},
	}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	token := signOIDCGatewayToken(t, cfg.JWT.Secret, user.ID, billingKey.ID, user.TokenVersion, service.AuthMethodOIDC)

	for name, setHeader := range map[string]func(*http.Request){
		"authorization_bearer": func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) },
		"x_api_key":            func(req *http.Request) { req.Header.Set("x-api-key", token) },
		"x_goog_api_key":       func(req *http.Request) { req.Header.Set("x-goog-api-key", token) },
	} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(apiKeyService, nil, authService, cfg)))
			router.GET("/t", func(c *gin.Context) {
				resolved, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.Equal(t, billingKey.ID, resolved.ID)
				c.Status(http.StatusNoContent)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			setHeader(req)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.Contains(t, w.Body.String(), "INVALID_API_KEY")
		})
	}
	require.Zero(t, getByKeyCalls, "JWT-shaped credentials must never reach the API-key lookup path")
}

func TestGatewayAuthRejectsImportedDottedBearerInsteadOfFallingBackToAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "gateway-dotted-key-fallback-secret"
	user := &service.User{ID: 20, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
	legacyKey := &service.APIKey{
		ID:     200,
		UserID: user.ID,
		Key:    "legacy.dotted.api-key",
		Status: service.StatusAPIKeyActive,
		User:   user,
	}
	getByKeyCalls := 0
	repo := &stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			getByKeyCalls++
			if key != legacyKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *legacyKey
			return &clone, nil
		},
	}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(apiKeyService, nil, authService, cfg)))
	router.GET("/t", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("Authorization", "Bearer "+legacyKey.Key)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_API_KEY")
	require.Zero(t, getByKeyCalls, "JWT-shaped bearer values must never fall back to API-key lookup")
}

func TestGatewayAuthRejectsOIDCBillingKeyValueAsStandaloneCredential(t *testing.T) {
	var getByKeyCalls int
	repo := &stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			getByKeyCalls++
			return &service.APIKey{
				ID:          701,
				UserID:      70,
				Key:         key,
				OIDCManaged: true,
				Status:      service.StatusAPIKeyActive,
				User:        &service.User{ID: 70, Status: service.StatusActive},
			}, nil
		},
	}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})

	_, err := resolveGatewayAPIKey(context.Background(), "oidc-internal:opaque-value", true, apiKeyService, nil)
	require.ErrorIs(t, err, errInvalidGatewayCredential)
	require.Equal(t, 1, getByKeyCalls)
}

func TestGatewayAuthAllowsOrdinaryKeyUsingOIDCLookingConfiguredPrefix(t *testing.T) {
	repo := &stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			return &service.APIKey{
				ID:          702,
				UserID:      71,
				Key:         key,
				OIDCManaged: false,
				Status:      service.StatusAPIKeyActive,
				User:        &service.User{ID: 71, Status: service.StatusActive},
			}, nil
		},
	}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})

	got, err := resolveGatewayAPIKey(context.Background(), "oidc-internal:ordinary-configured-prefix", true, apiKeyService, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.OIDCManaged)
}

func TestGatewayAuthRejectsNonOIDCOrMismatchedLocalTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "gateway-oidc-token-test-secret"
	cfg.JWT.AccessTokenExpireMinutes = 60

	user := &service.User{ID: 7, Email: "user@example.com", Role: service.RoleUser, Status: service.StatusActive, TokenVersion: 2}
	billingKey := &service.APIKey{ID: 80, UserID: user.ID, Key: "billing", OIDCManaged: true, Status: service.StatusAPIKeyActive, User: user}
	repo := &stubApiKeyRepo{
		getByKey: func(context.Context, string) (*service.APIKey, error) { return nil, service.ErrAPIKeyNotFound },
		getByID: func(_ context.Context, id int64) (*service.APIKey, error) {
			clone := *billingKey
			if id == billingKey.ID+1 {
				clone.ID = id
				clone.OIDCManaged = false
			}
			return &clone, nil
		},
	}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)

	passwordToken, err := authService.GenerateToken(user)
	require.NoError(t, err)
	tests := map[string]string{
		"password_access_token": passwordToken,
		"wrong_owner":           signOIDCGatewayToken(t, cfg.JWT.Secret, 999, billingKey.ID, user.TokenVersion, service.AuthMethodOIDC),
		"revoked_version":       signOIDCGatewayToken(t, cfg.JWT.Secret, user.ID, billingKey.ID, user.TokenVersion-1, service.AuthMethodOIDC),
		"missing_billing_key":   signOIDCGatewayToken(t, cfg.JWT.Secret, user.ID, 0, user.TokenVersion, service.AuthMethodOIDC),
		"ordinary_key_binding":  signOIDCGatewayToken(t, cfg.JWT.Secret, user.ID, billingKey.ID+1, user.TokenVersion, service.AuthMethodOIDC),
	}

	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(apiKeyService, nil, authService, cfg)))
			router.GET("/t", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.Contains(t, w.Body.String(), "INVALID_API_KEY")
		})
	}
}

func TestGatewayAuthExternalOIDCTokenUsesBoundAPIKeyBillingPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := newGatewayMiddlewareOAuthFixture(t)
	fixture.cfg.RunMode = config.RunModeStandard
	fixture.repo.boundKey.User.Balance = 10
	fixture.repo.boundKey.Quota = 1
	fixture.repo.boundKey.QuotaUsed = 1
	token := fixture.sign(t, fixture.claims())

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
	router.GET("/t", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "API_KEY_QUOTA_EXHAUSTED")
	require.Equal(t, 1, fixture.repo.identityLookups)
}

func TestGatewayAuthUsageAliasesSkipBillingPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	user := &service.User{ID: 22, Status: service.StatusActive}
	apiKey := &service.APIKey{
		ID:     220,
		UserID: user.ID,
		Key:    "expired-usage-inspection-key",
		Status: service.StatusAPIKeyExpired,
		User:   user,
	}
	repo := &stubApiKeyRepo{getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
		if key != apiKey.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone := *apiKey
		return &clone, nil
	}}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(apiKeyService, nil, nil, cfg)))
	for _, path := range []string{"/v1/usage", "/antigravity/v1/usage", "/v1/models"} {
		router.GET(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/v1/usage", want: http.StatusNoContent},
		{path: "/antigravity/v1/usage", want: http.StatusNoContent},
		{path: "/v1/models", want: http.StatusForbidden},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+apiKey.Key)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestGatewayAuthOIDCIdentityLookupFailureReturnsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := newGatewayMiddlewareOAuthFixture(t)
	fixture.repo.identityLookupErr = errors.New("database unavailable")
	token := fixture.sign(t, fixture.claims())

	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
	router.GET("/t", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "OIDC_PROVIDER_UNAVAILABLE")
}

func signOIDCGatewayToken(t *testing.T, secret string, userID, billingAPIKeyID, tokenVersion int64, authMethod string) string {
	t.Helper()
	now := time.Now()
	claims := service.JWTClaims{
		UserID:          userID,
		AuthMethod:      authMethod,
		BillingAPIKeyID: billingAPIKeyID,
		TokenVersion:    tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

func TestSimpleModeBypassesQuotaCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limit := 1.0
	group := &service.Group{
		ID:               42,
		Name:             "sub",
		Status:           service.StatusActive,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeSubscription,
		DailyLimitUSD:    &limit,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	t.Run("standard_mode_needs_maintenance_does_not_block_request", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeStandard}
		cfg.SubscriptionMaintenance.WorkerCount = 1
		cfg.SubscriptionMaintenance.QueueSize = 1

		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)

		past := time.Now().Add(-48 * time.Hour)
		sub := &service.UserSubscription{
			ID:               55,
			UserID:           user.ID,
			GroupID:          group.ID,
			Status:           service.SubscriptionStatusActive,
			ExpiresAt:        time.Now().Add(24 * time.Hour),
			DailyWindowStart: &past,
			DailyUsageUSD:    0,
		}
		maintenanceCalled := make(chan struct{}, 1)
		subscriptionRepo := &stubUserSubscriptionRepo{
			getActive: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				clone := *sub
				return &clone, nil
			},
			updateStatus:   func(ctx context.Context, subscriptionID int64, status string) error { return nil },
			activateWindow: func(ctx context.Context, id int64, start time.Time) error { return nil },
			resetDaily: func(ctx context.Context, id int64, start time.Time) error {
				maintenanceCalled <- struct{}{}
				return nil
			},
			resetWeekly:  func(ctx context.Context, id int64, start time.Time) error { return nil },
			resetMonthly: func(ctx context.Context, id int64, start time.Time) error { return nil },
		}
		subscriptionService := service.NewSubscriptionService(nil, subscriptionRepo, nil, nil, cfg)
		t.Cleanup(subscriptionService.Stop)

		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		select {
		case <-maintenanceCalled:
			// ok
		case <-time.After(time.Second):
			t.Fatalf("expected maintenance to be scheduled")
		}
	})

	t.Run("simple_mode_bypasses_quota_check", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("simple_mode_accepts_lowercase_bearer", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Authorization", "bearer "+apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("standard_mode_enforces_quota_check", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeStandard}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)

		now := time.Now()
		sub := &service.UserSubscription{
			ID:               55,
			UserID:           user.ID,
			GroupID:          group.ID,
			Status:           service.SubscriptionStatusActive,
			ExpiresAt:        now.Add(24 * time.Hour),
			DailyWindowStart: &now,
			DailyUsageUSD:    10,
		}
		subscriptionRepo := &stubUserSubscriptionRepo{
			getActive: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				if userID != sub.UserID || groupID != sub.GroupID {
					return nil, service.ErrSubscriptionNotFound
				}
				clone := *sub
				return &clone, nil
			},
			updateStatus:   func(ctx context.Context, subscriptionID int64, status string) error { return nil },
			activateWindow: func(ctx context.Context, id int64, start time.Time) error { return nil },
			resetDaily:     func(ctx context.Context, id int64, start time.Time) error { return nil },
			resetWeekly:    func(ctx context.Context, id int64, start time.Time) error { return nil },
			resetMonthly:   func(ctx context.Context, id int64, start time.Time) error { return nil },
		}
		subscriptionService := service.NewSubscriptionService(nil, subscriptionRepo, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Contains(t, w.Body.String(), "USAGE_LIMIT_EXCEEDED")
	})
}

func TestAPIKeyAuthSetsGroupContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:       101,
		Name:     "g1",
		Status:   service.StatusActive,
		Platform: service.PlatformAnthropic,
		Hydrated: true,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		groupFromCtx, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
		if !ok || groupFromCtx == nil || groupFromCtx.ID != group.ID {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthOverwritesInvalidContextGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:       101,
		Name:     "g1",
		Status:   service.StatusActive,
		Platform: service.PlatformAnthropic,
		Hydrated: true,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg)))

	invalidGroup := &service.Group{
		ID:       group.ID,
		Platform: group.Platform,
		Status:   group.Status,
	}
	router.GET("/t", func(c *gin.Context) {
		groupFromCtx, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
		if !ok || groupFromCtx == nil || groupFromCtx.ID != group.ID || !groupFromCtx.Hydrated || groupFromCtx == invalidGroup {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, invalidGroup))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthIPRestrictionDoesNotTrustSpoofedForwardHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:          100,
		UserID:      user.ID,
		Key:         "test-key",
		Status:      service.StatusActive,
		User:        user,
		IPWhitelist: []string{"1.2.3.4"},
	}

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("x-api-key", apiKey.Key)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "ACCESS_DENIED")
}

func TestAPIKeyAuthTouchesLastUsedOnSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "touch-ok",
		Status: service.StatusActive,
		User:   user,
	}

	var touchedID int64
	var touchedAt time.Time
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchedID = id
			touchedAt = usedAt
			return nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, apiKey.ID, touchedID)
	require.False(t, touchedAt.IsZero(), "expected touch timestamp")
}

func TestAPIKeyAuthTouchLastUsedFailureDoesNotBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          8,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     101,
		UserID: user.ID,
		Key:    "touch-fail",
		Status: service.StatusActive,
		User:   user,
	}

	touchCalls := 0
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchCalls++
			return errors.New("db unavailable")
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "touch failure should not block request")
	require.Equal(t, 1, touchCalls)
}

func TestAPIKeyAuthTouchesLastUsedInStandardMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          9,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     102,
		UserID: user.ID,
		Key:    "touch-standard",
		Status: service.StatusActive,
		User:   user,
	}

	touchCalls := 0
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchCalls++
			return nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, touchCalls)
}

func newAuthTestRouter(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, cfg *config.Config) *gin.Engine {
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

type stubApiKeyRepo struct {
	getByKey       func(ctx context.Context, key string) (*service.APIKey, error)
	getByID        func(ctx context.Context, id int64) (*service.APIKey, error)
	updateLastUsed func(ctx context.Context, id int64, usedAt time.Time) error
}

func (r *stubApiKeyRepo) Create(ctx context.Context, key *service.APIKey) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByID(ctx context.Context, id int64) (*service.APIKey, error) {
	if r.getByID != nil {
		return r.getByID(ctx, id)
	}
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	return "", 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	if r.getByKey != nil {
		return r.getByKey(ctx, key)
	}
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKey(ctx, key)
}

func (r *stubApiKeyRepo) Update(ctx context.Context, key *service.APIKey) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ExistsByKey(ctx context.Context, key string) (bool, error) {
	return false, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]service.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	if r.updateLastUsed != nil {
		return r.updateLastUsed(ctx, id, usedAt)
	}
	return nil
}

func (r *stubApiKeyRepo) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	return nil
}
func (r *stubApiKeyRepo) ResetRateLimitWindows(ctx context.Context, id int64) error {
	return nil
}
func (r *stubApiKeyRepo) GetRateLimitData(ctx context.Context, id int64) (*service.APIKeyRateLimitData, error) {
	return nil, nil
}

type stubUserSubscriptionRepo struct {
	getActive      func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error)
	updateStatus   func(ctx context.Context, subscriptionID int64, status string) error
	activateWindow func(ctx context.Context, id int64, start time.Time) error
	resetDaily     func(ctx context.Context, id int64, start time.Time) error
	resetWeekly    func(ctx context.Context, id int64, start time.Time) error
	resetMonthly   func(ctx context.Context, id int64, start time.Time) error
}

func (r *stubUserSubscriptionRepo) Create(ctx context.Context, sub *service.UserSubscription) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	if r.getActive != nil {
		return r.getActive(ctx, userID, groupID)
	}
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) Update(ctx context.Context, sub *service.UserSubscription) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	return false, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	if r.updateStatus != nil {
		return r.updateStatus(ctx, subscriptionID, status)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ActivateWindows(ctx context.Context, id int64, start time.Time) error {
	if r.activateWindow != nil {
		return r.activateWindow(ctx, id, start)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	if r.resetDaily != nil {
		return r.resetDaily(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	if r.resetWeekly != nil {
		return r.resetWeekly(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	if r.resetMonthly != nil {
		return r.resetMonthly(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	return 0, errors.New("not implemented")
}
