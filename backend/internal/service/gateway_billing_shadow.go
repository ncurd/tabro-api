package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Shadow estimates are pure queries against a frozen quote, so competing
// workers may safely retry them. The result is stored durably and never applied
// to a local wallet or converted into a central settlement.
func (s *GatewayBillingCoordinator) runShadowEstimates(ctx context.Context) {
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		if err := s.ReconcileShadows(ctx); err != nil && ctx.Err() == nil {
			logger.LegacyPrintf("billing_center", "shadow estimate failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *GatewayBillingCoordinator) ReconcileShadows(ctx context.Context) error {
	rows, err := s.repo.PendingBillingShadows(ctx, 20)
	if err != nil {
		return err
	}
	var firstErr error
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		estimate, err := s.authority.Estimate(ctx, row.Key, row.Quote.QuoteID, row.Usage)
		if err == nil {
			err = s.repo.CompleteBillingShadow(ctx, row, estimate)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err != nil {
			if auditErr := s.repo.MarkBillingShadowEstimateFailure(ctx, row); auditErr != nil {
				firstErr = auditErr
			}
		}
	}
	return firstErr
}
