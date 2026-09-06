package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApply_CommitsLedgerWithBillingTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	requestID := "request-123"
	apiKeyID := int64(22)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).
		WithArgs(requestID, apiKeyID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT request_fingerprint\s+FROM usage_billing_dedup_archive`).
		WithArgs(requestID, apiKeyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO gateway_usage_ledger`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := NewUsageBillingRepository(nil, db)
	result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKeyID,
		UserID:         11,
		AccountID:      33,
		Model:          "gpt-6-astra",
		RequestedModel: "gpt-6-astra",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingRepositoryApply_LedgerFailureRollsBackBillingTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	requestID := "request-rollback"
	apiKeyID := int64(22)
	userID := int64(11)
	ledgerErr := errors.New("ledger unavailable")
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).
		WithArgs(requestID, apiKeyID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT request_fingerprint\s+FROM usage_billing_dedup_archive`).
		WithArgs(requestID, apiKeyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`UPDATE users`).
		WithArgs(1.25, userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(98.75))
	mock.ExpectExec(`INSERT INTO gateway_usage_ledger`).
		WillReturnError(ledgerErr)
	mock.ExpectRollback()

	repo := NewUsageBillingRepository(nil, db)
	result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKeyID,
		UserID:         userID,
		Model:          "claude-opus-5",
		RequestedModel: "claude-opus-5",
		BalanceCost:    1.25,
		ActualCost:     1.25,
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ledgerErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingRepositoryApply_RetriesTransientTransactionFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	requestID := "request-transient-retry"
	apiKeyID := int64(22)
	mock.ExpectBegin().WillReturnError(&pq.Error{Code: "40001", Message: "serialization failure"})
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).
		WithArgs(requestID, apiKeyID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT request_fingerprint\s+FROM usage_billing_dedup_archive`).
		WithArgs(requestID, apiKeyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO gateway_usage_ledger`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := NewUsageBillingRepository(nil, db)
	result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKeyID,
		UserID:         11,
		AccountID:      33,
		Model:          "gpt-6-astra",
		RequestedModel: "gpt-6-astra",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
