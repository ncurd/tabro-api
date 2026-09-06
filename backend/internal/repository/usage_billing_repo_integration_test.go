//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApply_DeduplicatesBalanceBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-" + uuid.NewString(),
		Name:   "billing",
		Quota:  1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:           requestID,
		APIKeyID:            apiKey.ID,
		UserID:              user.ID,
		AccountID:           account.ID,
		AccountType:         service.AccountTypeAPIKey,
		BalanceCost:         1.25,
		APIKeyQuotaCost:     1.25,
		APIKeyRateLimitCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result1)
	require.True(t, result1.Applied)
	require.True(t, result1.APIKeyQuotaExhausted)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result2)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id = $1", apiKey.ID).Scan(&quotaUsed))
	require.InDelta(t, 1.25, quotaUsed, 0.000001)

	var usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT usage_5h FROM api_keys WHERE id = $1", apiKey.ID).Scan(&usage5h))
	require.InDelta(t, 1.25, usage5h, 0.000001)

	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM api_keys WHERE id = $1", apiKey.ID).Scan(&status))
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)
}

func TestUsageBillingRepositoryApply_PersistsDurableGatewayUsageLedgerOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-ledger-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-ledger-" + uuid.NewString(),
		Name:   "usage-ledger",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-ledger-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	oidcIssuer := "https://issuer.example"
	oidcSubject := "subject-123"
	oidcTenant := "tenant-456"
	tabroRunID := "run-789"
	tabroProjectID := "project-012"
	upstreamModel := "claude-opus-5-20260901"
	upstreamRequestID := "provider-request-" + uuid.NewString()
	accountRateMultiplier := 0.75
	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:             requestID,
		UpstreamRequestID:     upstreamRequestID,
		APIKeyID:              apiKey.ID,
		UserID:                user.ID,
		AccountID:             account.ID,
		AccountType:           service.AccountTypeAPIKey,
		OIDCIssuer:            &oidcIssuer,
		OIDCSubject:           &oidcSubject,
		OIDCTenant:            &oidcTenant,
		TabroRunID:            &tabroRunID,
		TabroProjectID:        &tabroProjectID,
		Model:                 "claude-opus-5",
		RequestedModel:        "claude-opus-5",
		UpstreamModel:         &upstreamModel,
		ServiceTier:           "priority",
		ReasoningEffort:       "high",
		BillingType:           service.BillingTypeBalance,
		BillingMode:           "token",
		InputTokens:           101,
		OutputTokens:          202,
		CacheCreationTokens:   303,
		CacheReadTokens:       404,
		CacheCreation5mTokens: 33,
		CacheCreation1hTokens: 44,
		ImageOutputTokens:     505,
		InputCost:             0.101,
		OutputCost:            0.202,
		CacheCreationCost:     0.303,
		CacheReadCost:         0.404,
		ImageOutputCost:       0.505,
		TotalCost:             1.515,
		ActualCost:            1.212,
		RateMultiplier:        1.25,
		AccountRateMultiplier: &accountRateMultiplier,
		BalanceCost:           1.212,
	}

	first, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)

	second, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, second.Applied)

	var (
		gotIssuer                string
		gotSubject               string
		gotTenant                string
		gotRunID                 string
		gotProjectID             string
		gotUpstreamRequestID     string
		gotModel                 string
		gotRequestedModel        string
		gotUpstreamModel         string
		gotInputTokens           int
		gotOutputTokens          int
		gotCacheCreationTokens   int
		gotCacheReadTokens       int
		gotCacheCreation5mTokens int
		gotCacheCreation1hTokens int
		gotImageOutputTokens     int
		gotActualCost            float64
		gotBalanceCost           float64
		ledgerCount              int
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			upstream_request_id,
			oidc_issuer,
			oidc_subject,
			oidc_tenant,
			tabro_run_id,
			tabro_project_id,
			model,
			requested_model,
			upstream_model,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			actual_cost,
			balance_cost,
			COUNT(*) OVER ()
		FROM gateway_usage_ledger
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKey.ID).Scan(
		&gotUpstreamRequestID,
		&gotIssuer,
		&gotSubject,
		&gotTenant,
		&gotRunID,
		&gotProjectID,
		&gotModel,
		&gotRequestedModel,
		&gotUpstreamModel,
		&gotInputTokens,
		&gotOutputTokens,
		&gotCacheCreationTokens,
		&gotCacheReadTokens,
		&gotCacheCreation5mTokens,
		&gotCacheCreation1hTokens,
		&gotImageOutputTokens,
		&gotActualCost,
		&gotBalanceCost,
		&ledgerCount,
	))
	require.Equal(t, upstreamRequestID, gotUpstreamRequestID)
	require.Equal(t, oidcIssuer, gotIssuer)
	require.Equal(t, oidcSubject, gotSubject)
	require.Equal(t, oidcTenant, gotTenant)
	require.Equal(t, tabroRunID, gotRunID)
	require.Equal(t, tabroProjectID, gotProjectID)
	require.Equal(t, "claude-opus-5", gotModel)
	require.Equal(t, "claude-opus-5", gotRequestedModel)
	require.Equal(t, upstreamModel, gotUpstreamModel)
	require.Equal(t, 101, gotInputTokens)
	require.Equal(t, 202, gotOutputTokens)
	require.Equal(t, 303, gotCacheCreationTokens)
	require.Equal(t, 404, gotCacheReadTokens)
	require.Equal(t, 33, gotCacheCreation5mTokens)
	require.Equal(t, 44, gotCacheCreation1hTokens)
	require.Equal(t, 505, gotImageOutputTokens)
	require.InDelta(t, 1.212, gotActualCost, 0.0000000001)
	require.InDelta(t, 1.212, gotBalanceCost, 0.0000000001)
	require.Equal(t, 1, ledgerCount)
}

func TestUsageBillingRepositoryApply_LedgerFailureRollsBackChargeAndDedup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-ledger-rollback-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-ledger-rollback-" + uuid.NewString(),
		Name:   "usage-ledger-rollback",
	})
	requestID := uuid.NewString()

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      requestID,
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		Model:          strings.Repeat("m", 101),
		RequestedModel: "model",
		BalanceCost:    1.25,
		ActualCost:     1.25,
	})
	require.Error(t, err)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 100, balance, 0.000001)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Zero(t, dedupCount)

	var ledgerCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM gateway_usage_ledger WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&ledgerCount))
	require.Zero(t, ledgerCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesSubscriptionBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-group-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-" + uuid.NewString(),
		Name:    "billing-sub",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:  user.ID,
		GroupID: group.ID,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:        requestID,
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        0,
		SubscriptionID:   &subscription.ID,
		SubscriptionCost: 2.5,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 2.5, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryApply_RequestFingerprintConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-conflict-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-conflict-" + uuid.NewString(),
		Name:   "billing-conflict",
	})

	requestID := uuid.NewString()
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	})
	require.NoError(t, err)

	_, err = repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 2.50,
	})
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
}

func TestUsageBillingRepositoryApply_UpdatesAccountQuota(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-account-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-account-" + uuid.NewString(),
		Name:   "billing-account",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-quota-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
		Extra: map[string]any{
			"quota_limit": 100.0,
		},
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:        uuid.NewString(),
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        account.ID,
		AccountType:      service.AccountTypeAPIKey,
		AccountQuotaCost: 3.5,
	})
	require.NoError(t, err)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric, 0) FROM accounts WHERE id = $1", account.ID).Scan(&quotaUsed))
	require.InDelta(t, 3.5, quotaUsed, 0.000001)
}

func TestDashboardAggregationRepositoryCleanupUsageBillingDedup_BatchDeletesOldRows(t *testing.T) {
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	oldRequestID := "dedup-old-" + uuid.NewString()
	newRequestID := "dedup-new-" + uuid.NewString()
	oldCreatedAt := time.Now().UTC().AddDate(0, 0, -400)
	newCreatedAt := time.Now().UTC().Add(-time.Hour)

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $2, $3), ($4, 1, $5, $6)
	`,
		oldRequestID, strings.Repeat("a", 64), oldCreatedAt,
		newRequestID, strings.Repeat("b", 64), newCreatedAt,
	)
	require.NoError(t, err)

	require.NoError(t, repo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	var oldCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", oldRequestID).Scan(&oldCount))
	require.Equal(t, 0, oldCount)

	var newCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", newRequestID).Scan(&newCount))
	require.Equal(t, 1, newCount)

	var archivedCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup_archive WHERE request_id = $1", oldRequestID).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesAgainstArchivedKey(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	aggRepo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-archive-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-archive-" + uuid.NewString(),
		Name:   "billing-archive",
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_dedup
		SET created_at = $1
		WHERE request_id = $2 AND api_key_id = $3
	`, time.Now().UTC().AddDate(0, 0, -400), requestID, apiKey.ID)
	require.NoError(t, err)
	require.NoError(t, aggRepo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
}
