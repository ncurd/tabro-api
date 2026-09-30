package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type provisionUserRepoStub struct {
	service.UserRepository
	user *service.User
}

func (r *provisionUserRepoStub) GetByID(_ context.Context, id int64) (*service.User, error) {
	if r.user == nil || id != r.user.ID {
		return nil, service.ErrUserNotFound
	}
	return r.user, nil
}

type provisionKeyRepoStub struct {
	service.APIKeyRepository
	key     *service.APIKey
	creates int
}

func (r *provisionKeyRepoStub) ListKeysByUserID(_ context.Context, userID int64) ([]string, error) {
	if r.key != nil && r.key.UserID == userID {
		return []string{r.key.Key}, nil
	}
	return nil, nil
}

func (r *provisionKeyRepoStub) ListByUserID(_ context.Context, _ int64, _ pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{}, nil
}

func (r *provisionKeyRepoStub) Create(_ context.Context, key *service.APIKey) error {
	r.creates++
	key.ID = 9001
	clone := *key
	clone.User = &service.User{ID: key.UserID, Status: service.StatusActive}
	r.key = &clone
	return nil
}

func (r *provisionKeyRepoStub) GetByKeyForAuth(_ context.Context, value string) (*service.APIKey, error) {
	if r.key == nil || r.key.Key != value {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}

func (r *provisionKeyRepoStub) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	if r.key == nil || r.key.ID != id {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}

func (r *provisionKeyRepoStub) BindOIDCIdentity(_ context.Context, id int64, issuer, subject string) error {
	if r.key == nil || r.key.ID != id || (r.key.OIDCIssuer != "" && (r.key.OIDCIssuer != issuer || r.key.OIDCSubject != subject)) {
		return service.ErrOIDCGatewayIdentityConflict
	}
	r.key.OIDCIssuer, r.key.OIDCSubject = issuer, subject
	return nil
}

func (r *provisionKeyRepoStub) GetByOIDCIdentity(_ context.Context, issuer, subject string) (*service.APIKey, error) {
	if r.key == nil || r.key.OIDCIssuer != issuer || r.key.OIDCSubject != subject {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}

func TestProvisionUserOIDCGatewayIdentityWithoutUserLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: 7, Status: service.StatusActive, APIOnly: true}
	repo := &provisionKeyRepoStub{}
	apiKeys := service.NewAPIKeyService(repo, &provisionUserRepoStub{user: user}, nil, nil, nil, nil, &config.Config{})
	h := NewAdminAPIKeyHandler(newStubAdminService(), apiKeys)
	router := gin.New()
	router.PUT("/api/v1/admin/users/:id/oidc-gateway-identity", h.ProvisionUserOIDCGatewayIdentity)

	const path = "/api/v1/admin/users/7/oidc-gateway-identity"
	const requestBody = `{"issuer":"https://idp.example.com/realms/tabro","subject":"user-7"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(requestBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var result struct {
			Data struct {
				APIKey struct {
					ID          int64  `json:"id"`
					Key         string `json:"key"`
					OIDCManaged bool   `json:"oidc_managed"`
				} `json:"api_key"`
				Issuer  string `json:"issuer"`
				Subject string `json:"subject"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, int64(9001), result.Data.APIKey.ID)
		require.Empty(t, result.Data.APIKey.Key, "internal billing key must not be disclosed")
		require.True(t, result.Data.APIKey.OIDCManaged)
		require.Equal(t, "https://idp.example.com/realms/tabro", result.Data.Issuer)
		require.Equal(t, "user-7", result.Data.Subject)
	}
	require.Equal(t, 1, repo.creates)
}
