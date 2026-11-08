package service

import (
	"context"
	"errors"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

func openAIUsageBillingTokens(usage OpenAIUsage) UsageTokens {
	return UsageTokens{
		InputTokens:  max(usage.InputTokens-usage.CacheReadInputTokens-usage.CacheCreationInputTokens, 0),
		OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadInputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens, ImageOutputTokens: usage.ImageOutputTokens,
		ImageInputTokens: max(usage.ImageInputTokens-usage.CacheReadImageTokens, 0), CacheReadImageTokens: usage.CacheReadImageTokens,
	}
}

func addOpenAIImageBilledUsage(total *OpenAIUsage, usage OpenAIUsage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
	total.ImageInputTokens += usage.ImageInputTokens
	total.CacheReadImageTokens += usage.CacheReadImageTokens
	total.ImageOutputTokens += usage.ImageOutputTokens
}

func addOpenAIImageCost(total, extra *CostBreakdown) {
	total.InputCost += extra.InputCost
	total.OutputCost += extra.OutputCost
	total.ImageOutputCost += extra.ImageOutputCost
	total.CacheReadCost += extra.CacheReadCost
	total.CacheCreationCost += extra.CacheCreationCost
	total.TotalCost += extra.TotalCost
	total.ActualCost += extra.ActualCost
}

// Explicit image prices retain their existing per-image contract. Otherwise,
// GPT Image 2.6 uses reported image usage, plus separately reported orchestration
// and SVG author usage. A missing report is not a fixed-price image estimate.
func (s *OpenAIGatewayService) calculateOpenAIImage26Cost(ctx context.Context, input *OpenAIRecordUsageInput, model string, multiplier float64) (*CostBreakdown, OpenAIUsage, error) {
	result := input.Result
	totalUsage := result.Usage
	if result.ImageOrchestrationModel != "" && result.ImageUsage != nil {
		addOpenAIImageBilledUsage(&totalUsage, *result.ImageUsage)
	}
	for _, svg := range result.SVGUsage {
		addOpenAIImageBilledUsage(&totalUsage, OpenAIUsage{InputTokens: svg.InputTokens, OutputTokens: svg.OutputTokens})
	}
	var groupConfig *ImagePriceConfig
	if group := input.APIKey.Group; group != nil {
		groupConfig = &ImagePriceConfig{Price1K: group.ImagePrice1K, Price2K: group.ImagePrice2K, Price4K: group.ImagePrice4K}
	}
	if unit, configured := configuredImageUnitPrice(groupConfig, result.ImageSize); configured {
		if !finiteNonnegativeCredit(unit) {
			return nil, totalUsage, errors.New("GPT Image 2.6 image price must be finite and non-negative")
		}
		return s.billingService.CalculateImageCost(model, result.ImageSize, result.ImageCount, groupConfig, multiplier), totalUsage, nil
	}
	// Match the same authenticated group context used by ordinary token billing.
	if group := input.APIKey.Group; group != nil {
		ctx = context.WithValue(ctx, ctxkey.Group, group)
	}
	var resolved *ResolvedPricing
	if s.resolver != nil && input.APIKey.GroupID != nil {
		resolved = s.resolver.Resolve(ctx, PricingInput{Model: model, GroupID: input.APIKey.GroupID})
		if resolved != nil && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) {
			unit, configured := openAIImageConfiguredTierPrice(resolved, result.ImageBillingTier, result.ImageSize)
			if !configured || !finiteNonnegativeCredit(unit) {
				return nil, totalUsage, errors.New("GPT Image 2.6 image tier has no configured price")
			}
			if multiplier <= 0 {
				multiplier = 1
			}
			total := unit * float64(result.ImageCount)
			return &CostBreakdown{TotalCost: total, ActualCost: total * multiplier, BillingMode: string(resolved.Mode)}, totalUsage, nil
		}
	}
	imageUsage := result.ImageUsage
	if imageUsage == nil && result.ImageOrchestrationModel == "" && result.Usage.ImageUsageComplete {
		imageUsage = &result.Usage
	}
	if imageUsage == nil || !imageUsage.ImageUsageComplete || imageUsage.OutputTokens <= 0 {
		return nil, totalUsage, errors.New("complete image-model usage is required for GPT Image 2.6 token billing")
	}
	if result.SVGUsageIncomplete {
		return nil, totalUsage, errors.New("SVG author usage is required for GPT Image 2.6 token billing")
	}
	imageTokens := openAIUsageBillingTokens(*imageUsage)
	var cost *CostBreakdown
	var err error
	if resolved != nil {
		cost, err = s.billingService.CalculateCostUnified(CostInput{
			Ctx: ctx, Model: model, GroupID: input.APIKey.GroupID, Tokens: imageTokens, RequestCount: 1,
			RateMultiplier: multiplier, Resolver: s.resolver, Resolved: resolved,
		})
	} else {
		cost, err = s.billingService.CalculateCostWithServiceTier(model, imageTokens, multiplier, "")
	}
	if err != nil {
		return nil, totalUsage, err
	}
	cost.BillingMode = string(BillingModeToken)
	if result.ImageOrchestrationModel != "" {
		base, err := s.billingService.CalculateCostWithServiceTier(result.ImageOrchestrationModel, openAIUsageBillingTokens(result.Usage), multiplier, result.Usage.ResponseServiceTier)
		if err != nil {
			return nil, totalUsage, err
		}
		addOpenAIImageCost(cost, base)
	}
	for _, svg := range result.SVGUsage {
		extra, err := s.billingService.CalculateCostWithServiceTier(svg.Model, UsageTokens{InputTokens: svg.InputTokens, OutputTokens: svg.OutputTokens}, multiplier, "")
		if err != nil {
			return nil, totalUsage, err
		}
		addOpenAIImageCost(cost, extra)
	}
	return cost, totalUsage, nil
}

func openAIImageConfiguredTierPrice(resolved *ResolvedPricing, requestTier, size string) (float64, bool) {
	for _, label := range []string{requestTier, size} {
		if label == "" {
			continue
		}
		for _, tier := range resolved.RequestTiers {
			if strings.EqualFold(tier.TierLabel, label) && tier.PerRequestPrice != nil {
				return *tier.PerRequestPrice, true
			}
		}
	}
	return resolved.DefaultPerRequestPrice, resolved.DefaultPriceConfigured
}

// Native multi-image stream usage has no documented cumulative/per-image
// contract. It is safe to bill a configured image count, but not to guess token
// totals from the last completed event. OAuth batches have separate responses.
func (s *OpenAIGatewayService) validateOpenAIImage26NativeStream(ctx context.Context, body []byte) error {
	if !isGPTImage26Model(gjson.GetBytes(body, "model").String()) || !openAIImagesRequestWantsStream(body) || gjson.GetBytes(body, "n").Int() <= 1 || bc.IsCentral(ctx) {
		return nil
	}
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && group != nil {
		config := &ImagePriceConfig{Price1K: group.ImagePrice1K, Price2K: group.ImagePrice2K, Price4K: group.ImagePrice4K}
		size := normalizeOpenAIImageSize(gjson.GetBytes(body, "size").String())
		if unit, configured := configuredImageUnitPrice(config, size); configured && finiteNonnegativeCredit(unit) {
			return nil
		}
		if s.resolver != nil {
			resolved := s.resolver.Resolve(ctx, PricingInput{Model: gjson.GetBytes(body, "model").String(), GroupID: &group.ID})
			if resolved != nil && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) {
				if unit, configured := openAIImageConfiguredTierPrice(resolved, billingImageTier(body), size); configured && finiteNonnegativeCredit(unit) {
					return nil
				}
			}
		}
	}
	return &OpenAIImageRequestError{Message: "GPT Image 2.6 token billing requires n=1 for native streaming; use stream=false for multiple images or configure a per-image price"}
}

func (s *OpenAIGatewayService) applyOpenAIImage26AccountStatsCost(ctx context.Context, usageLog *UsageLog, input *OpenAIRecordUsageInput, tokens UsageTokens, totalCost float64) {
	if s.channelService == nil || input.APIKey.GroupID == nil {
		return
	}
	groupID := *input.APIKey.GroupID
	channel, err := s.channelService.GetChannelForGroup(ctx, groupID)
	if err != nil || channel == nil {
		return
	}
	result := input.Result
	model := forwardResultBillingModel(result.Model, result.UpstreamModel)
	if override := tryCustomRules(channel, input.Account.ID, groupID, s.channelService.GetGroupPlatform(ctx, groupID), model, tokens, result.ImageCount); override != nil {
		usageLog.AccountStatsCost = override
		return
	}
	if channel.ApplyPricingToAccountStats {
		usageLog.AccountStatsCost = &totalCost
		return
	}
	// A model-file provider estimate must price each model separately. The
	// aggregate ledger tokens mix image rendering, orchestration, and SVG text.
	if result.ImageUsage == nil || !result.ImageUsage.ImageUsageComplete || result.SVGUsageIncomplete {
		return
	}
	total := tryModelFilePricing(s.billingService, model, openAIUsageBillingTokens(*result.ImageUsage))
	if total == nil {
		return
	}
	if result.ImageOrchestrationModel != "" {
		base := tryModelFilePricing(s.billingService, result.ImageOrchestrationModel, openAIUsageBillingTokens(result.Usage))
		if base == nil {
			return
		}
		*total += *base
	}
	for _, svg := range result.SVGUsage {
		extra := tryModelFilePricing(s.billingService, svg.Model, UsageTokens{InputTokens: svg.InputTokens, OutputTokens: svg.OutputTokens})
		if extra == nil {
			return
		}
		*total += *extra
	}
	usageLog.AccountStatsCost = total
}
