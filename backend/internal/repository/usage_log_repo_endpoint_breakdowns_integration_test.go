//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryEndpointBreakdownsSQL(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	// A transaction-local table keeps this SQL regression isolated from fixtures
	// and exercises nullable endpoints and historical account pricing directly.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE usage_logs (
		user_id BIGINT DEFAULT 11, account_id BIGINT, billing_mode TEXT, created_at TIMESTAMPTZ,
		inbound_endpoint TEXT, upstream_endpoint TEXT,
		input_tokens INTEGER DEFAULT 10, output_tokens INTEGER DEFAULT 20,
		cache_creation_tokens INTEGER DEFAULT 2, cache_read_tokens INTEGER DEFAULT 3,
		total_cost NUMERIC, actual_cost NUMERIC, account_stats_cost NUMERIC,
		account_rate_multiplier NUMERIC, duration_ms INTEGER DEFAULT 100
	) ON COMMIT DROP`)
	require.NoError(t, err)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs
		(account_id, billing_mode, created_at, inbound_endpoint, upstream_endpoint,
		 total_cost, actual_cost, account_stats_cost, account_rate_multiplier)
		VALUES
		(7, 'token', $1, ' /v1/messages ', '/v1/responses', 2, 1, 1, 2),
		(7, 'token', $1, '/v1/messages', NULL, 4, 2, NULL, 0.5),
		(7, 'token', $1, NULL, '  ', 6, 3, 1, NULL),
		(7, 'image', $1, '/excluded-mode', '/excluded-mode', 100, 100, 100, 1),
		(8, 'token', $1, '/excluded-account', '/excluded-account', 100, 100, 100, 1),
		(7, 'token', $2, '/excluded-time', '/excluded-time', 100, 100, 100, 1)`, start, end)
	require.NoError(t, err)
	repo := &usageLogRepository{sql: tx}

	for _, tt := range []struct {
		name                      string
		userID                    int64
		messagesCost, unknownCost float64
	}{
		{name: "account pricing", messagesCost: 4, unknownCost: 1},
		{name: "user pricing", userID: 11, messagesCost: 3, unknownCost: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stats, err := repo.GetStatsWithFilters(ctx, UsageLogFilters{
				UserID: tt.userID, AccountID: 7, BillingMode: "token", StartTime: &start, EndTime: &end,
			})
			require.NoError(t, err)
			require.Equal(t, int64(3), stats.TotalRequests)
			require.Equal(t, int64(105), stats.TotalTokens)
			require.Equal(t, 12.0, stats.TotalCost)
			require.Equal(t, 6.0, stats.TotalActualCost)
			require.Equal(t, 5.0, *stats.TotalAccountCost)
			require.Equal(t, []EndpointStat{
				{Endpoint: "/v1/messages", Requests: 2, TotalTokens: 70, Cost: 6, ActualCost: tt.messagesCost},
				{Endpoint: "unknown", Requests: 1, TotalTokens: 35, Cost: 6, ActualCost: tt.unknownCost},
			}, stats.Endpoints)
			require.Len(t, stats.UpstreamEndpoints, 2)
			require.Equal(t, "unknown", stats.UpstreamEndpoints[0].Endpoint)
			require.Equal(t, int64(2), stats.UpstreamEndpoints[0].Requests)
			require.Len(t, stats.EndpointPaths, 3)
			for _, distribution := range [][]EndpointStat{stats.Endpoints, stats.UpstreamEndpoints, stats.EndpointPaths} {
				var requests, tokens int64
				var cost, actualCost float64
				for _, endpoint := range distribution {
					requests += endpoint.Requests
					tokens += endpoint.TotalTokens
					cost += endpoint.Cost
					actualCost += endpoint.ActualCost
				}
				require.Equal(t, stats.TotalRequests, requests)
				require.Equal(t, stats.TotalTokens, tokens)
				require.Equal(t, stats.TotalCost, cost)
				require.Equal(t, tt.messagesCost+tt.unknownCost, actualCost)
			}
		})
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM usage_logs")
	require.NoError(t, err)
	stats, err := repo.GetStatsWithFilters(ctx, UsageLogFilters{StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.Zero(t, stats.TotalRequests)
	require.Equal(t, []EndpointStat{}, stats.Endpoints)
	require.Equal(t, []EndpointStat{}, stats.UpstreamEndpoints)
	require.Equal(t, []EndpointStat{}, stats.EndpointPaths)
}
