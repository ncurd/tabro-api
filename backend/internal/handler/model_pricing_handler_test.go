package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelPricingHandlerServiceStub struct {
	gotUserID int64
	resp      *service.AvailableModelPricingResponse
	err       error
}

func (s *modelPricingHandlerServiceStub) ListAvailablePricing(_ context.Context, userID int64) (*service.AvailableModelPricingResponse, error) {
	s.gotUserID = userID
	return s.resp, s.err
}

func TestModelPricingHandlerGetAvailableRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-pricing/available", nil)

	h := NewModelPricingHandler(&modelPricingHandlerServiceStub{})
	h.GetAvailable(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestModelPricingHandlerGetAvailableReturnsPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-pricing/available", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})

	stub := &modelPricingHandlerServiceStub{
		resp: &service.AvailableModelPricingResponse{
			Groups: []service.AvailableModelPricingGroup{{
				ID:       10,
				Name:     "OpenAI",
				Platform: service.PlatformOpenAI,
				Models: []service.AvailableModelPricingModel{{
					ID:                  "gpt-5.4",
					PricingAvailable:    true,
					InputPricePerMillion: 2.5,
				}},
			}},
		},
	}
	h := NewModelPricingHandler(stub)

	h.GetAvailable(c)

	require.Equal(t, int64(42), stub.gotUserID)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":{"groups":[{"id":10,"name":"OpenAI","platform":"openai","rate_multiplier":0,"effective_rate_multiplier":0,"models":[{"id":"gpt-5.4","pricing_available":true,"input_price_per_million":2.5}]}]}}`, rec.Body.String())
}

