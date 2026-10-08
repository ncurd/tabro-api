package repository

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func billingSourceUsageRows(t *testing.T, source string) *sqlmock.Rows {
	t.Helper()
	mode := "per_request"
	log := &service.UsageLog{ID: 7, UserID: 11, APIKeyID: 22, AccountID: 33,
		RequestID: "frozen-price", Model: "example", BillingMode: &mode,
		InputTokens: 10, InputCost: .25, TotalCost: .5, ActualCost: 3, RateMultiplier: 6,
		CreatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	prepared := prepareUsageLogInsert(log)
	values := []driver.Value{log.ID}
	for _, arg := range prepared.args {
		value, err := driver.DefaultParameterConverter.ConvertValue(arg)
		require.NoError(t, err)
		values = append(values, value)
	}
	values = append(values, source)
	columns := append(strings.Split(usageLogStoredSelectColumns, ", "), "billing_source")
	return sqlmock.NewRows(columns).AddRow(values...)
}

func TestUsageLogBillingSource_UsesCommittedOwnerAndPreservesPricingMode(t *testing.T) {
	require.Contains(t, usageLogBillingSourceExpression, "gul.request_id = usage_logs.request_id")
	require.Contains(t, usageLogBillingSourceExpression, "gul.api_key_id = usage_logs.api_key_id")
	require.Contains(t, usageLogBillingSourceExpression, "gul.user_id = usage_logs.user_id")
	require.Contains(t, usageLogBillingSourceExpression, "gul.billing_mode = 'central'")
	require.Contains(t, usageLogBillingSourceExpression, "usage_logs.billing_mode = 'central'")
	require.NotContains(t, usageLogBillingSourceExpression, "oidc_issuer")

	for _, source := range []string{"central", "local"} {
		t.Run(source, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT " + usageLogSelectColumns + " FROM usage_logs WHERE id = $1")).
				WithArgs(int64(7)).WillReturnRows(billingSourceUsageRows(t, source)).RowsWillBeClosed()
			log, err := repo.GetByID(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, source, log.BillingSource)
			require.Equal(t, "per_request", *log.BillingMode)
			require.Equal(t, .25, log.InputCost)
			require.Equal(t, .5, log.TotalCost)
			require.Equal(t, 3.0, log.ActualCost)
			require.Equal(t, 6.0, log.RateMultiplier)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUsageLogBillingSource_PaginatedQueryDoesNotAddPerRecordRoundTrips(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	params := pagination.PaginationParams{Page: 1, PageSize: 20}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM usage_logs WHERE user_id = $1")).
		WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE user_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3")).
		WithArgs(int64(11), 20, 0).WillReturnRows(billingSourceUsageRows(t, "central")).RowsWillBeClosed()
	logs, page, err := repo.ListByUser(context.Background(), 11, params)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "central", logs[0].BillingSource)
	require.Equal(t, "per_request", *logs[0].BillingMode)
	require.NoError(t, mock.ExpectationsWereMet())
}
