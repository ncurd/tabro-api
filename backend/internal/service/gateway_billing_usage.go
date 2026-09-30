package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/shopspring/decimal"
)

// BillingCenterUsage meters are disjoint: OpenAI input has already had cache
// removed by RecordUsage; Claude reports fresh input directly. Image output is
// part of output_tokens and is removed here before charging the text meter.
func BillingCenterUsage(cmd *UsageBillingCommand) (map[string]bc.Decimal, error) {
	if cmd == nil {
		return nil, bc.ErrConflict
	}
	if cmd.ExactUsage != nil {
		for _, value := range cmd.ExactUsage {
			if _, err := value.MarshalJSON(); err != nil || strings.HasPrefix(string(value), "-") {
				return nil, bc.ErrConflict
			}
		}
		return cmd.ExactUsage, nil
	}
	raw := map[string]int{
		"input_tokens": cmd.InputTokens, "output_tokens": cmd.OutputTokens - cmd.ImageOutputTokens,
		"cache_read_tokens": cmd.CacheReadTokens, "image_output_tokens": cmd.ImageOutputTokens,
		"cache_write_tokens": cmd.CacheCreationTokens, "cache_write_5m_tokens": cmd.CacheCreation5mTokens, "cache_write_1h_tokens": cmd.CacheCreation1hTokens,
		"request_count": 1, "image_count": cmd.ImageCount,
	}
	if cmd.CacheCreation5mTokens+cmd.CacheCreation1hTokens > 0 {
		raw["cache_write_tokens"] = 0
		// A partial TTL breakdown uses the documented default 5-minute bucket for
		// remaining cache writes; it must not also charge the aggregate meter.
		if remaining := cmd.CacheCreationTokens - cmd.CacheCreation5mTokens - cmd.CacheCreation1hTokens; remaining > 0 {
			raw["cache_write_5m_tokens"] += remaining
		}
	}
	result := make(map[string]bc.Decimal, len(raw))
	for key, value := range raw {
		if value < 0 {
			return nil, fmt.Errorf("invalid central usage meter %s", key)
		}
		result[key] = bc.Decimal(strconv.Itoa(value))
	}
	return result, nil
}

func BillingCenterUsageForProduct(cmd *UsageBillingCommand, product string) (map[string]bc.Decimal, error) {
	if strings.HasSuffix(product, ":images") {
		if cmd == nil || cmd.ImageCount <= 0 {
			return nil, bc.ErrState
		}
		return map[string]bc.Decimal{"request_count": "1", "image_count": bc.Decimal(strconv.Itoa(cmd.ImageCount))}, nil
	}
	return BillingCenterUsage(cmd)
}

func attachCentralUsage(ctx context.Context, cmd *UsageBillingCommand) error {
	e := bc.ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	if cmd == nil {
		return bc.ErrConflict
	}
	usage, err := BillingCenterUsage(cmd)
	if err != nil {
		return err
	}
	if cmd.InputTokens+cmd.OutputTokens+cmd.CacheReadTokens+cmd.CacheCreationTokens+cmd.ImageCount <= 0 {
		return bc.ErrState
	}
	if strings.HasSuffix(e.ProductKey, ":images") {
		if cmd.ImageCount <= 0 {
			return bc.ErrState
		}
		usage = map[string]bc.Decimal{"request_count": "1", "image_count": bc.Decimal(strconv.Itoa(cmd.ImageCount))}
		cmd.ExactUsage = usage
	}
	return attachCentralUsageMeters(ctx, cmd, usage)
}
func attachCentralUsageMeters(ctx context.Context, cmd *UsageBillingCommand, usage map[string]bc.Decimal) error {
	e := bc.ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	op, err := e.Coordinator.Store.Get(ctx, e.Key)
	if err != nil {
		return err
	}
	if op.State != bc.Dispatched && op.State != bc.ReconciliationRequired && op.State != bc.SettlementPending && op.State != bc.Settled {
		return bc.ErrState
	}
	expectedVersion := op.RemoteVersion
	var persisted *bc.Event
	if reader, ok := e.Coordinator.Store.(interface {
		GetTerminalEvent(context.Context, bc.Key) (bc.Event, error)
	}); ok {
		previous, err := reader.GetTerminalEvent(ctx, e.Key)
		if err == nil {
			var request bc.SettleRequest
			if previous.Kind != bc.SettleEvent || json.Unmarshal(previous.Body, &request) != nil {
				return bc.ErrConflict
			}
			expectedVersion = request.ExpectedVersion
			persisted = &previous
		} else if !errors.Is(err, bc.ErrNotFound) {
			return err
		}
	}
	settlementUsage := usage
	if maximum, priced := e.Quote.Request.MaximumUsage[gatewayCreditMeter]; priced {
		if len(e.OriginalMaximumUsage) == 0 {
			return fmt.Errorf("%w: original usage bounds are unavailable", bc.ErrState)
		}
		for meter, measured := range usage {
			bound, ok := e.OriginalMaximumUsage[meter]
			measuredValue, measuredErr := decimal.NewFromString(string(measured))
			boundValue, boundErr := decimal.NewFromString(string(bound))
			if !ok || measuredErr != nil || boundErr != nil || measuredValue.IsNegative() || measuredValue.GreaterThan(boundValue) {
				return fmt.Errorf("%w: actual %s usage exceeds the original gateway bound", bc.ErrState, meter)
			}
		}
		actual, amountErr := gatewayCreditDecimal(cmd.ActualCost, false)
		if amountErr != nil {
			return amountErr
		}
		limit, limitErr := decimal.NewFromString(string(maximum))
		value, valueErr := decimal.NewFromString(string(actual))
		if limitErr != nil || valueErr != nil || limit.IsNegative() || value.GreaterThan(limit) {
			return fmt.Errorf("%w: calculated gateway credits exceed the frozen reservation", bc.ErrState)
		}
		settlementUsage = map[string]bc.Decimal{gatewayCreditMeter: actual}
	}
	event, err := bc.NewSettlement(e.Key, op.ReservationID, bc.SettleRequest{TransitionRequest: bc.TransitionRequest{EventID: "settle:" + e.Key.OperationID, ExpectedVersion: expectedVersion}, UsageItemID: e.Key.OperationID, Usage: settlementUsage, EvidenceReference: "gateway_usage_ledger:" + cmd.RequestID, UsageComplete: true})
	if err != nil {
		return err
	}
	if persisted != nil {
		before, err := bc.Fingerprint(persisted.Body)
		if err != nil {
			return err
		}
		after, err := bc.Fingerprint(event.Body)
		if err != nil {
			return err
		}
		if before != after {
			return bc.ErrConflict
		}
		event = *persisted
	}
	cmd.CentralEvent = &event
	// Provider quota remains local; customer money and its local cache must not
	// be touched. Account limits are charged by the authority's frozen snapshot.
	cmd.BalanceCost = 0
	cmd.SubscriptionCost = 0
	cmd.SubscriptionID = nil
	cmd.APIKeyQuotaCost = 0
	cmd.APIKeyRateLimitCost = 0
	cmd.BillingMode = "central"
	cmd.RequestFingerprint = ""
	cmd.Normalize()
	return nil
}
