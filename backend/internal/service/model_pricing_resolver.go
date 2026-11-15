package service

import (
	"context"
	"log/slog"
)

// PricingSource 定价来源标识
const (
	PricingSourceChannel  = "channel"
	PricingSourceLiteLLM  = "litellm"
	PricingSourceFallback = "fallback"
	PricingSourceManual   = "manual"
)

// ResolvedPricing 统一定价解析结果
type ResolvedPricing struct {
	Error error `json:"-"`
	// Explicit administrator units also apply to public model aliases.
	PriceUnit                 string   `json:"price_unit,omitempty"`
	InputAudioPricePerSecond  *float64 `json:"input_audio_price_per_second,omitempty"`
	OutputAudioPricePerSecond *float64 `json:"output_audio_price_per_second,omitempty"`
	OutputVideoPricePerSecond *float64 `json:"output_video_price_per_second,omitempty"`
	// Mode 计费模式
	Mode BillingMode

	// Token 模式：基础定价（管理员覆盖、LiteLLM 或 fallback）
	BasePricing *ModelPricing

	// Token 模式：区间定价列表（如有，覆盖 BasePricing 中的对应字段）
	Intervals []PricingInterval

	// 按次/图片模式：分层定价
	RequestTiers []PricingInterval

	// 按次/图片模式：默认价格（未命中层级时使用）
	DefaultPerRequestPrice float64
	// DefaultPriceConfigured distinguishes an explicit free price from no price.
	DefaultPriceConfigured bool

	// 来源标识
	Source string // "channel", "manual", "litellm", "fallback"

	// 是否支持缓存细分
	SupportsCacheBreakdown bool
}

// ModelPricingResolver 统一模型定价解析器。
// 解析链：Channel → Administrator → LiteLLM → Fallback。
type ModelPricingResolver struct {
	channelService *ChannelService
	billingService *BillingService
	modelPolicies  *AdminModelPolicyService
}

func (r *ModelPricingResolver) SetAdminModelPolicies(policies *AdminModelPolicyService) {
	r.modelPolicies = policies
}

// NewModelPricingResolver 创建定价解析器实例
func NewModelPricingResolver(channelService *ChannelService, billingService *BillingService) *ModelPricingResolver {
	return &ModelPricingResolver{
		channelService: channelService,
		billingService: billingService,
	}
}

// PricingInput 定价解析输入
type PricingInput struct {
	Model   string
	GroupID *int64 // nil 表示不检查渠道
}

// Resolve 解析模型定价。
// 1. 获取基础定价（LiteLLM → Fallback）
// 2. 应用管理员基础价
// 3. 如果指定了 GroupID，查找渠道定价并覆盖
func (r *ModelPricingResolver) Resolve(ctx context.Context, input PricingInput) *ResolvedPricing {
	// 1. 获取基础定价
	basePricing, source := r.resolveBasePricing(input.Model)

	resolved := &ResolvedPricing{
		Mode:                   BillingModeToken,
		BasePricing:            basePricing,
		Source:                 source,
		SupportsCacheBreakdown: basePricing != nil && basePricing.SupportsCacheBreakdown,
	}
	if r.modelPolicies != nil {
		price, err := r.modelPolicies.GetPricing(ctx, input.Model)
		if err != nil {
			resolved.Error = err
			return resolved
		}
		if price != nil {
			resolved.Source, resolved.Mode = PricingSourceManual, price.BillingMode
			resolved.PriceUnit = price.PriceUnit
			if price.BillingMode == BillingModeDuration {
				r.applyDurationOverrides(price, resolved)
			} else if price.BillingMode == BillingModeToken {
				r.applyManualTokenOverrides(price, resolved)
			} else {
				r.applyRequestTierOverrides(price, resolved)
			}
		}
	}

	// 2. 如果有 GroupID，尝试渠道覆盖
	if input.GroupID != nil {
		r.applyChannelOverrides(ctx, *input.GroupID, input.Model, resolved)
	}

	return resolved
}

func (r *ModelPricingResolver) applyDurationOverrides(price *ChannelModelPricing, resolved *ResolvedPricing) {
	resolved.PriceUnit = "second"
	resolved.DefaultPerRequestPrice, resolved.DefaultPriceConfigured = 0, false
	resolved.RequestTiers, resolved.Intervals = nil, nil
	resolved.InputAudioPricePerSecond = copyDurationPrice(price.InputAudioPricePerSecond)
	resolved.OutputAudioPricePerSecond = copyDurationPrice(price.OutputAudioPricePerSecond)
	resolved.OutputVideoPricePerSecond = copyDurationPrice(price.OutputVideoPricePerSecond)
}

func copyDurationPrice(price *float64) *float64 {
	if price == nil {
		return nil
	}
	value := *price
	return &value
}

// Administrator token prices are flat rates, independent of automatic model
// tiers. Keep optional cache defaults and dormant catalogue metadata so a
// higher-priority channel rule can retain its existing tier semantics.
func (r *ModelPricingResolver) applyManualTokenOverrides(price *ChannelModelPricing, resolved *ResolvedPricing) {
	r.applyTokenOverrides(price, resolved)
	base := resolved.BasePricing
	base.FlatTokenPrices = true
	if base.HasImageOutputPricing || base.ImageOutputPricePerToken != 0 {
		base.ImageOutputPricePerToken = base.OutputPricePerToken
		base.HasImageOutputPricing = true
	}
}

// resolveBasePricing 从 LiteLLM 或 Fallback 获取基础定价
func (r *ModelPricingResolver) resolveBasePricing(model string) (*ModelPricing, string) {
	pricing, err := r.billingService.GetModelPricing(model)
	if err != nil {
		slog.Debug("failed to get model pricing from LiteLLM, using fallback",
			"model", model, "error", err)
		return nil, PricingSourceFallback
	}
	return pricing, PricingSourceLiteLLM
}

// applyChannelOverrides 应用渠道定价覆盖
func (r *ModelPricingResolver) applyChannelOverrides(ctx context.Context, groupID int64, model string, resolved *ResolvedPricing) {
	chPricing := r.channelService.GetChannelModelPricing(ctx, groupID, model)
	if chPricing == nil {
		return
	}

	resolved.Source = PricingSourceChannel
	resolved.Mode = chPricing.BillingMode
	resolved.PriceUnit = chPricing.PriceUnit
	resolved.InputAudioPricePerSecond, resolved.OutputAudioPricePerSecond, resolved.OutputVideoPricePerSecond = nil, nil, nil
	if resolved.Mode == "" {
		resolved.Mode = BillingModeToken
	}

	switch resolved.Mode {
	case BillingModeDuration:
		r.applyDurationOverrides(chPricing, resolved)
	case BillingModeToken:
		// Channel rules keep their existing service-tier and long-context
		// behavior. Do not let a lower-priority administrator flat-price flag
		// change that behavior, including when intervals return early below.
		if resolved.BasePricing != nil {
			resolved.BasePricing = cloneModelPricing(resolved.BasePricing)
			resolved.BasePricing.FlatTokenPrices = false
		}
		r.applyTokenOverrides(chPricing, resolved)
	case BillingModePerRequest, BillingModeImage, BillingModeVideo, BillingModeAudio:
		// A channel unit-price rule is authoritative even if it is incomplete;
		// do not accidentally publish an administrator fallback as its price.
		resolved.DefaultPerRequestPrice, resolved.DefaultPriceConfigured = 0, false
		r.applyRequestTierOverrides(chPricing, resolved)
	}
}

// applyTokenOverrides 应用 token 模式的渠道覆盖
func (r *ModelPricingResolver) applyTokenOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	// 过滤掉所有价格字段都为空的无效 interval
	validIntervals := filterValidIntervals(chPricing.Intervals)

	// 如果有有效的区间定价，使用区间
	if len(validIntervals) > 0 {
		resolved.Intervals = validIntervals
		return
	}

	// 否则用 flat 字段覆盖 BasePricing
	if resolved.BasePricing != nil {
		// BasePricing may point at a shared fallback entry. Channel overrides are
		// request-scoped, so always apply them to a private copy.
		resolved.BasePricing = cloneModelPricing(resolved.BasePricing)
	} else {
		resolved.BasePricing = &ModelPricing{}
	}

	if chPricing.InputPrice != nil {
		resolved.BasePricing.InputPricePerToken = *chPricing.InputPrice
		resolved.BasePricing.InputPricePerTokenPriority = *chPricing.InputPrice
		if resolved.BasePricing.HasImageInputPricing || resolved.BasePricing.ImageInputPricePerToken != 0 {
			resolved.BasePricing.ImageInputPricePerToken = *chPricing.InputPrice
			resolved.BasePricing.HasImageInputPricing = true
		}
	}
	if chPricing.OutputPrice != nil {
		resolved.BasePricing.OutputPricePerToken = *chPricing.OutputPrice
		resolved.BasePricing.OutputPricePerTokenPriority = *chPricing.OutputPrice
	}
	if chPricing.CacheWritePrice != nil {
		resolved.BasePricing.CacheCreationPricePerToken = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreationPricePerTokenPriority = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreation5mPrice = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreation1hPrice = *chPricing.CacheWritePrice
	}
	if chPricing.CacheReadPrice != nil {
		resolved.BasePricing.CacheReadPricePerToken = *chPricing.CacheReadPrice
		resolved.BasePricing.CacheReadPricePerTokenPriority = *chPricing.CacheReadPrice
		if resolved.BasePricing.HasCacheReadImagePricing || resolved.BasePricing.CacheReadImagePricePerToken != 0 {
			resolved.BasePricing.CacheReadImagePricePerToken = *chPricing.CacheReadPrice
			resolved.BasePricing.HasCacheReadImagePricing = true
		}
	}
	if chPricing.ImageOutputPrice != nil {
		resolved.BasePricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
		resolved.BasePricing.HasImageOutputPricing = true
	}
}

// applyRequestTierOverrides 应用按次/图片模式的渠道覆盖
func (r *ModelPricingResolver) applyRequestTierOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	resolved.RequestTiers = filterValidIntervals(chPricing.Intervals)
	if chPricing.PerRequestPrice != nil {
		resolved.DefaultPerRequestPrice = *chPricing.PerRequestPrice
		resolved.DefaultPriceConfigured = true
	}
}

// filterValidIntervals 过滤掉所有价格字段都为空的无效 interval。
// 前端可能创建了只有 min/max 但无价格的空 interval。
func filterValidIntervals(intervals []PricingInterval) []PricingInterval {
	var valid []PricingInterval
	for _, iv := range intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil ||
			iv.CacheWritePrice != nil || iv.CacheReadPrice != nil ||
			iv.PerRequestPrice != nil {
			valid = append(valid, iv)
		}
	}
	return valid
}

// GetIntervalPricing 根据 context token 数获取区间定价。
// 如果有区间列表，找到匹配区间并构造 ModelPricing；否则直接返回 BasePricing。
func (r *ModelPricingResolver) GetIntervalPricing(resolved *ResolvedPricing, totalContextTokens int) *ModelPricing {
	if len(resolved.Intervals) == 0 {
		return resolved.BasePricing
	}

	iv := FindMatchingInterval(resolved.Intervals, totalContextTokens)
	if iv == nil {
		return resolved.BasePricing
	}

	return intervalToModelPricing(iv, resolved.BasePricing, resolved.SupportsCacheBreakdown)
}

// intervalToModelPricing 将区间定价转换为 ModelPricing
func intervalToModelPricing(iv *PricingInterval, basePricing *ModelPricing, supportsCacheBreakdown bool) *ModelPricing {
	pricing := cloneModelPricing(basePricing)
	if pricing == nil {
		pricing = &ModelPricing{}
	}
	pricing.SupportsCacheBreakdown = supportsCacheBreakdown
	if iv.InputPrice != nil {
		pricing.InputPricePerToken = *iv.InputPrice
		pricing.InputPricePerTokenPriority = *iv.InputPrice
		if pricing.HasImageInputPricing || pricing.ImageInputPricePerToken != 0 {
			pricing.ImageInputPricePerToken = *iv.InputPrice
			pricing.HasImageInputPricing = true
		}
	}
	if iv.OutputPrice != nil {
		pricing.OutputPricePerToken = *iv.OutputPrice
		pricing.OutputPricePerTokenPriority = *iv.OutputPrice
	}
	if iv.CacheWritePrice != nil {
		pricing.CacheCreationPricePerToken = *iv.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = *iv.CacheWritePrice
		pricing.CacheCreation5mPrice = *iv.CacheWritePrice
		pricing.CacheCreation1hPrice = *iv.CacheWritePrice
	}
	if iv.CacheReadPrice != nil {
		pricing.CacheReadPricePerToken = *iv.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = *iv.CacheReadPrice
		if pricing.HasCacheReadImagePricing || pricing.CacheReadImagePricePerToken != 0 {
			pricing.CacheReadImagePricePerToken = *iv.CacheReadPrice
			pricing.HasCacheReadImagePricing = true
		}
	}
	return pricing
}

// GetRequestTierPrice 根据层级标签获取按次价格
func (r *ModelPricingResolver) GetRequestTierPrice(resolved *ResolvedPricing, tierLabel string) float64 {
	for _, tier := range resolved.RequestTiers {
		if tier.TierLabel == tierLabel && tier.PerRequestPrice != nil {
			return *tier.PerRequestPrice
		}
	}
	return 0
}

// GetRequestTierPriceByContext 根据 context token 数获取按次价格
func (r *ModelPricingResolver) GetRequestTierPriceByContext(resolved *ResolvedPricing, totalContextTokens int) float64 {
	iv := FindMatchingInterval(resolved.RequestTiers, totalContextTokens)
	if iv != nil && iv.PerRequestPrice != nil {
		return *iv.PerRequestPrice
	}
	return 0
}
