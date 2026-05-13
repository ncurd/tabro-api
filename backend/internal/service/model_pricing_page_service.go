package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
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

type ModelPricingPageService struct {
	groupProvider   ModelPricingGroupProvider
	accountProvider ModelPricingAccountProvider
	resolver        ModelPricingResolverProvider
}

func NewModelPricingPageService(
	groupProvider ModelPricingGroupProvider,
	accountProvider ModelPricingAccountProvider,
	resolver ModelPricingResolverProvider,
) *ModelPricingPageService {
	return &ModelPricingPageService{
		groupProvider:   groupProvider,
		accountProvider: accountProvider,
		resolver:        resolver,
	}
}

type AvailableModelPricingResponse struct {
	Groups []AvailableModelPricingGroup `json:"groups"`
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
	ID                               string  `json:"id"`
	PricingAvailable                 bool    `json:"pricing_available"`
	InputPricePerMillion             float64 `json:"input_price_per_million,omitempty"`
	OutputPricePerMillion            float64 `json:"output_price_per_million,omitempty"`
	CacheWritePricePerMillion        float64 `json:"cache_write_price_per_million,omitempty"`
	CacheReadPricePerMillion         float64 `json:"cache_read_price_per_million,omitempty"`
	PriorityInputPricePerMillion     float64 `json:"priority_input_price_per_million,omitempty"`
	PriorityOutputPricePerMillion    float64 `json:"priority_output_price_per_million,omitempty"`
	PriorityCacheReadPricePerMillion float64 `json:"priority_cache_read_price_per_million,omitempty"`
	ImageOutputPricePerMillion       float64 `json:"image_output_price_per_million,omitempty"`
	Source                           string  `json:"source,omitempty"`
}

func (s *ModelPricingPageService) ListAvailablePricing(ctx context.Context, userID int64) (*AvailableModelPricingResponse, error) {
	groups, err := s.groupProvider.GetModelPricingGroups(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get model pricing groups: %w", err)
	}

	userRates, err := s.groupProvider.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user group rates: %w", err)
	}

	resp := &AvailableModelPricingResponse{
		Groups: make([]AvailableModelPricingGroup, 0, len(groups)),
	}

	for i := range groups {
		group := groups[i]
		effectiveRate := group.RateMultiplier
		if rate, ok := userRates[group.ID]; ok {
			effectiveRate = rate
		}

		modelIDs := s.availableModelIDsForGroup(ctx, group)
		models := make([]AvailableModelPricingModel, 0, len(modelIDs))
		for _, modelID := range modelIDs {
			models = append(models, s.resolveModelPricing(ctx, group.ID, modelID, effectiveRate))
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

func (s *ModelPricingPageService) availableModelIDsForGroup(ctx context.Context, group Group) []string {
	accounts, err := s.accountProvider.ListSchedulableByGroupID(ctx, group.ID)
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
		return "dashscope"
	}
	return groupPlatform
}

func hasAliyunCompatibleBaseURL(accounts []Account) bool {
	for i := range accounts {
		baseURL := strings.ToLower(strings.TrimSpace(accounts[i].GetCredential("base_url")))
		if baseURL == "" {
			continue
		}
		if strings.Contains(baseURL, "dashscope") ||
			strings.Contains(baseURL, "aliyuncs.com") ||
			strings.Contains(baseURL, "alibabacloud.com") {
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
	case "aliyun", "dashscope", "qwen":
		return []string{
			"qwen-turbo",
			"qwen-plus",
			"qwen-max",
			"qwen-coder",
			"qwq-plus",
			"qwen3-coder-flash",
			"qwen3-coder-plus",
			"qwen3-max",
			"qwen3-next-80b-a3b-instruct",
			"qwen3-next-80b-a3b-thinking",
			"qwen3-vl-plus",
			"qwen3-vl-235b-a22b-instruct",
			"qwen3-vl-32b-instruct",
			"qwen3.5-plus",
			"deepseek-chat",
			"deepseek-reasoner",
			"deepseek-v3",
			"deepseek-v3.2",
			"deepseek-r1",
			"minimax-m2.5",
			"minimax-m2.5-lightning",
			"minimax-m2.1",
			"minimax-m2.1-lightning",
			"minimax-m2",
			"llama-3.3-70b-instruct",
			"llama-3.1-405b-instruct",
			"llama-3.1-70b-instruct",
			"llama-3.1-8b-instruct",
			"llama-3-70b-instruct",
			"llama-3-8b-instruct",
			"kimi-latest",
			"kimi-latest-128k",
			"kimi-latest-32k",
			"kimi-latest-8k",
			"kimi-k2.5",
			"kimi-k2-thinking",
			"glm-4.5",
			"glm-4.5-air",
			"glm-4.5-flash",
			"glm-4.6",
			"glm-4.7",
			"glm-5",
		}
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
	result := AvailableModelPricingModel{ID: modelID}
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
	result.Source = resolved.Source

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
	result.ImageOutputPricePerMillion = toEffectivePerMillion(pricing.ImageOutputPricePerToken, effectiveRate)
	return result
}

func toEffectivePerMillion(pricePerToken float64, effectiveRate float64) float64 {
	if pricePerToken <= 0 {
		return 0
	}
	return pricePerToken * pricePerMillionMultiplier * effectiveRate
}
