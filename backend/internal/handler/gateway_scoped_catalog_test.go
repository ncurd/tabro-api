package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmptyAuthorizedCatalogueNeverFallsBackOrProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{gatewayService: &service.GatewayService{}}
	for _, test := range []struct {
		path    string
		status  int
		handler gin.HandlerFunc
		body    string
	}{
		{"/v1/models", http.StatusOK, h.Models, `"data":[]`},
		{"/antigravity/models", http.StatusOK, h.AntigravityModels, `"data":[]`},
		{"/v1beta/models", http.StatusOK, h.GeminiV1BetaListModels, `"models":[]`},
		{"/v1beta/models/private-model", http.StatusNotFound, h.GeminiV1BetaGetModel, "Model is not available"},
	} {
		t.Run(test.path, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1})
				c.Request = c.Request.WithContext(service.WithGatewayModelGroups(c.Request.Context(), nil))
				c.Next()
			})
			path := test.path
			if test.status == http.StatusNotFound {
				path = "/v1beta/models/:model"
			}
			router.GET(path, test.handler)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			require.Equal(t, test.status, recorder.Code)
			require.Contains(t, recorder.Body.String(), test.body)
		})
	}
}
