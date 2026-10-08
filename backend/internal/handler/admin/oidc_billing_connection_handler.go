package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetOIDCBillingConnectionSettings returns the billing connector configuration
// without exposing client credentials.
// GET /api/v1/admin/settings/oidc-billing-connection
func (h *SettingHandler) GetOIDCBillingConnectionSettings(c *gin.Context) {
	settings, err := h.settingService.GetOIDCBillingConnectionSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateOIDCBillingConnectionSettings replaces the billing connector and gateway
// resource-server configuration. An empty client secret retains the saved value.
// PUT /api/v1/admin/settings/oidc-billing-connection
func (h *SettingHandler) UpdateOIDCBillingConnectionSettings(c *gin.Context) {
	var cfg service.OIDCBillingConnectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings, err := h.settingService.UpdateOIDCBillingConnectionConfig(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// SetupOIDCBillingConnectionSettings discovers and validates the billing
// connection for the already-saved OIDC provider before persisting it.
// POST /api/v1/admin/settings/oidc-billing-connection/setup
func (h *SettingHandler) SetupOIDCBillingConnectionSettings(c *gin.Context) {
	var req struct {
		ClientSecret string `json:"client_secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings, err := h.settingService.SetupOIDCBillingConnection(c.Request.Context(), req.ClientSecret)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
