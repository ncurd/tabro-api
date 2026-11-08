package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryGetStatsWithFiltersCombinesEndpointBreakdowns(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := UsageLogFilters{AccountID: 7, BillingMode: "token", StartTime: &start, EndTime: &end}
	where := regexp.QuoteMeta("WHERE account_id = $1 AND billing_mode = $2 AND created_at >= $3 AND created_at < $4")

	mock.ExpectQuery("FROM usage_logs\\s+"+where).
		WithArgs(int64(7), "token", start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "avg_duration_ms",
		}).AddRow(3, 10, 20, 5, 1.5, 1.2, 0.9, 100))
	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS actual_cost")+
		"[\\s\\S]*"+where+"[\\s\\S]*GROUP BY GROUPING SETS").
		WithArgs(int64(7), "token", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint_type", "endpoint", "requests", "total_tokens", "cost", "actual_cost"}).
			AddRow(3, "/v1/messages", 3, 35, 1.5, 0.9).
			AddRow(5, "/v1/responses", 2, 25, 1.0, 0.6).
			AddRow(6, "/v1/messages -> /v1/responses", 2, 25, 1.0, 0.6).
			AddRow(5, "unknown", 1, 10, 0.5, 0.3).
			AddRow(6, "/v1/messages -> unknown", 1, 10, 0.5, 0.3))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, []EndpointStat{{Endpoint: "/v1/messages", Requests: 3, TotalTokens: 35, Cost: 1.5, ActualCost: 0.9}}, stats.Endpoints)
	require.Equal(t, []EndpointStat{
		{Endpoint: "/v1/responses", Requests: 2, TotalTokens: 25, Cost: 1.0, ActualCost: 0.6},
		{Endpoint: "unknown", Requests: 1, TotalTokens: 10, Cost: 0.5, ActualCost: 0.3},
	}, stats.UpstreamEndpoints)
	require.Equal(t, []EndpointStat{
		{Endpoint: "/v1/messages -> /v1/responses", Requests: 2, TotalTokens: 25, Cost: 1.0, ActualCost: 0.6},
		{Endpoint: "/v1/messages -> unknown", Requests: 1, TotalTokens: 10, Cost: 0.5, ActualCost: 0.3},
	}, stats.EndpointPaths)
	require.Equal(t, int64(35), stats.TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet(), "the complete response should require exactly two queries")
}

func TestUsageLogRepositoryGetStatsWithFiltersEndpointFailurePreservesTotals(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("FROM usage_logs").WillReturnRows(sqlmock.NewRows([]string{
		"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
		"total_cost", "total_actual_cost", "total_account_cost", "avg_duration_ms",
	}).AddRow(3, 10, 20, 5, 1.5, 1.2, 0.9, 100))
	mock.ExpectQuery("GROUP BY GROUPING SETS").WillReturnError(errors.New("endpoint aggregation unavailable"))

	stats, err := repo.GetStatsWithFilters(context.Background(), usagestats.UsageLogFilters{})
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, []EndpointStat{}, stats.Endpoints)
	require.Equal(t, []EndpointStat{}, stats.UpstreamEndpoints)
	require.Equal(t, []EndpointStat{}, stats.EndpointPaths)
	require.NoError(t, mock.ExpectationsWereMet())
}
