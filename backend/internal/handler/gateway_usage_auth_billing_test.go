package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageAuthBillingNeverReportsLocalWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	ctx.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 17, UserID: 23, AuthBillingOnly: true,
		Status: service.StatusAPIKeyAuthBillingOnly,
	})
	ctx.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 23})

	(&GatewayHandler{}).Usage(ctx)

	require.Equal(t, http.StatusOK, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "auth_billing", body["mode"])
	require.Equal(t, "auth", body["billingAuthority"])
	require.Equal(t, true, body["isValid"])
	require.NotContains(t, body, "balance")
	require.NotContains(t, body, "remaining")
	require.NotContains(t, body, "quota")
}
