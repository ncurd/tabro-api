package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type gatewayBillingUsageVerifier interface {
	Verify(context.Context, string) (*service.GatewayOIDCPrincipal, error)
}

type GatewayBillingUsageHandler struct {
	repo     service.GatewayBillingUsageRepository
	verifier gatewayBillingUsageVerifier
	now      func() time.Time
	settings *service.SettingService
}

func NewGatewayBillingUsageHandler(repo service.GatewayBillingUsageRepository, cfg *config.Config, keys *service.APIKeyService, settings *service.SettingService) *GatewayBillingUsageHandler {
	return &GatewayBillingUsageHandler{repo: repo, verifier: service.NewGatewayResourceServer(cfg, keys), now: time.Now, settings: settings}
}

// List is a read-only resource endpoint. Verify deliberately does not provision
// keys, resolve model groups, reserve credits or dispatch any supplier request.
func (h *GatewayBillingUsageHandler) List(c *gin.Context) {
	if h == nil || h.repo == nil || h.verifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing_usage_unavailable"})
		return
	}
	headers := c.Request.Header.Values("Authorization")
	if len(headers) != 1 || c.GetHeader("x-api-key") != "" || c.GetHeader("x-goog-api-key") != "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token"})
		return
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_token"})
		return
	}
	principal, err := h.verifier.Verify(c.Request.Context(), parts[1])
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, service.ErrGatewayOIDCScopeDenied) {
			status = http.StatusForbidden
		}
		if errors.Is(err, service.ErrGatewayOIDCUnavailable) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": "billing_usage_unauthorized"})
		return
	}
	if principal == nil || strings.TrimSpace(principal.Issuer) == "" || strings.TrimSpace(principal.Subject) == "" || strings.TrimSpace(principal.Tenant) == "" || strings.TrimSpace(principal.ClientID) == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "billing_usage_unauthorized"})
		return
	}
	hasInvokeScope := false
	for _, scope := range principal.Scopes {
		if scope == "llm.invoke" {
			hasInvokeScope = true
			break
		}
	}
	if !hasInvokeScope {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient_scope"})
		return
	}
	defaultTimezone := service.DefaultOIDCBillingSettlementTimezone
	if h.settings != nil && c.Query("timezone") == "" {
		policy, policyErr := h.settings.GetOIDCBillingPolicy(c.Request.Context())
		if policyErr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing_usage_unavailable"})
			return
		}
		defaultTimezone = policy.SettlementTimezone
	}
	q, err := parseGatewayBillingUsageQuery(c, h.now(), defaultTimezone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_billing_usage_query", "message": err.Error()})
		return
	}
	q.Identity = service.GatewayBillingUsageIdentity{Issuer: principal.Issuer, Subject: principal.Subject, Tenant: principal.Tenant, AppID: principal.ClientID}
	page, err := h.repo.ListGatewayBillingUsage(c.Request.Context(), q)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing_usage_unavailable"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, page)
}

func parseGatewayBillingUsageQuery(c *gin.Context, now time.Time, defaultTimezone string) (service.GatewayBillingUsageQuery, error) {
	var q service.GatewayBillingUsageQuery
	allowed := map[string]bool{"date": true, "start_date": true, "end_date": true, "timezone": true, "operation_id": true, "page": true, "page_size": true}
	for key, values := range c.Request.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return q, errors.New("unsupported or duplicate query parameter")
		}
	}
	q.Timezone = c.DefaultQuery("timezone", defaultTimezone)
	if q.Timezone == "Local" || q.Timezone == "" {
		return q, errors.New("invalid timezone")
	}
	location, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return q, errors.New("invalid timezone")
	}
	day := time.Date(now.In(location).Year(), now.In(location).Month(), now.In(location).Day(), 0, 0, 0, 0, location)
	q.Start = day.AddDate(0, 0, -29)
	q.End = day.AddDate(0, 0, 1)
	parseDate := func(value string) (time.Time, error) { return time.ParseInLocation("2006-01-02", value, location) }
	if value := c.Query("date"); value != "" {
		if c.Query("start_date") != "" || c.Query("end_date") != "" {
			return q, errors.New("date cannot be combined with a date range")
		}
		q.Start, err = parseDate(value)
		if err != nil {
			return q, errors.New("invalid date; use YYYY-MM-DD")
		}
		q.End = q.Start.AddDate(0, 0, 1)
	} else {
		if value := c.Query("start_date"); value != "" {
			q.Start, err = parseDate(value)
			if err != nil {
				return q, errors.New("invalid start_date; use YYYY-MM-DD")
			}
		}
		if value := c.Query("end_date"); value != "" {
			end, parseErr := parseDate(value)
			if parseErr != nil {
				return q, errors.New("invalid end_date; use YYYY-MM-DD")
			}
			q.End = end.AddDate(0, 0, 1)
		}
	}
	if !q.Start.Before(q.End) || q.End.After(q.Start.AddDate(0, 0, 90)) {
		return q, errors.New("date range must contain between 1 and 90 days")
	}
	q.OperationID = strings.TrimSpace(c.Query("operation_id"))
	if len(q.OperationID) > 200 || strings.ContainsAny(q.OperationID, "\r\n\x00") {
		return q, errors.New("invalid operation_id")
	}
	q.Page = 1
	q.PageSize = 50
	if value := c.Query("page"); value != "" {
		q.Page, err = strconv.Atoi(value)
		if err != nil || q.Page < 1 || q.Page > 10000 {
			return q, errors.New("page must be between 1 and 10000")
		}
	}
	if value := c.Query("page_size"); value != "" {
		q.PageSize, err = strconv.Atoi(value)
		if err != nil || q.PageSize < 1 || q.PageSize > 100 {
			return q, errors.New("page_size must be between 1 and 100")
		}
	}
	q.Start = q.Start.UTC()
	q.End = q.End.UTC()
	return q, nil
}
