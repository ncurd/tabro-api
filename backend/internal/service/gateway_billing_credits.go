package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/shopspring/decimal"
)

const gatewayCreditProductKey = "gateway:credits"
const gatewayCreditMeter = "credit_amount"

// gatewayCreditPricer is the trusted gateway-side source of the maximum debit.
// Auth only holds and settles credits; it does not know model prices.
type gatewayCreditPricer interface {
	MaximumCredit(context.Context, *APIKey, string, bc.QuoteRequest) (bc.Decimal, json.RawMessage, error)
}

type gatewayCreditPriceCalculator struct{ gateway *GatewayService }

type gatewayCreditPriceSnapshot struct {
	BillingModel    string              `json:"billing_model"`
	ServiceTier     string              `json:"service_tier"`
	RateMultiplier  float64             `json:"rate_multiplier"`
	Resolved        *ResolvedPricing    `json:"resolved"`
	MediaKind       string              `json:"media_kind,omitempty"`
	MediaTier       string              `json:"media_tier,omitempty"`
	MediaPrice      *MediaPriceSnapshot `json:"media_price,omitempty"`
	ImageUnitPrice  *float64            `json:"image_unit_price,omitempty"`
	ImageUnitPrices map[string]float64  `json:"image_unit_prices,omitempty"`
}

func (p *gatewayCreditPriceCalculator) MaximumCredit(ctx context.Context, key *APIKey, path string, quote bc.QuoteRequest) (bc.Decimal, json.RawMessage, error) {
	s := p.gateway
	if s == nil || s.billingService == nil || s.resolver == nil || s.channelService == nil ||
		key == nil || key.Group == nil || key.GroupID == nil || *key.GroupID != key.Group.ID ||
		key.UserID <= 0 || !key.Group.IsActive() {
		return "", nil, fmt.Errorf("%w: authenticated gateway pricing context is unavailable", bc.ErrState)
	}
	if !strings.HasPrefix(quote.ProductKey, "ai:") {
		return "", nil, fmt.Errorf("%w: unsupported gateway credit product", bc.ErrState)
	}
	if isBillingTextPath(path) || strings.Contains(path, "/models/") {
		if quote.MaximumUsage["image_count"] != "0" {
			return "", nil, fmt.Errorf("%w: generated image pricing is not bounded by token limits", bc.ErrState)
		}
	} else if !isGatewayCreditMediaPath(path) && !IsBillingImagesPath(path) {
		return "", nil, fmt.Errorf("%w: gateway-priced credits are not bounded for this endpoint", bc.ErrState)
	}
	requestedModel := strings.TrimSuffix(strings.TrimPrefix(quote.ProductKey, "ai:"), ":images")
	if requestedModel == "" {
		return "", nil, bc.ErrConflict
	}
	channel, err := s.channelService.GetChannelForGroup(ctx, key.Group.ID)
	if err != nil || channel == nil {
		return "", nil, fmt.Errorf("%w: active group channel is required for gateway credit pricing", bc.ErrState)
	}
	mapping := s.channelService.ResolveChannelMapping(ctx, key.Group.ID, requestedModel)
	if mapping.BillingModelSource == BillingModelSourceUpstream {
		return "", nil, fmt.Errorf("%w: upstream-selected model has no frozen credit price", bc.ErrState)
	}
	billingModel := requestedModel
	if mapping.BillingModelSource == BillingModelSourceChannelMapped && mapping.MappedModel != "" {
		billingModel = mapping.MappedModel
	}
	rate := key.Group.RateMultiplier
	if s.userGroupRateRepo != nil {
		userRate, rateErr := s.userGroupRateRepo.GetByUserAndGroup(ctx, key.UserID, key.Group.ID)
		if rateErr != nil {
			return "", nil, fmt.Errorf("%w: user credit multiplier is unavailable: %v", bc.ErrState, rateErr)
		}
		if userRate != nil {
			rate = *userRate
		}
	}
	if !finiteNonnegativeCredit(rate) {
		return "", nil, fmt.Errorf("%w: invalid gateway credit multiplier", bc.ErrState)
	}
	if isGatewayCreditMediaPath(path) {
		return s.maximumMediaCredit(ctx, path, quote, billingModel, key.Group.ID, rate)
	}
	if IsBillingImagesPath(path) {
		return s.maximumImageCredit(ctx, quote, billingModel, key, rate)
	}
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: key.GroupID})
	if resolved == nil || resolved.Source != PricingSourceChannel && resolved.BasePricing == nil {
		return "", nil, fmt.Errorf("%w: model price is unavailable", bc.ErrState)
	}
	boundRate := rate
	if !isBillingTextPath(path) {
		// Gemini's long-context path can double the above-threshold input.
		boundRate *= 2
	}
	amount, err := maximumGatewayTokenCost(s.billingService, resolved, billingModel, quote.MaximumUsage, quote.ServiceTier, boundRate)
	if err != nil {
		return "", nil, err
	}
	maximum, err := gatewayCreditDecimal(amount, true)
	if err != nil {
		return "", nil, err
	}
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: billingModel, ServiceTier: quote.ServiceTier, RateMultiplier: rate, Resolved: resolved})
	if err != nil {
		return "", nil, fmt.Errorf("%w: cannot freeze gateway credit price: %v", bc.ErrState, err)
	}
	return maximum, snapshot, nil
}

func finiteNonnegativeCredit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func isGatewayCreditMediaPath(path string) bool {
	switch strings.TrimPrefix(path, "/v1") {
	case "/videos/generations", "/audio/transcriptions", "/audio/voices", "/audio/speech", "/audio/speech/jobs":
		return true
	default:
		return false
	}
}

func (s *GatewayService) maximumMediaCredit(ctx context.Context, path string, quote bc.QuoteRequest, model string, groupID int64, rate float64) (bc.Decimal, json.RawMessage, error) {
	kind, meter := "", ""
	switch strings.TrimPrefix(path, "/v1") {
	case "/videos/generations":
		kind, meter = MediaJobKindVideoGeneration, "video_seconds"
	case "/audio/transcriptions":
		kind, meter = MediaJobKindAudioTranscription, "audio_seconds"
	case "/audio/voices":
		kind, meter = MediaJobKindVoiceClone, "request_count"
	case "/audio/speech", "/audio/speech/jobs":
		kind, meter = MediaJobKindAudioSpeech, "audio_characters"
	default:
		return "", nil, bc.ErrState
	}
	price, err := snapshotMediaPrice(s.channelService.GetChannelModelPricing(ctx, groupID, model), kind)
	if err != nil {
		return "", nil, fmt.Errorf("%w: media price unavailable: %v", bc.ErrState, err)
	}
	unit, err := price.UnitPrice(quote.ServiceTier)
	if err != nil {
		return "", nil, err
	}
	qty, err := strconv.ParseFloat(string(quote.MaximumUsage[meter]), 64)
	if err != nil || !finiteNonnegativeCredit(qty) || qty <= 0 {
		return "", nil, bc.ErrConflict
	}
	if price.Mode == BillingModePerRequest {
		qty = 1
	}
	maximum, err := gatewayCreditDecimal(unit*qty*rate, true)
	if err != nil {
		return "", nil, err
	}
	snapshot, err := json.Marshal(gatewayCreditPriceSnapshot{BillingModel: model, RateMultiplier: rate,
		MediaKind: kind, MediaTier: normalizeMediaBillingTier(quote.ServiceTier), MediaPrice: price})
	if err != nil {
		return "", nil, err
	}
	return maximum, snapshot, nil
}

func (s *GatewayService) maximumImageCredit(ctx context.Context, quote bc.QuoteRequest, model string, key *APIKey, rate float64) (bc.Decimal, json.RawMessage, error) {
	count, err := strconv.Atoi(string(quote.MaximumUsage["image_count"]))
	if err != nil || count < 1 || count > 10 {
		return "", nil, bc.ErrConflict
	}
	snap := gatewayCreditPriceSnapshot{BillingModel: model, RateMultiplier: rate, ServiceTier: quote.ServiceTier}
	maxUnit := 0.0
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: model, GroupID: key.GroupID})
	if resolved != nil && resolved.Source == PricingSourceChannel && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) {
		selected := resolved.DefaultPerRequestPrice
		configured := resolved.DefaultPriceConfigured
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice == nil {
				continue
			}
			if !finiteNonnegativeCredit(*tier.PerRequestPrice) {
				return "", nil, bc.ErrState
			}
			if strings.EqualFold(tier.TierLabel, quote.ServiceTier) {
				selected, configured = *tier.PerRequestPrice, true
			}
			if *tier.PerRequestPrice > maxUnit {
				maxUnit = *tier.PerRequestPrice
			}
		}
		if !configured {
			return "", nil, fmt.Errorf("%w: image tier price unavailable", bc.ErrState)
		}
		if selected > maxUnit {
			maxUnit = selected
		}
		snap.ImageUnitPrice = &selected
	} else {
		prices, priceErr := gatewayImageUnitPrices(s.billingService, model, key.Group)
		if priceErr != nil {
			return "", nil, priceErr
		}
		snap.ImageUnitPrices = prices
		for _, unit := range prices {
			if unit > maxUnit {
				maxUnit = unit
			}
		}
	}
	maximum, err := gatewayCreditDecimal(maxUnit*float64(count)*rate, true)
	if err != nil {
		return "", nil, err
	}
	snapshot, err := json.Marshal(snap)
	return maximum, snapshot, err
}

func gatewayImageUnitPrices(billing *BillingService, model string, group *Group) (map[string]float64, error) {
	if billing == nil || group == nil {
		return nil, bc.ErrState
	}
	var catalogPrice float64
	if billing.pricingService != nil {
		if catalog := billing.pricingService.GetModelPricing(model); catalog != nil {
			catalogPrice = catalog.OutputCostPerImage
		}
	}
	if !finiteNonnegativeCredit(catalogPrice) {
		return nil, bc.ErrState
	}
	configured := map[string]*float64{"1K": group.ImagePrice1K, "2K": group.ImagePrice2K, "4K": group.ImagePrice4K}
	prices := make(map[string]float64, 3)
	for _, size := range []string{"1K", "2K", "4K"} {
		unit := catalogPrice
		if configured[size] != nil {
			unit = *configured[size]
		} else if catalogPrice <= 0 {
			return nil, fmt.Errorf("%w: image size %s has no configured credit price", bc.ErrState, size)
		}
		if configured[size] == nil && size == "2K" {
			unit *= 1.5
		} else if configured[size] == nil && size == "4K" {
			unit *= 2
		}
		if !finiteNonnegativeCredit(unit) {
			return nil, bc.ErrState
		}
		prices[size] = unit
	}
	return prices, nil
}

func gatewayCreditDecimal(value float64, roundUp bool) (bc.Decimal, error) {
	if !finiteNonnegativeCredit(value) || value > 999999999999999999 {
		return "", fmt.Errorf("%w: invalid gateway credit amount", bc.ErrConflict)
	}
	if roundUp && value > 0 {
		// Bound calculations and actual usage can sum the same floating rates in
		// different orders. Keep a few ULPs of reservation headroom only.
		for i := 0; i < 8; i++ {
			value = math.Nextafter(value, math.Inf(1))
		}
	}
	d := decimal.NewFromFloat(value)
	// Always round upward: positive sub-unit usage must never become free.
	// The maximum and actual debit share the authority's 10-place precision.
	d = d.RoundCeil(10)
	return bc.Decimal(d.String()), nil
}

func maximumGatewayTokenCost(billing *BillingService, resolved *ResolvedPricing, model string,
	usage map[string]bc.Decimal, serviceTier string, multiplier float64) (float64, error) {
	if billing == nil || resolved == nil || usage == nil || !finiteNonnegativeCredit(multiplier) {
		return 0, fmt.Errorf("%w: incomplete gateway price", bc.ErrState)
	}
	read := func(name string) (int, error) {
		raw, ok := usage[name]
		if !ok {
			return 0, fmt.Errorf("%w: missing %s bound", bc.ErrConflict, name)
		}
		value, err := strconv.ParseInt(string(raw), 10, 32)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("%w: invalid %s bound", bc.ErrConflict, name)
		}
		return int(value), nil
	}
	input, err := read("input_tokens")
	if err != nil {
		return 0, err
	}
	output, err := read("output_tokens")
	if err != nil {
		return 0, err
	}
	cacheRead, err := read("cache_read_tokens")
	if err != nil {
		return 0, err
	}
	cacheWrite, err := read("cache_write_tokens")
	if err != nil {
		return 0, err
	}
	cache5m, err := read("cache_write_5m_tokens")
	if err != nil {
		return 0, err
	}
	cache1h, err := read("cache_write_1h_tokens")
	if err != nil {
		return 0, err
	}
	imageOutput, err := read("image_output_tokens")
	if err != nil {
		return 0, err
	}
	if output <= 0 || input <= 0 {
		return 0, fmt.Errorf("%w: model input and output caps are required", bc.ErrState)
	}
	if resolved.Mode == BillingModePerRequest {
		count, countErr := read("request_count")
		if countErr != nil {
			return 0, countErr
		}
		if count <= 0 {
			return 0, bc.ErrConflict
		}
		price := resolved.DefaultPerRequestPrice
		if !resolved.DefaultPriceConfigured && len(resolved.RequestTiers) == 0 {
			return 0, fmt.Errorf("%w: per-request model price is unavailable", bc.ErrState)
		}
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice != nil {
				if !finiteNonnegativeCredit(*tier.PerRequestPrice) {
					return 0, bc.ErrState
				}
				if *tier.PerRequestPrice > price {
					price = *tier.PerRequestPrice
				}
			}
		}
		if !finiteNonnegativeCredit(price) {
			return 0, bc.ErrState
		}
		return price * float64(count) * multiplier, nil
	}
	if resolved.Mode != BillingModeToken {
		return 0, fmt.Errorf("%w: unsupported gateway credit pricing mode", bc.ErrState)
	}
	candidates := []*ModelPricing{resolved.BasePricing}
	for i := range resolved.Intervals {
		candidates = append(candidates, intervalToModelPricing(&resolved.Intervals[i], resolved.BasePricing, resolved.SupportsCacheBreakdown))
	}
	maximum := -1.0
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		candidate = billing.applyModelSpecificPricingPolicy(model, candidate)
		if !validGatewayTokenPrice(candidate) {
			return 0, fmt.Errorf("%w: invalid model credit price", bc.ErrState)
		}
		for _, longContext := range []bool{false, true} {
			for _, imageCount := range []int{0, min(imageOutput, output)} {
				tokens := UsageTokens{InputTokens: input, OutputTokens: output,
					CacheReadTokens: cacheRead, CacheCreationTokens: cacheWrite,
					CacheCreation5mTokens: cache5m, CacheCreation1hTokens: cache1h,
					ImageOutputTokens: imageCount}
				for _, tier := range []string{serviceTier, "default", "priority", "flex", "anthropic_fast"} {
					cost := billing.computeTokenBreakdown(candidate, tokens, multiplier, tier, longContext)
					if cost == nil || !finiteNonnegativeCredit(cost.ActualCost) {
						return 0, bc.ErrState
					}
					if cost.ActualCost > maximum {
						maximum = cost.ActualCost
					}
				}
			}
		}
	}
	if maximum < 0 {
		return 0, fmt.Errorf("%w: model credit price is unavailable", bc.ErrState)
	}
	return maximum, nil
}

func validGatewayTokenPrice(price *ModelPricing) bool {
	if price == nil {
		return false
	}
	for _, rate := range []float64{price.InputPricePerToken, price.OutputPricePerToken,
		price.InputPricePerTokenPriority, price.OutputPricePerTokenPriority,
		price.CacheReadPricePerToken, price.CacheReadPricePerTokenPriority,
		price.CacheCreationPricePerToken, price.CacheCreationPricePerTokenPriority,
		price.CacheCreation5mPrice, price.CacheCreation1hPrice,
		price.ImageOutputPricePerToken, price.LongContextInputMultiplier,
		price.LongContextOutputMultiplier} {
		if !finiteNonnegativeCredit(rate) {
			return false
		}
	}
	return true
}

// Frozen pricing ensures an admin edit during a streamed request cannot change
// the debit after the supplier has already consumed the reserved credits.
func (s *GatewayService) frozenGatewayCreditCost(ctx context.Context, e *bc.Execution, result *ForwardResult, opts *recordUsageOpts) (*CostBreakdown, string, float64, error) {
	if s == nil || s.billingService == nil || s.resolver == nil || e == nil || len(e.GatewayPricingSnapshot) == 0 || result == nil || opts == nil {
		return nil, "", 0, bc.ErrState
	}
	var snap gatewayCreditPriceSnapshot
	if json.Unmarshal(e.GatewayPricingSnapshot, &snap) != nil || snap.BillingModel == "" || !finiteNonnegativeCredit(snap.RateMultiplier) {
		return nil, "", 0, bc.ErrState
	}
	if strings.HasSuffix(e.ProductKey, ":images") {
		if result.ImageCount < 1 {
			return nil, "", 0, bc.ErrState
		}
		unit := 0.0
		if snap.ImageUnitPrice != nil {
			unit = *snap.ImageUnitPrice
		} else if snap.ImageUnitPrices != nil {
			var known bool
			unit, known = snap.ImageUnitPrices[result.ImageSize]
			if !known {
				return nil, "", 0, bc.ErrState
			}
		} else {
			return nil, "", 0, bc.ErrState
		}
		if !finiteNonnegativeCredit(unit) {
			return nil, "", 0, bc.ErrState
		}
		total := unit * float64(result.ImageCount)
		return &CostBreakdown{ActualCost: total * snap.RateMultiplier, TotalCost: total, BillingMode: string(BillingModeImage)}, snap.BillingModel, snap.RateMultiplier, nil
	}
	if snap.Resolved == nil || result.ImageCount > 0 {
		return nil, "", 0, fmt.Errorf("%w: image output requires a bounded product", bc.ErrState)
	}
	if snap.RateMultiplier == 0 {
		return &CostBreakdown{ActualCost: 0, TotalCost: 0, BillingMode: string(snap.Resolved.Mode)}, snap.BillingModel, 0, nil
	}
	tokens := UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens, CacheReadTokens: result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens, CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		ImageOutputTokens: result.Usage.ImageOutputTokens}
	actualTier := snap.ServiceTier
	if speed := strings.TrimSpace(result.Usage.Speed); speed != "" {
		actualTier = speed
		if strings.EqualFold(speed, "fast") {
			actualTier = "anthropic_fast"
		}
	}
	calculate := func(tokenUsage UsageTokens, rate float64) (*CostBreakdown, error) {
		return s.billingService.CalculateCostUnified(CostInput{Ctx: ctx, Model: snap.BillingModel,
			Tokens: tokenUsage, RequestCount: 1, RateMultiplier: rate, ServiceTier: actualTier,
			Resolver: s.resolver, Resolved: snap.Resolved})
	}
	if opts.LongContextThreshold <= 0 || opts.LongContextMultiplier <= 1 || snap.Resolved.Source == PricingSourceChannel ||
		tokens.CacheReadTokens+tokens.InputTokens <= opts.LongContextThreshold || snap.Resolved.Mode != BillingModeToken {
		cost, err := calculate(tokens, snap.RateMultiplier)
		return cost, snap.BillingModel, snap.RateMultiplier, err
	}
	// Keep Gemini's existing over-threshold split while using the frozen price.
	within := tokens
	beyond := UsageTokens{}
	if tokens.CacheReadTokens >= opts.LongContextThreshold {
		within.CacheReadTokens = opts.LongContextThreshold
		within.InputTokens = 0
		beyond.CacheReadTokens = tokens.CacheReadTokens - opts.LongContextThreshold
		beyond.InputTokens = tokens.InputTokens
	} else {
		within.InputTokens = opts.LongContextThreshold - tokens.CacheReadTokens
		beyond.InputTokens = tokens.InputTokens - within.InputTokens
	}
	baseCost, err := calculate(within, snap.RateMultiplier)
	if err != nil {
		return nil, "", 0, err
	}
	extraCost, err := calculate(beyond, snap.RateMultiplier*opts.LongContextMultiplier)
	if err != nil {
		return nil, "", 0, err
	}
	baseCost.InputCost += extraCost.InputCost
	baseCost.CacheReadCost += extraCost.CacheReadCost
	baseCost.TotalCost += extraCost.TotalCost
	baseCost.ActualCost += extraCost.ActualCost
	return baseCost, snap.BillingModel, snap.RateMultiplier, nil
}
