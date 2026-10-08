package handler

import (
	"errors"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type oidcAccountLinks struct {
	CreditDetails    string `json:"credit_details,omitempty"`
	Profile          string `json:"profile,omitempty"`
	SecuritySettings string `json:"security_settings,omitempty"`
}

type oidcAccountResponse struct {
	Status     string           `json:"status"`
	Issuer     *string          `json:"issuer"`
	Subject    *string          `json:"subject"`
	ClientID   *string          `json:"client_id"`
	SummaryURL *string          `json:"summary_url"`
	Links      oidcAccountLinks `json:"links"`
}

// GetOIDCAccount returns the identity already verified during this API session's
// OIDC login. The browser must independently match Auth's current cookie session
// to this issuer/subject before displaying its credits or workspace links.
// GET /api/v1/auth/oidc-account
func (h *AuthHandler) GetOIDCAccount(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	session, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	result := oidcAccountResponse{Status: "disabled"}
	if h.settingSvc == nil {
		response.Success(c, result)
		return
	}
	configuration, err := h.settingSvc.GetOIDCAccountConfiguration(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("OIDC_ACCOUNT_UNAVAILABLE", "OIDC account configuration is temporarily unavailable"))
		return
	}
	if !configuration.Enabled {
		response.Success(c, result)
		return
	}
	result.Status = "unlinked"
	if session.AuthMethod != service.AuthMethodOIDC || session.BillingAPIKeyID <= 0 || h.apiKeyService == nil {
		response.Success(c, result)
		return
	}
	key, err := h.apiKeyService.GetByID(c.Request.Context(), session.BillingAPIKeyID)
	if err != nil {
		if errors.Is(err, service.ErrAPIKeyNotFound) {
			response.Success(c, result)
			return
		}
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("OIDC_ACCOUNT_UNAVAILABLE", "OIDC identity is temporarily unavailable"))
		return
	}
	if key == nil || key.ID != session.BillingAPIKeyID || key.UserID != session.UserID || !key.OIDCManaged ||
		!key.IsActive() || key.IsExpired() || key.OIDCIssuer != configuration.Issuer || strings.TrimSpace(key.OIDCSubject) == "" {
		response.Success(c, result)
		return
	}
	provider, _ := url.Parse(configuration.Issuer) // validated by the settings service
	link := func(path string) string {
		endpoint := *provider
		endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
		endpoint.RawPath = ""
		return endpoint.String()
	}
	summary, _ := url.Parse(link("/api/account/header-summary"))
	summary.RawQuery = url.Values{"client_id": {configuration.ClientID}}.Encode()
	summaryURL := summary.String()
	result.Status = "linked"
	result.Issuer, result.Subject = &configuration.Issuer, &key.OIDCSubject
	result.ClientID, result.SummaryURL = &configuration.ClientID, &summaryURL
	result.Links = oidcAccountLinks{
		CreditDetails:    link("/Identity/Account/Manage/CreditDetails"),
		Profile:          link("/Identity/Account/Manage/Index"),
		SecuritySettings: link("/Identity/Account/Manage/TwoFactorAuthentication"),
	}
	response.Success(c, result)
}
