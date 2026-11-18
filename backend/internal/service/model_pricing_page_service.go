package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/aliyun"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const pricePerMillionMultiplier = 1_000_000

type ModelPricingGroupProvider interface {
	GetModelPricingGroups(ctx context.Context, userID int64) ([]Group, error)
	GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error)
}

type ModelPricingAccountProvider interface {
	ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error)
}

type ModelPricingResolverProvider interface {
	Resolve(ctx context.Context, input PricingInput) *ResolvedPricing
	GetIntervalPricing(resolved *ResolvedPricing, totalContextTokens int) *ModelPricing
}

type ModelPricingBillingPolicyProvider interface {
	GetOIDCBillingPolicy(ctx context.Context) (OIDCBillingPolicy, error)
}

type ModelPricingPageService struct {
	groupProvider   ModelPricingGroupProvider
	accountProvider ModelPricingAccountProvider
	resolver        ModelPricingResolverProvider
	billingPolicy   ModelPricingBillingPolicyProvider
	modelPolicies   *AdminModelPolicyService
}

func (s *ModelPricingPageService) SetAdminModelPolicies(policies *AdminModelPolicyService) {
	s.modelPolicies = policies
}

func NewModelPricingPageService(
	groupProvider ModelPricingGroupProvider,
	accountProvider ModelPricingAccountProvider,
	resolver ModelPricingResolverProvider,
	billingPolicy ModelPricingBillingPolicyProvider,
) *ModelPricingPageService {
	return &ModelPricingPageService{
		groupProvider:   groupProvider,
		accountProvider: accountProvider,
		resolver:        resolver,
		billingPolicy:   billingPolicy,
	}
}

type AvailableModelPricingResponse struct {
	Groups                    []AvailableModelPricingGroup `json:"groups"`
	OIDCBillingEnabled        bool                         `json:"oidc_billing_enabled"`
	OIDCBillingRateMultiplier float64                      `json:"oidc_billing_rate_multiplier"`
}

type AvailableModelPricingGroup struct {
	ID                      int64                        `json:"id"`
	Name                    string                       `json:"name"`
	Platform                string                       `json:"platform"`
	RateMultiplier          float64                      `json:"rate_multiplier"`
	EffectiveRateMultiplier float64                      `json:"effective_rate_multiplier"`
	Models                  []AvailableModelPricingModel `json:"models"`
}

type AvailableModelPricingModel struct {
	Error                            error                       `json:"-"`
	ID                               string                      `json:"id"`
	PricingAvailable                 bool                        `json:"pricing_available"`
	BillingMode                      BillingMode                 `json:"billing_mode"`
	UnitPrice                        *float64                    `json:"unit_price,omitempty"`
	PriceUnit                        string                      `json:"price_unit"`
	InputAudioPricePerSecond         *float64                    `json:"input_audio_price_per_second,omitempty"`
	OutputAudioPricePerSecond        *float64                    `json:"output_audio_price_per_second,omitempty"`
	OutputVideoPricePerSecond        *float64                    `json:"output_video_price_per_second,omitempty"`
	Tiers                            []AvailableModelPricingTier `json:"tiers,omitempty"`
	InputPricePerMillion             float64                     `json:"input_price_per_million,omitempty"`
	OutputPricePerMillion            float64                     `json:"output_price_per_million,omitempty"`
	CacheWritePricePerMillion        float64                     `json:"cache_write_price_per_million,omitempty"`
	CacheReadPricePerMillion         float64                     `json:"cache_read_price_per_million,omitempty"`
	PriorityInputPricePerMillion     float64                     `json:"priority_input_price_per_million,omitempty"`
	PriorityOutputPricePerMillion    float64                     `json:"priority_output_price_per_million,omitempty"`
	PriorityCacheReadPricePerMillion float64                     `json:"priority_cache_read_price_per_million,omitempty"`
	ImageOutputPricePerMillion       *float64                    `json:"image_output_price_per_million,omitempty"`
	ImageInputPricePerMillion        *float64                    `json:"image_input_price_per_million,omitempty"`
	CachedImageInputPricePerMillion  *float64                    `json:"cached_image_input_price_per_million,omitempty"`
	Source                           string                      `json:"source,omitempty"`
}

type AvailableModelPricingTier struct {
	Label     string  `json:"label"`
	UnitPrice float64 `json:"unit_price"`
	MinTokens int     `json:"min_tokens,omitempty"`
	MaxTokens *int    `json:"max_tokens,omitempty"`
}

func (s *ModelPricingPageService) ListAvailablePricing(ctx context.Context, userID int64) (*AvailableModelPricingResponse, error) {
	// Read the same validated policy used when freezing request prices. A failed
	// settings read must not publish local prices as if Auth conversion were off.
	policy := defaultOIDCBillingPolicy()
	if s.billingPolicy != nil {
		var err error
		policy, err = s.billingPolicy.GetOIDCBillingPolicy(ctx)
		if err != nil {
			return nil, fmt.Errorf("get model pricing billing policy: %w", err)
		}
	}
	oidcRate := 1.0
	if policy.Enabled {
		if !finiteNonnegativeCredit(policy.RateMultiplier) || policy.RateMultiplier <= 0 {
			return nil, fmt.Errorf("invalid model pricing OIDC billing multiplier")
		}
		oidcRate = policy.RateMultiplier
	}

	groups, err := s.groupProvider.GetModelPricingGroups(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get model pricing groups: %w", err)
	}

	userRates, err := s.groupProvider.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user group rates: %w", err)
	}

	resp := &AvailableModelPricingResponse{
		Groups:                    make([]AvailableModelPricingGroup, 0, len(groups)),
		OIDCBillingEnabled:        policy.Enabled,
		OIDCBillingRateMultiplier: oidcRate,
	}

	for i := range groups {
		group := groups[i]
		effectiveRate := group.RateMultiplier
		if rate, ok := userRates[group.ID]; ok {
			effectiveRate = rate
		}
		if policy.Enabled {
			if !finiteNonnegativeCredit(effectiveRate) || !finiteNonnegativeCredit(effectiveRate*oidcRate) {
				return nil, fmt.Errorf("invalid model pricing credit multiplier for group %d", group.ID)
			}
			effectiveRate *= oidcRate
		}

		var modelIDs []string
		var allAccounts []Account
		if group.Platform == PlatformAll {
			allAccounts, err = s.accountProvider.ListSchedulableByGroupID(ctx, group.ID)
			if err != nil {
				return nil, fmt.Errorf("get all-platform model pricing accounts: %w", err)
			}
			allAccounts = gatewayEligibleGroupAccounts(&group, allAccounts)
			modelIDs = gatewayAccountsModelIDs(allAccounts)
		} else {
			modelIDs = s.availableModelIDsForGroup(ctx, group)
		}
		if s.modelPolicies != nil {
			modelIDs, err = s.modelPolicies.FilterEnabledModels(ctx, modelIDs)
			if err != nil {
				return nil, fmt.Errorf("get available model policies: %w", err)
			}
		}
		models := make([]AvailableModelPricingModel, 0, len(modelIDs))
		for _, modelID := range modelIDs {
			priceCtx, priceModel := ctx, modelID
			if group.Platform == PlatformAll {
				var channels *ChannelService
				if resolver, ok := s.resolver.(*ModelPricingResolver); ok {
					channels = resolver.channelService
				}
				platform, platformErr := gatewayModelPlatform(ctx, &group, allAccounts, modelID, channels)
				if platformErr != nil {
					return nil, fmt.Errorf("get all-platform model channel prices: %w", platformErr)
				}
				if platform == "" {
					continue
				}
				effective := group
				effective.Platform, effective.Hydrated = platform, true
				priceCtx = context.WithValue(ctx, ctxkey.Group, &effective)
				if channels != nil {
					if _, channelErr := channels.GetChannelForGroup(priceCtx, group.ID); channelErr != nil {
						return nil, fmt.Errorf("get all-platform model channel prices: %w", channelErr)
					}
					mapping := channels.ResolveChannelMapping(priceCtx, group.ID, modelID)
					if mapping.BillingModelSource == BillingModelSourceChannelMapped && mapping.MappedModel != "" {
						priceModel = mapping.MappedModel
					}
				}
			}
			model := s.resolveModelPricing(priceCtx, group.ID, priceModel, effectiveRate)
			if model.Error != nil {
				return nil, fmt.Errorf("resolve model price: %w", model.Error)
			}
			model.ID = modelID
			if policy.Enabled && !finiteConvertedModelPricing(model) {
				return nil, fmt.Errorf("invalid converted model price for group %d model %q", group.ID, modelID)
			}
			models = append(models, model)
		}

		resp.Groups = append(resp.Groups, AvailableModelPricingGroup{
			ID:                      group.ID,
			Name:                    group.Name,
			Platform:                group.Platform,
			RateMultiplier:          group.RateMultiplier,
			EffectiveRateMultiplier: effectiveRate,
			Models:                  models,
		})
	}

	return resp, nil
}

func finiteConvertedModelPricing(model AvailableModelPricingModel) bool {
	prices := []float64{
		model.InputPricePerMillion, model.OutputPricePerMillion,
		model.CacheWritePricePerMillion, model.CacheReadPricePerMillion,
		model.PriorityInputPricePerMillion, model.PriorityOutputPricePerMillion,
		model.PriorityCacheReadPricePerMillion,
	}
	if model.ImageOutputPricePerMillion != nil {
		prices = append(prices, *model.ImageOutputPricePerMillion)
	}
	if model.ImageInputPricePerMillion != nil {
		prices = append(prices, *model.ImageInputPricePerMillion)
	}
	if model.CachedImageInputPricePerMillion != nil {
		prices = append(prices, *model.CachedImageInputPricePerMillion)
	}
	if model.UnitPrice != nil {
		prices = append(prices, *model.UnitPrice)
	}
	for _, price := range []*float64{model.InputAudioPricePerSecond, model.OutputAudioPricePerSecond, model.OutputVideoPricePerSecond} {
		if price != nil {
			prices = append(prices, *price)
		}
	}
	for _, tier := range model.Tiers {
		prices = append(prices, tier.UnitPrice)
	}
	for _, price := range prices {
		if !finiteNonnegativeCredit(price) {
			return false
		}
	}
	return true
}

func (s *ModelPricingPageService) availableModelIDsForGroup(ctx context.Context, group Group) []string {
	accounts, err := s.accountProvider.ListSchedulableByGroupID(ctx, group.ID)
	if group.Platform == PlatformAll {
		return gatewayAccountsModelIDs(gatewayEligibleGroupAccounts(&group, accounts))
	}
	if err != nil || len(accounts) == 0 {
		return defaultModelIDsForPricingPlatform(group.Platform)
	}
	defaultPlatform := pricingDefaultPlatformForGroup(group.Platform, accounts)

	modelSet := make(map[string]struct{})
	hasMapping := false
	for i := range accounts {
		mapping := accounts[i].GetModelMapping()
		if len(mapping) == 0 {
			continue
		}
		hasMapping = true
		for modelID := range mapping {
			modelSet[modelID] = struct{}{}
		}
	}
	if !hasMapping {
		return defaultModelIDsForPricingPlatform(defaultPlatform)
	}

	models := make([]string, 0, len(modelSet))
	for modelID := range modelSet {
		models = append(models, modelID)
	}
	sort.Strings(models)
	return models
}

func pricingDefaultPlatformForGroup(groupPlatform string, accounts []Account) string {
	if strings.EqualFold(strings.TrimSpace(groupPlatform), PlatformOpenAI) && hasAliyunCompatibleBaseURL(accounts) {
		return "aliyun"
	}
	return groupPlatform
}

func hasAliyunCompatibleBaseURL(accounts []Account) bool {
	for i := range accounts {
		if accounts[i].Type != AccountTypeOAuth && aliyun.IsCompatibleBaseURL(accounts[i].GetCredential("base_url")) {
			return true
		}
	}
	return false
}

func defaultModelIDsForPricingPlatform(platform string) []string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformGemini:
		models := gemini.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.Name)
		}
		return ids
	case PlatformDashScope, PlatformVolcengineArk, PlatformAzureSpeech:
		return DefaultMediaModels(strings.ToLower(strings.TrimSpace(platform)))
	case "aliyun", "qwen":
		return aliyun.DefaultTextModelIDs()
	case "meta", "llama":
		return []string{
			"llama-3.3-70b-instruct",
			"llama-3.2-90b-vision-instruct",
			"llama-3.2-11b-vision-instruct",
			"llama-3.2-3b-instruct",
			"llama-3.2-1b-instruct",
			"llama-3.1-405b-instruct",
			"llama-3.1-70b-instruct",
			"llama-3.1-8b-instruct",
			"llama-3-70b-instruct",
			"llama-3-8b-instruct",
		}
	case "minimax":
		return []string{
			"minimax-m2.5",
			"minimax-m2.5-lightning",
			"minimax-m2.1",
			"minimax-m2.1-lightning",
			"minimax-m2",
		}
	default:
		return claude.DefaultModelIDs()
	}
}

func (s *ModelPricingPageService) resolveModelPricing(ctx context.Context, groupID int64, modelID string, effectiveRate float64) AvailableModelPricingModel {
	result := AvailableModelPricingModel{ID: modelID, BillingMode: BillingModeToken, PriceUnit: "million_tokens"}
	if s.resolver == nil {
		return result
	}

	resolved := s.resolver.Resolve(ctx, PricingInput{
		Model:   modelID,
		GroupID: &groupID,
	})
	if resolved == nil {
		return result
	}
	if resolved.Error != nil {
		result.Error = resolved.Error
		return result
	}
	result.Source = resolved.Source
	if resolved.Mode != "" {
		result.BillingMode = resolved.Mode
	}
	if result.BillingMode == BillingModeDuration {
		result.PriceUnit = "second"
		convert := func(price *float64) *float64 {
			if price == nil {
				return nil
			}
			value := *price * effectiveRate
			return &value
		}
		result.InputAudioPricePerSecond = convert(resolved.InputAudioPricePerSecond)
		result.OutputAudioPricePerSecond = convert(resolved.OutputAudioPricePerSecond)
		result.OutputVideoPricePerSecond = convert(resolved.OutputVideoPricePerSecond)
		result.PricingAvailable = result.InputAudioPricePerSecond != nil || result.OutputAudioPricePerSecond != nil || result.OutputVideoPricePerSecond != nil
		return result
	}
	if result.BillingMode != BillingModeToken {
		result.PriceUnit = modelPricingUnit(result.BillingMode, modelID)
		if resolved.PriceUnit != "" {
			result.PriceUnit = resolved.PriceUnit
		}
		if resolved.DefaultPriceConfigured {
			price := resolved.DefaultPerRequestPrice * effectiveRate
			result.UnitPrice = &price
			result.PricingAvailable = true
		}
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice == nil {
				continue
			}
			result.Tiers = append(result.Tiers, AvailableModelPricingTier{
				Label: tier.TierLabel, UnitPrice: *tier.PerRequestPrice * effectiveRate,
				MinTokens: tier.MinTokens, MaxTokens: tier.MaxTokens,
			})
			result.PricingAvailable = true
		}
		return result
	}

	pricing := s.resolver.GetIntervalPricing(resolved, 0)
	if pricing == nil {
		return result
	}

	result.PricingAvailable = true
	result.InputPricePerMillion = toEffectivePerMillion(pricing.InputPricePerToken, effectiveRate)
	result.OutputPricePerMillion = toEffectivePerMillion(pricing.OutputPricePerToken, effectiveRate)
	result.CacheWritePricePerMillion = toEffectivePerMillion(pricing.CacheCreationPricePerToken, effectiveRate)
	result.CacheReadPricePerMillion = toEffectivePerMillion(pricing.CacheReadPricePerToken, effectiveRate)
	result.PriorityInputPricePerMillion = toEffectivePerMillion(pricing.InputPricePerTokenPriority, effectiveRate)
	result.PriorityOutputPricePerMillion = toEffectivePerMillion(pricing.OutputPricePerTokenPriority, effectiveRate)
	result.PriorityCacheReadPricePerMillion = toEffectivePerMillion(pricing.CacheReadPricePerTokenPriority, effectiveRate)
	if pricing.HasImageOutputPricing || pricing.ImageOutputPricePerToken != 0 {
		price := toEffectivePerMillion(pricing.ImageOutputPricePerToken, effectiveRate)
		result.ImageOutputPricePerMillion = &price
	}
	if pricing.HasImageInputPricing || pricing.ImageInputPricePerToken != 0 {
		price := toEffectivePerMillion(pricing.ImageInputPricePerToken, effectiveRate)
		result.ImageInputPricePerMillion = &price
	}
	if pricing.HasCacheReadImagePricing || pricing.CacheReadImagePricePerToken != 0 {
		price := toEffectivePerMillion(pricing.CacheReadImagePricePerToken, effectiveRate)
		result.CachedImageInputPricePerMillion = &price
	}
	return result
}

func modelPricingUnit(mode BillingMode, modelID string) string {
	switch mode {
	case BillingModeVideo, BillingModeDuration:
		return "second"
	case BillingModeImage:
		return "image"
	case BillingModePerRequest:
		return "request"
	case BillingModeAudio:
		model := strings.ToLower(modelID)
		if IsNativeRealtimeASRModel(model) || strings.Contains(model, "asr") || strings.Contains(model, "transcri") {
			return "second"
		}
		if strings.Contains(model, "tts") || strings.Contains(model, "speech") {
			return "character"
		}
		return "audio_unit"
	default:
		return "million_tokens"
	}
}

func toEffectivePerMillion(pricePerToken float64, effectiveRate float64) float64 {
	if pricePerToken <= 0 {
		return 0
	}
	return pricePerToken * pricePerMillionMultiplier * effectiveRate
}
