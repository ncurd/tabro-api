package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ModelPricingService interface {
	ListAvailablePricing(ctx context.Context, userID int64) (*service.AvailableModelPricingResponse, error)
}

type ModelPricingHandler struct {
	modelPricingService ModelPricingService
}

func NewModelPricingHandler(modelPricingService ModelPricingService) *ModelPricingHandler {
	return &ModelPricingHandler{modelPricingService: modelPricingService}
}

func (h *ModelPricingHandler) GetAvailable(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	result, err := h.modelPricingService.ListAvailablePricing(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

