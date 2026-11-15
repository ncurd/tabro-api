package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const adminModelPolicyPrefix = "gateway_model_policy_v1:"

var ErrAdminModelPolicyInvalid = infraerrors.BadRequest("MODEL_POLICY_INVALID", "model policy is invalid")

// AdminModelPrice uses display units at the HTTP boundary. Runtime conversion
// produces the same per-token and per-unit values as channel pricing.
type AdminModelPrice struct {
	BillingMode               BillingMode `json:"billing_mode"`
	PriceUnit                 string      `json:"price_unit"`
	InputPricePerMillion      *float64    `json:"input_price_per_million,omitempty"`
	OutputPricePerMillion     *float64    `json:"output_price_per_million,omitempty"`
	CacheWritePricePerMillion *float64    `json:"cache_write_price_per_million,omitempty"`
	CacheReadPricePerMillion  *float64    `json:"cache_read_price_per_million,omitempty"`
	UnitPrice                 *float64    `json:"unit_price,omitempty"`
	InputAudioPricePerSecond  *float64    `json:"input_audio_price_per_second,omitempty"`
	OutputAudioPricePerSecond *float64    `json:"output_audio_price_per_second,omitempty"`
	OutputVideoPricePerSecond *float64    `json:"output_video_price_per_second,omitempty"`
}

type AdminModelPolicy struct {
	Model           string           `json:"model"`
	Platforms       []string         `json:"platforms"`
	HasProvider     bool             `json:"has_provider"`
	Enabled         bool             `json:"enabled"`
	Pricing         *AdminModelPrice `json:"pricing"`
	DefaultPricing  *AdminModelPrice `json:"default_pricing"`
	Source          string           `json:"source"`
	UpdatedAt       *time.Time       `json:"updated_at,omitempty"`
	ChannelOverride bool             `json:"channel_override"`
}

type AdminModelPolicyUpdate struct {
	Model   string           `json:"model"`
	Enabled *bool            `json:"enabled"`
	Pricing *AdminModelPrice `json:"pricing"`
}

type AdminModelPolicyList struct {
	Items    []AdminModelPolicy `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

type storedAdminModelPolicy struct {
	Model     string           `json:"model"`
	Enabled   bool             `json:"enabled"`
	Pricing   *AdminModelPrice `json:"pricing"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// One setting per exact public ID avoids whole-catalogue read/modify/write
// races. Reads are uncached so another gateway replica observes changes too.
type AdminModelPolicyService struct {
	settings SettingRepository
	pricing  *PricingService
	billing  *BillingService
	accounts AccountRepository
	channels ChannelRepository
}

func NewAdminModelPolicyService(settings SettingRepository, pricing *PricingService, billing *BillingService, accounts AccountRepository, channels ChannelRepository) *AdminModelPolicyService {
	return &AdminModelPolicyService{settings: settings, pricing: pricing, billing: billing, accounts: accounts, channels: channels}
}

func normalizeAdminModelID(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 300 || strings.ContainsAny(model, "*?") || strings.IndexFunc(model, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", fmt.Errorf("%w: select one exact model ID", ErrAdminModelPolicyInvalid)
	}
	return strings.ToLower(model), nil
}

func adminModelPolicyKey(model string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(model))))
	return adminModelPolicyPrefix + hex.EncodeToString(sum[:])
}

func decodeAdminModelPolicy(raw, model string) (*storedAdminModelPolicy, error) {
	var policy storedAdminModelPolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil, fmt.Errorf("invalid stored model policy: %w", err)
	}
	id, err := normalizeAdminModelID(policy.Model)
	if err != nil || id != strings.ToLower(strings.TrimSpace(model)) {
		return nil, errors.New("stored model policy identity mismatch")
	}
	if err = validateAdminModelPrice(policy.Model, policy.Pricing); err != nil {
		return nil, fmt.Errorf("invalid stored model price: %w", err)
	}
	return &policy, nil
}

func (s *AdminModelPolicyService) read(ctx context.Context, model string) (*storedAdminModelPolicy, error) {
	if s == nil || s.settings == nil {
		return nil, errors.New("model policy storage unavailable")
	}
	raw, err := s.settings.GetValue(ctx, adminModelPolicyKey(model))
	if errors.Is(err, ErrSettingNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeAdminModelPolicy(raw, model)
}

func (s *AdminModelPolicyService) IsModelEnabled(ctx context.Context, model string) (bool, error) {
	policy, err := s.read(ctx, model)
	if err != nil {
		return false, err
	}
	return policy == nil || policy.Enabled, nil
}

func (s *AdminModelPolicyService) FilterEnabledModels(ctx context.Context, models []string) ([]string, error) {
	if s == nil || s.settings == nil {
		return nil, errors.New("model policy storage unavailable")
	}
	keys := make([]string, 0, len(models))
	for _, model := range models {
		keys = append(keys, adminModelPolicyKey(model))
	}
	values, err := s.settings.GetMultiple(ctx, keys)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(models))
	for _, model := range models {
		if raw, exists := values[adminModelPolicyKey(model)]; exists {
			policy, decodeErr := decodeAdminModelPolicy(raw, model)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if !policy.Enabled {
				continue
			}
		}
		result = append(result, model)
	}
	return result, nil
}

func validateAdminModelPrice(model string, p *AdminModelPrice) error {
	if p == nil {
		return nil
	}
	for _, v := range []*float64{p.InputPricePerMillion, p.OutputPricePerMillion, p.CacheWritePricePerMillion, p.CacheReadPricePerMillion, p.UnitPrice, p.InputAudioPricePerSecond, p.OutputAudioPricePerSecond, p.OutputVideoPricePerSecond} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return fmt.Errorf("%w: prices must be finite and nonnegative", ErrAdminModelPolicyInvalid)
		}
	}
	hasDurationPrices := p.InputAudioPricePerSecond != nil || p.OutputAudioPricePerSecond != nil || p.OutputVideoPricePerSecond != nil
	if p.BillingMode == BillingModeDuration {
		if p.PriceUnit != "second" || !hasDurationPrices || p.UnitPrice != nil || p.InputPricePerMillion != nil || p.OutputPricePerMillion != nil || p.CacheWritePricePerMillion != nil || p.CacheReadPricePerMillion != nil {
			return fmt.Errorf("%w: duration pricing requires at least one per-second direction price and no token or unit prices", ErrAdminModelPolicyInvalid)
		}
		modelID := strings.ToLower(strings.TrimSpace(model))
		knownASR := IsNativeRealtimeASRModel(modelID) || modelID == "qwen3-asr-flash" || modelID == "qwen3-asr-flash-2026-02-10" || modelID == "qwen3-asr-flash-2025-09-08"
		knownTTS := IsQwenRealtimeTTSModel(modelID) || modelID == "azure-tts" || modelID == QwenVoiceCloneModel
		if knownASR && p.InputAudioPricePerSecond == nil {
			return fmt.Errorf("%w: ASR duration pricing requires an input audio price", ErrAdminModelPolicyInvalid)
		}
		if knownTTS && p.OutputAudioPricePerSecond == nil {
			return fmt.Errorf("%w: TTS duration pricing requires an output audio price", ErrAdminModelPolicyInvalid)
		}
		return nil
	}
	if hasDurationPrices {
		return fmt.Errorf("%w: per-second direction prices require duration billing mode", ErrAdminModelPolicyInvalid)
	}
	if IsNativeRealtimeASRModel(strings.ToLower(model)) && p.BillingMode != BillingModePerRequest && (p.BillingMode != BillingModeAudio || p.PriceUnit != "second") {
		return fmt.Errorf("%w: ASR requires per-second audio pricing or per-request pricing", ErrAdminModelPolicyInvalid)
	}
	if IsQwenRealtimeTTSModel(strings.ToLower(model)) && p.BillingMode != BillingModePerRequest && (p.BillingMode != BillingModeAudio || p.PriceUnit != "character") {
		return fmt.Errorf("%w: TTS requires per-character audio pricing or per-request pricing", ErrAdminModelPolicyInvalid)
	}
	if p.BillingMode == BillingModeToken {
		if p.PriceUnit != "million_tokens" || p.InputPricePerMillion == nil || p.OutputPricePerMillion == nil || p.UnitPrice != nil {
			return fmt.Errorf("%w: token pricing requires input and output prices per million tokens", ErrAdminModelPolicyInvalid)
		}
		return nil
	}
	if p.InputPricePerMillion != nil || p.OutputPricePerMillion != nil || p.CacheWritePricePerMillion != nil || p.CacheReadPricePerMillion != nil || p.UnitPrice == nil {
		return fmt.Errorf("%w: unit pricing requires an explicit unit price", ErrAdminModelPolicyInvalid)
	}
	valid := p.BillingMode == BillingModePerRequest && p.PriceUnit == "request" || p.BillingMode == BillingModeImage && p.PriceUnit == "image" || p.BillingMode == BillingModeVideo && p.PriceUnit == "second" || p.BillingMode == BillingModeAudio && (p.PriceUnit == "second" || p.PriceUnit == "character")
	if !valid {
		return fmt.Errorf("%w: billing mode and price unit do not match", ErrAdminModelPolicyInvalid)
	}
	if IsNativeRealtimeASRModel(strings.ToLower(model)) && p.BillingMode == BillingModeAudio && p.PriceUnit != "second" {
		return fmt.Errorf("%w: ASR must be priced per audio second", ErrAdminModelPolicyInvalid)
	}
	if IsQwenRealtimeTTSModel(strings.ToLower(model)) && p.BillingMode == BillingModeAudio && p.PriceUnit != "character" {
		return fmt.Errorf("%w: TTS must be priced per billing character", ErrAdminModelPolicyInvalid)
	}
	return nil
}

func (s *AdminModelPolicyService) GetPricing(ctx context.Context, model string) (*ChannelModelPricing, error) {
	policy, err := s.read(ctx, model)
	if err != nil {
		return nil, err
	}
	if policy == nil || policy.Pricing == nil {
		return nil, nil
	}
	p := policy.Pricing
	result := &ChannelModelPricing{Models: []string{model}, BillingMode: p.BillingMode, PriceUnit: p.PriceUnit, PerRequestPrice: p.UnitPrice,
		InputAudioPricePerSecond: p.InputAudioPricePerSecond, OutputAudioPricePerSecond: p.OutputAudioPricePerSecond, OutputVideoPricePerSecond: p.OutputVideoPricePerSecond}
	convert := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		v := *value / pricePerMillionMultiplier
		return &v
	}
	result.InputPrice = convert(p.InputPricePerMillion)
	result.OutputPrice = convert(p.OutputPricePerMillion)
	result.CacheWritePrice = convert(p.CacheWritePricePerMillion)
	result.CacheReadPrice = convert(p.CacheReadPricePerMillion)
	return result, nil
}

func (s *AdminModelPolicyService) Update(ctx context.Context, input AdminModelPolicyUpdate) (*AdminModelPolicy, error) {
	model, err := normalizeAdminModelID(input.Model)
	if err != nil {
		return nil, err
	}
	if input.Enabled == nil {
		return nil, fmt.Errorf("%w: enabled must be specified", ErrAdminModelPolicyInvalid)
	}
	if err = validateAdminModelPrice(model, input.Pricing); err != nil {
		return nil, err
	}
	policy := storedAdminModelPolicy{Model: strings.TrimSpace(input.Model), Enabled: *input.Enabled, Pricing: input.Pricing, UpdatedAt: time.Now().UTC()}
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	if s == nil || s.settings == nil {
		return nil, errors.New("model policy storage unavailable")
	}
	if err = s.settings.Set(ctx, adminModelPolicyKey(model), string(raw)); err != nil {
		return nil, err
	}
	return s.Detail(ctx, model)
}

func (s *AdminModelPolicyService) ResetPrice(ctx context.Context, model string) (*AdminModelPolicy, error) {
	if _, err := normalizeAdminModelID(model); err != nil {
		return nil, err
	}
	policy, err := s.read(ctx, model)
	if err != nil {
		return nil, err
	}
	enabled := policy == nil || policy.Enabled
	return s.Update(ctx, AdminModelPolicyUpdate{Model: model, Enabled: &enabled, Pricing: nil})
}

func (s *AdminModelPolicyService) inventory(ctx context.Context) (map[string]map[string]bool, map[string]bool, map[string]bool, error) {
	models := map[string]map[string]bool{}
	overrides := map[string]bool{}
	providers := map[string]bool{}
	wildcardOverrides := []string{}
	add := func(model, platform string) {
		id, err := normalizeAdminModelID(model)
		if err != nil {
			return
		}
		if models[id] == nil {
			models[id] = map[string]bool{}
		}
		if platform != "" {
			models[id][platform] = true
		}
	}
	if s.pricing != nil {
		s.pricing.mu.RLock()
		for model, p := range s.pricing.pricingData {
			platform := ""
			if p != nil {
				platform = p.LiteLLMProvider
			}
			add(model, platform)
		}
		s.pricing.mu.RUnlock()
	}
	if s.billing != nil {
		for model := range s.billing.fallbackPrices {
			add(model, "")
		}
	}
	if s.accounts != nil {
		for page := 1; ; page++ {
			accounts, result, err := s.accounts.List(ctx, pagination.PaginationParams{Page: page, PageSize: 1000})
			if err != nil {
				return nil, nil, nil, err
			}
			for i := range accounts {
				for _, id := range gatewayAccountModelIDs(&accounts[i]) {
					add(id, accounts[i].Platform)
					// Provider presence comes from configured accounts, never from
					// a provider name in the pricing catalogue or a channel price.
					providers[strings.ToLower(strings.TrimSpace(id))] = true
				}
			}
			if len(accounts) < 1000 || result != nil && page*1000 >= int(result.Total) {
				break
			}
		}
	}
	if s.channels != nil {
		channels, err := s.channels.ListAll(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, ch := range channels {
			for _, price := range ch.ModelPricing {
				for _, id := range price.Models {
					if strings.HasSuffix(id, "*") {
						wildcardOverrides = append(wildcardOverrides, strings.ToLower(strings.TrimSuffix(id, "*")))
					}
					add(id, price.Platform)
					if key, err := normalizeAdminModelID(id); err == nil {
						overrides[key] = true
					}
				}
			}
		}
	}
	for model := range models {
		for _, prefix := range wildcardOverrides {
			if strings.HasPrefix(model, prefix) {
				overrides[model] = true
				break
			}
		}
	}
	return models, overrides, providers, nil
}

func adminModelHasProvider(model string, providers map[string]bool) bool {
	if providers[model] {
		return true
	}
	// Wildcard mappings can provide concrete catalogue and manually added IDs
	// without introducing wildcard rows into model management.
	for pattern := range providers {
		if strings.HasSuffix(pattern, "*") && matchWildcard(pattern, model) {
			return true
		}
	}
	return false
}

func (s *AdminModelPolicyService) defaultPrice(model string) *AdminModelPrice {
	if s.billing == nil {
		return nil
	}
	// Do not interpret an audio/image metadata entry's zero token fields as a
	// free token price. Media prices require an explicit administrator rule.
	if s.pricing != nil {
		if entry := s.pricing.GetModelPricing(model); entry != nil && entry.Mode != "" && entry.Mode != "chat" && entry.Mode != "completion" && entry.Mode != "embedding" {
			return nil
		}
	}
	if IsNativeRealtimeASRModel(model) || IsQwenRealtimeTTSModel(model) {
		return nil
	}
	price, err := s.billing.GetModelPricing(model)
	if err != nil || price == nil {
		return nil
	}
	value := func(v float64) *float64 { v *= pricePerMillionMultiplier; return &v }
	return &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: value(price.InputPricePerToken), OutputPricePerMillion: value(price.OutputPricePerToken), CacheWritePricePerMillion: value(price.CacheCreationPricePerToken), CacheReadPricePerMillion: value(price.CacheReadPricePerToken)}
}

func (s *AdminModelPolicyService) row(model string, platforms map[string]bool, override, hasProvider bool, stored *storedAdminModelPolicy) AdminModelPolicy {
	row := AdminModelPolicy{Model: model, Enabled: true, Platforms: []string{}, HasProvider: hasProvider, DefaultPricing: s.defaultPrice(model), Source: "unpriced", ChannelOverride: override}
	for platform := range platforms {
		row.Platforms = append(row.Platforms, platform)
	}
	sort.Strings(row.Platforms)
	if row.DefaultPricing != nil {
		row.Source = "automatic"
	}
	if stored != nil {
		row.Model = stored.Model
		row.Enabled = stored.Enabled
		row.Pricing = stored.Pricing
		row.UpdatedAt = &stored.UpdatedAt
		if stored.Pricing != nil {
			row.Source = "manual"
		}
	}
	return row
}

func (s *AdminModelPolicyService) List(ctx context.Context, search string, enabled, hasProvider *bool, page, pageSize int) (*AdminModelPolicyList, error) {
	models, overrides, providers, err := s.inventory(ctx)
	if err != nil {
		return nil, err
	}
	values, err := s.settings.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	stored := map[string]*storedAdminModelPolicy{}
	for key, raw := range values {
		if !strings.HasPrefix(key, adminModelPolicyPrefix) {
			continue
		}
		var identity struct {
			Model string `json:"model"`
		}
		if json.Unmarshal([]byte(raw), &identity) != nil {
			return nil, errors.New("invalid stored model policy")
		}
		id, err := normalizeAdminModelID(identity.Model)
		if err != nil || key != adminModelPolicyKey(id) {
			return nil, errors.New("invalid stored model policy identity")
		}
		policy, err := decodeAdminModelPolicy(raw, id)
		if err != nil {
			return nil, err
		}
		stored[id] = policy
		if models[id] == nil {
			models[id] = map[string]bool{}
		}
	}
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	matchingIDs := []string{}
	search = strings.ToLower(strings.TrimSpace(search))
	for _, id := range ids {
		matches := search == "" || strings.Contains(id, search)
		for platform := range models[id] {
			matches = matches || strings.Contains(platform, search)
		}
		if !matches {
			continue
		}
		active := stored[id] == nil || stored[id].Enabled
		if enabled != nil && active != *enabled {
			continue
		}
		if hasProvider != nil && adminModelHasProvider(id, providers) != *hasProvider {
			continue
		}
		matchingIDs = append(matchingIDs, id)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	result := &AdminModelPolicyList{Items: []AdminModelPolicy{}, Total: len(matchingIDs), Page: page, PageSize: pageSize}
	if page-1 > len(matchingIDs)/pageSize {
		return result, nil
	}
	start := (page - 1) * pageSize
	if start < len(matchingIDs) {
		end := start + pageSize
		if end > len(matchingIDs) {
			end = len(matchingIDs)
		}
		for _, id := range matchingIDs[start:end] {
			result.Items = append(result.Items, s.row(id, models[id], overrides[id], adminModelHasProvider(id, providers), stored[id]))
		}
	}
	return result, nil
}

func (s *AdminModelPolicyService) Detail(ctx context.Context, model string) (*AdminModelPolicy, error) {
	id, err := normalizeAdminModelID(model)
	if err != nil {
		return nil, err
	}
	stored, err := s.read(ctx, id)
	if err != nil {
		return nil, err
	}
	models, overrides, providers, err := s.inventory(ctx)
	if err != nil {
		return nil, err
	}
	row := s.row(id, models[id], overrides[id], adminModelHasProvider(id, providers), stored)
	return &row, nil
}
