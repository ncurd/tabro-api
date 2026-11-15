package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ModelPolicyHandler struct {
	policies *service.AdminModelPolicyService
}

func NewModelPolicyHandler(policies *service.AdminModelPolicyService) *ModelPolicyHandler {
	return &ModelPolicyHandler{policies: policies}
}

func (h *ModelPolicyHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	var enabled *bool
	if raw, exists := c.GetQuery("enabled"); exists {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "enabled must be true or false")
			return
		}
		enabled = &v
	}
	var hasProvider *bool
	if raw, exists := c.GetQuery("has_provider"); exists {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "has_provider must be true or false")
			return
		}
		hasProvider = &v
	}
	result, err := h.policies.List(c.Request.Context(), c.Query("search"), enabled, hasProvider, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *ModelPolicyHandler) Detail(c *gin.Context) {
	result, err := h.policies.Detail(c.Request.Context(), c.Query("model"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *ModelPolicyHandler) Update(c *gin.Context) {
	var input service.AdminModelPolicyUpdate
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid model policy request")
		return
	}
	result, err := h.policies.Update(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *ModelPolicyHandler) ResetPrice(c *gin.Context) {
	var input struct {
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid model policy request")
		return
	}
	result, err := h.policies.ResetPrice(c.Request.Context(), input.Model)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
