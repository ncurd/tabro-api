package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type billingUsageVerifierStub struct {
	principal *service.GatewayOIDCPrincipal
	err       error
	calls     int
}

func (s *billingUsageVerifierStub) Verify(context.Context, string) (*service.GatewayOIDCPrincipal, error) {
	s.calls++
	return s.principal, s.err
}

type billingUsageReaderStub struct {
	query service.GatewayBillingUsageQuery
	calls int
}

func (s *billingUsageReaderStub) ListGatewayBillingUsage(_ context.Context, q service.GatewayBillingUsageQuery) (*service.GatewayBillingUsagePage, error) {
	s.calls++
	s.query = q
	return &service.GatewayBillingUsagePage{Unit: "credit", Timezone: q.Timezone, Page: q.Page, PageSize: q.PageSize, Total: 1,
		Items: []service.GatewayBillingUsageItem{{ID: "1", Model: "gpt-6-sol", Credits: bc.Decimal("0.1234567891"), RateMultiplier: bc.Decimal("10"), State: "settled"}}, Daily: []service.GatewayBillingUsageDay{}}, nil
}
func billingUsageTestHandler() (*GatewayBillingUsageHandler, *billingUsageReaderStub, *billingUsageVerifierStub) {
	repo := &billingUsageReaderStub{}
	verifier := &billingUsageVerifierStub{principal: &service.GatewayOIDCPrincipal{Issuer: "https://auth.example", Subject: "user-a", Tenant: "tenant-a", ClientID: "app-a", Scopes: []string{"llm.invoke"}}}
	h := &GatewayBillingUsageHandler{repo: repo, verifier: verifier, now: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }}
	return h, repo, verifier
}
func billingUsageRequest(h *GatewayBillingUsageHandler, query, authorization string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v1/billing/usage", h.List)
	r := httptest.NewRequest(http.MethodGet, "/v1/billing/usage"+query, nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func TestGatewayBillingUsageUsesVerifiedIdentityAndExactDecimalStrings(t *testing.T) {
	h, repo, _ := billingUsageTestHandler()
	w := billingUsageRequest(h, "?date=2026-10-10&page=2&page_size=1&operation_id=op-1", "Bearer verified-subject-token")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"credits":"0.1234567891"`)
	require.Equal(t, service.GatewayBillingUsageIdentity{Issuer: "https://auth.example", Subject: "user-a", Tenant: "tenant-a", AppID: "app-a"}, repo.query.Identity)
	require.Equal(t, time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC), repo.query.Start)
	require.Equal(t, time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC), repo.query.End)
	require.Equal(t, "Asia/Shanghai", repo.query.Timezone)
	require.Equal(t, "op-1", repo.query.OperationID)
	require.Equal(t, 2, repo.query.Page)
	require.Equal(t, 1, repo.query.PageSize)
}
func TestGatewayBillingUsageRejectsUntrustedFiltersAndUnboundedQueries(t *testing.T) {
	for _, query := range []string{"?user_id=999", "?tenant_id=other", "?app_id=other", "?api_key_id=1", "?key=secret", "?page=0", "?page_size=101", "?page=1&page=2", "?date=2026-02-30", "?date=2026-10-10&start_date=2026-10-09", "?timezone=Local", "?timezone=invalid-zone", "?start_date=2026-01-01&end_date=2026-10-10"} {
		t.Run(query, func(t *testing.T) {
			h, repo, _ := billingUsageTestHandler()
			w := billingUsageRequest(h, query, "Bearer token")
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, repo.calls)
		})
	}
}
func TestGatewayBillingUsageRequiresRealGatewaySubjectAndScope(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		tenant, scope string
		authorization string
		status        int
	}{
		{name: "missing bearer", tenant: "tenant-a", scope: "llm.invoke", status: 401},
		{name: "api key", tenant: "tenant-a", scope: "llm.invoke", authorization: "sk-test", status: 401},
		{name: "invalid signature", err: service.ErrGatewayOIDCTokenInvalid, tenant: "tenant-a", scope: "llm.invoke", authorization: "Bearer bad-token", status: 401},
		{name: "missing required scope", err: service.ErrGatewayOIDCScopeDenied, tenant: "tenant-a", scope: "llm.invoke", authorization: "Bearer token", status: 403},
		{name: "resource settings narrowed", tenant: "tenant-a", scope: "models.read", authorization: "Bearer token", status: 403},
		{name: "service token has no tenant", scope: "llm.invoke", authorization: "Bearer token", status: 403},
		{name: "auth unavailable", err: service.ErrGatewayOIDCUnavailable, tenant: "tenant-a", scope: "llm.invoke", authorization: "Bearer token", status: 503},
		{name: "unknown error", err: errors.New("invalid token"), tenant: "tenant-a", scope: "llm.invoke", authorization: "Bearer token", status: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, verifier := billingUsageTestHandler()
			verifier.err = tc.err
			verifier.principal.Tenant = tc.tenant
			verifier.principal.Scopes = []string{tc.scope}
			w := billingUsageRequest(h, "", tc.authorization)
			require.Equal(t, tc.status, w.Code)
			require.Zero(t, repo.calls)
		})
	}
}
func TestGatewayBillingUsageCalendarRangesRemainDSTSafe(t *testing.T) {
	h, repo, _ := billingUsageTestHandler()
	w := billingUsageRequest(h, "?date=2026-03-08&timezone=America%2FNew_York", "Bearer token")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 23*time.Hour, repo.query.End.Sub(repo.query.Start))
}
