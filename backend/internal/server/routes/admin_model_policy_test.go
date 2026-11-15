package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminModelSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *adminModelSettings) GetValue(_ context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return v, nil
}
func (s *adminModelSettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *adminModelSettings) GetAll(context.Context) (map[string]string, error) { return s.values, nil }
func (s *adminModelSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	m := map[string]string{}
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			m[key] = v
		}
	}
	return m, nil
}

func TestAdminModelPolicyRoutesRequireAdminAndPersistPriceAndAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &adminModelSettings{values: map[string]string{}}
	policies := service.NewAdminModelPolicyService(store, nil, nil, nil, nil)
	router := gin.New()
	RegisterAdminRoutes(router.Group("/api/v1"), &handler.Handlers{Admin: &handler.AdminHandlers{ModelPolicy: admin.NewModelPolicyHandler(policies)}}, middleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("X-Test-Role") != "admin" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}))
	call := func(method, path, body string, admin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if admin {
			req.Header.Set("X-Test-Role", "admin")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/admin/models"}, {"GET", "/api/v1/admin/models/detail?model=paraformer-realtime-v2"}, {"PUT", "/api/v1/admin/models"}, {"POST", "/api/v1/admin/models/reset-price"}} {
		require.Equal(t, http.StatusForbidden, call(route.method, route.path, `{}`, false).Code)
	}
	body := `{"model":"paraformer-realtime-v2","enabled":false,"pricing":{"billing_mode":"audio","price_unit":"second","unit_price":0.001}}`
	rec := call(http.MethodPut, "/api/v1/admin/models", body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	allowed, err := policies.IsModelEnabled(context.Background(), "paraformer-realtime-v2")
	require.NoError(t, err)
	require.False(t, allowed)
	price, err := policies.GetPricing(context.Background(), "paraformer-realtime-v2")
	require.NoError(t, err)
	require.Equal(t, 0.001, *price.PerRequestPrice)
	rec = call(http.MethodGet, "/api/v1/admin/models?enabled=false", ``, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var listed struct {
		Data service.AdminModelPolicyList `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Equal(t, 1, listed.Data.Total)
	rec = call(http.MethodPost, "/api/v1/admin/models/reset-price", `{"model":"paraformer-realtime-v2"}`, true)
	require.Equal(t, http.StatusOK, rec.Code)
	allowed, err = policies.IsModelEnabled(context.Background(), "paraformer-realtime-v2")
	require.NoError(t, err)
	require.False(t, allowed)
	price, err = policies.GetPricing(context.Background(), "paraformer-realtime-v2")
	require.NoError(t, err)
	require.Nil(t, price)
	rec = call(http.MethodPut, "/api/v1/admin/models", `{"model":"x","enabled":true,"pricing":{"billing_mode":"audio","price_unit":"second","unit_price":-1}}`, true)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = call(http.MethodPut, "/api/v1/admin/models", `{"model":"multimedia-model","enabled":true,"pricing":{"billing_mode":"duration","price_unit":"second","input_audio_price_per_second":0,"output_audio_price_per_second":0.02,"output_video_price_per_second":0.3}}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, "/api/v1/admin/models/detail?model=multimedia-model", "", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var duration struct {
		Data service.AdminModelPolicy `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &duration))
	require.Equal(t, service.BillingModeDuration, duration.Data.Pricing.BillingMode)
	require.NotNil(t, duration.Data.Pricing.InputAudioPricePerSecond)
	require.Zero(t, *duration.Data.Pricing.InputAudioPricePerSecond)
	require.Equal(t, 0.02, *duration.Data.Pricing.OutputAudioPricePerSecond)
	require.Equal(t, 0.3, *duration.Data.Pricing.OutputVideoPricePerSecond)
	require.Nil(t, duration.Data.Pricing.UnitPrice)
	rec = call(http.MethodPut, "/api/v1/admin/models", `{"model":"x","enabled":true,"pricing":{"billing_mode":"duration","price_unit":"second"}}`, true)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	for _, tc := range []struct {
		name   string
		query  string
		models []string
	}{
		{name: "provider filter omitted", models: []string{"multimedia-model", "paraformer-realtime-v2"}},
		{name: "without provider", query: "?has_provider=false", models: []string{"multimedia-model", "paraformer-realtime-v2"}},
		{name: "with provider", query: "?has_provider=true", models: []string{}},
		{name: "disabled without provider", query: "?has_provider=false&enabled=false&search=paraformer", models: []string{"paraformer-realtime-v2"}},
		{name: "enabled without provider", query: "?has_provider=false&enabled=true&search=multimedia", models: []string{"multimedia-model"}},
		{name: "enabled and search both apply", query: "?has_provider=false&enabled=true&search=paraformer", models: []string{}},
		{name: "provider required with other filters", query: "?has_provider=true&enabled=false&search=paraformer", models: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(http.MethodGet, "/api/v1/admin/models"+tc.query, "", true)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var listed struct {
				Data struct {
					Items []struct {
						Model       string `json:"model"`
						HasProvider *bool  `json:"has_provider"`
					} `json:"items"`
					Total int `json:"total"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
			require.Equal(t, len(tc.models), listed.Data.Total)
			models := make([]string, 0, len(listed.Data.Items))
			for _, item := range listed.Data.Items {
				models = append(models, item.Model)
				require.NotNil(t, item.HasProvider, "has_provider must be present for %s", item.Model)
				require.False(t, *item.HasProvider, "manual model %s has no configured provider", item.Model)
			}
			require.ElementsMatch(t, tc.models, models)
		})
	}

	for _, value := range []string{"invalid", "yes", "null"} {
		t.Run("invalid provider filter "+value, func(t *testing.T) {
			rec := call(http.MethodGet, "/api/v1/admin/models?has_provider="+value, "", true)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			var result struct {
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			require.Equal(t, "has_provider must be true or false", result.Message)
		})
	}

	t.Run("detail reports provider presence", func(t *testing.T) {
		rec := call(http.MethodGet, "/api/v1/admin/models/detail?model=multimedia-model", "", true)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var detail struct {
			Data struct {
				HasProvider *bool `json:"has_provider"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
		require.NotNil(t, detail.Data.HasProvider)
		require.False(t, *detail.Data.HasProvider)
	})
}
