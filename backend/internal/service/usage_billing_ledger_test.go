package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDetachedBillingContextIgnoresCancellationButPreservesTaskDeadline(t *testing.T) {
	cancelled, cancelCancelled := context.WithCancel(context.Background())
	cancelCancelled()
	detached, cancelDetached := detachedBillingContext(cancelled)
	defer cancelDetached()
	require.NoError(t, detached.Err(), "request cancellation must not drop known provider usage")

	taskCtx, cancelTask := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelTask()
	taskDeadline, ok := taskCtx.Deadline()
	require.True(t, ok)

	bounded, cancelBounded := detachedBillingContext(taskCtx)
	defer cancelBounded()
	boundedDeadline, ok := bounded.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, taskDeadline, boundedDeadline, time.Millisecond)
}

func TestUsageBillingCommandNormalizeDefaultsRequestedModel(t *testing.T) {
	cmd := &UsageBillingCommand{RequestID: " request-123 ", UpstreamRequestID: " provider-request-456 ", Model: " gpt-6-astra "}

	cmd.Normalize()

	require.Equal(t, "request-123", cmd.RequestID)
	require.Equal(t, "provider-request-456", cmd.UpstreamRequestID)
	require.Equal(t, "gpt-6-astra", cmd.Model)
	require.Equal(t, "gpt-6-astra", cmd.RequestedModel)
	require.NotEmpty(t, cmd.RequestFingerprint)
}

func TestBuildUsageBillingCommandCopiesDurableLedgerFields(t *testing.T) {
	oidcIssuer := "https://issuer.example"
	oidcSubject := "user-123"
	oidcTenant := "tenant-456"
	tabroRunID := "run-789"
	tabroProjectID := "project-012"
	upstreamModel := "claude-opus-5-20260901"
	serviceTier := "priority"
	reasoningEffort := "high"
	billingMode := "token"
	mediaType := "image/png"
	accountRateMultiplier := 0.8
	subscriptionID := int64(44)

	usageLog := &UsageLog{
		OIDCIssuer:            &oidcIssuer,
		OIDCSubject:           &oidcSubject,
		OIDCTenant:            &oidcTenant,
		TabroRunID:            &tabroRunID,
		TabroProjectID:        &tabroProjectID,
		Model:                 "claude-opus-5",
		RequestedModel:        "claude-opus-5",
		UpstreamModel:         &upstreamModel,
		ServiceTier:           &serviceTier,
		ReasoningEffort:       &reasoningEffort,
		BillingType:           BillingTypeSubscription,
		BillingMode:           &billingMode,
		SubscriptionID:        &subscriptionID,
		InputTokens:           101,
		OutputTokens:          202,
		CacheCreationTokens:   303,
		CacheReadTokens:       404,
		CacheCreation5mTokens: 33,
		CacheCreation1hTokens: 44,
		ImageOutputTokens:     505,
		ImageCount:            2,
		MediaType:             &mediaType,
		InputCost:             0.101,
		OutputCost:            0.202,
		CacheCreationCost:     0.303,
		CacheReadCost:         0.404,
		ImageOutputCost:       0.505,
		TotalCost:             1.515,
		ActualCost:            1.212,
		RateMultiplier:        1.25,
		AccountRateMultiplier: &accountRateMultiplier,
	}

	cmd := buildUsageBillingCommand("request-123", usageLog, &postUsageBillingParams{
		Cost: &CostBreakdown{
			TotalCost:  1.515,
			ActualCost: 1.212,
		},
		User:               &User{ID: 11},
		APIKey:             &APIKey{ID: 22},
		Account:            &Account{ID: 33},
		Subscription:       &UserSubscription{ID: subscriptionID},
		UpstreamRequestID:  "provider-request-456",
		IsSubscriptionBill: true,
	})

	require.NotNil(t, cmd)
	require.Equal(t, "request-123", cmd.RequestID)
	require.Equal(t, "provider-request-456", cmd.UpstreamRequestID)
	require.Equal(t, int64(11), cmd.UserID)
	require.Equal(t, int64(22), cmd.APIKeyID)
	require.Equal(t, int64(33), cmd.AccountID)
	require.Equal(t, &subscriptionID, cmd.SubscriptionID)
	require.Equal(t, &oidcIssuer, cmd.OIDCIssuer)
	require.Equal(t, &oidcSubject, cmd.OIDCSubject)
	require.Equal(t, &oidcTenant, cmd.OIDCTenant)
	require.Equal(t, &tabroRunID, cmd.TabroRunID)
	require.Equal(t, &tabroProjectID, cmd.TabroProjectID)
	require.Equal(t, "claude-opus-5", cmd.Model)
	require.Equal(t, "claude-opus-5", cmd.RequestedModel)
	require.Equal(t, &upstreamModel, cmd.UpstreamModel)
	require.Equal(t, serviceTier, cmd.ServiceTier)
	require.Equal(t, reasoningEffort, cmd.ReasoningEffort)
	require.Equal(t, billingMode, cmd.BillingMode)
	require.Equal(t, 101, cmd.InputTokens)
	require.Equal(t, 202, cmd.OutputTokens)
	require.Equal(t, 303, cmd.CacheCreationTokens)
	require.Equal(t, 404, cmd.CacheReadTokens)
	require.Equal(t, 33, cmd.CacheCreation5mTokens)
	require.Equal(t, 44, cmd.CacheCreation1hTokens)
	require.Equal(t, 505, cmd.ImageOutputTokens)
	require.Equal(t, 2, cmd.ImageCount)
	require.Equal(t, mediaType, cmd.MediaType)
	require.InDelta(t, 0.101, cmd.InputCost, 1e-12)
	require.InDelta(t, 0.202, cmd.OutputCost, 1e-12)
	require.InDelta(t, 0.303, cmd.CacheCreationCost, 1e-12)
	require.InDelta(t, 0.404, cmd.CacheReadCost, 1e-12)
	require.InDelta(t, 0.505, cmd.ImageOutputCost, 1e-12)
	require.InDelta(t, 1.515, cmd.TotalCost, 1e-12)
	require.InDelta(t, 1.212, cmd.ActualCost, 1e-12)
	require.InDelta(t, 1.25, cmd.RateMultiplier, 1e-12)
	require.Equal(t, &accountRateMultiplier, cmd.AccountRateMultiplier)
	require.InDelta(t, 1.515, cmd.SubscriptionCost, 1e-12)
}
