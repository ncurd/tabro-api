package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

var ErrMediaPricingNotConfigured = errors.New("media pricing is not configured")
var ErrMediaBillingUnavailable = errors.New("media billing is unavailable")
var ErrMediaUsageIncomplete = errors.New("upstream media usage is incomplete")

type mediaPricingSource interface {
	GetChannelModelPricing(context.Context, int64, string) *ChannelModelPricing
	GetChannelForGroup(context.Context, int64) (*Channel, error)
	ResolveChannelMapping(context.Context, int64, string) ChannelMappingResult
}

type MediaBillingService struct {
	pricing mediaPricingSource
	ledger  UsageBillingRepository
	usage   UsageLogRepository
	cache   *BillingCacheService
	rates   UserGroupRateRepository
	apiKeys *APIKeyService
	cfg     *config.Config
}

func NewMediaBillingService(pricing *ChannelService, ledger UsageBillingRepository, usage UsageLogRepository, cache *BillingCacheService, rates UserGroupRateRepository, apiKeys *APIKeyService, cfg *config.Config) *MediaBillingService {
	return &MediaBillingService{pricing: pricing, ledger: ledger, usage: usage, cache: cache, rates: rates, apiKeys: apiKeys, cfg: cfg}
}

type MediaPriceSnapshot struct {
	Mode    BillingMode        `json:"mode"`
	Default *float64           `json:"default,omitempty"`
	Tiers   map[string]float64 `json:"tiers,omitempty"`
}

// Only prices and verified billing identity are persisted; never credentials or input media.
type MediaBillingSnapshot struct {
	Version               int                 `json:"version"`
	Kind                  string              `json:"kind"`
	UserID                int64               `json:"user_id"`
	APIKeyID              int64               `json:"api_key_id"`
	AccountID             int64               `json:"account_id"`
	AccountType           string              `json:"account_type"`
	GroupID               *int64              `json:"group_id,omitempty"`
	SubscriptionID        *int64              `json:"subscription_id,omitempty"`
	ChannelID             int64               `json:"channel_id,omitempty"`
	Model                 string              `json:"model"`
	RequestedModel        string              `json:"requested_model"`
	UpstreamModel         string              `json:"upstream_model"`
	Price                 MediaPriceSnapshot  `json:"price"`
	AccountPrice          *MediaPriceSnapshot `json:"account_price,omitempty"`
	RateMultiplier        float64             `json:"rate_multiplier"`
	AccountRateMultiplier float64             `json:"account_rate_multiplier"`
	ChargeAPIKeyQuota     bool                `json:"charge_api_key_quota"`
	ChargeRateLimits      bool                `json:"charge_rate_limits"`
	ChargeAccountQuota    bool                `json:"charge_account_quota"`
	PayloadHash           string              `json:"payload_hash"`
	OIDCIssuer            *string             `json:"oidc_issuer,omitempty"`
	OIDCSubject           *string             `json:"oidc_subject,omitempty"`
	OIDCTenant            *string             `json:"oidc_tenant,omitempty"`
	TabroRunID            *string             `json:"tabro_run_id,omitempty"`
	TabroProjectID        *string             `json:"tabro_project_id,omitempty"`
}

func (s *MediaBillingSnapshot) JSON() []byte { b, _ := json.Marshal(s); return b }

func (s *MediaBillingService) Prepare(ctx context.Context, meta MediaRequestMeta, account *Account, model, upstreamModel, kind, tier string) (*MediaBillingSnapshot, error) {
	if s == nil || s.ledger == nil || s.pricing == nil || s.usage == nil {
		return nil, ErrMediaBillingUnavailable
	}
	key := meta.APIKey
	if key == nil || account == nil || key.UserID != meta.UserID || key.ID != meta.APIKeyID || key.GroupID == nil || key.Group == nil || *key.GroupID != key.Group.ID {
		return nil, fmt.Errorf("%w: authenticated billing context is required", ErrMediaBillingUnavailable)
	}
	mapping := s.pricing.ResolveChannelMapping(ctx, *key.GroupID, model)
	billingModel := model
	switch mapping.BillingModelSource {
	case BillingModelSourceUpstream:
		billingModel = upstreamModel
	case BillingModelSourceChannelMapped:
		billingModel = firstNonEmpty(mapping.MappedModel, model)
	}
	price, err := snapshotMediaPrice(s.pricing.GetChannelModelPricing(ctx, *key.GroupID, billingModel), kind)
	if err != nil {
		return nil, fmt.Errorf("%w for model %s: %v", ErrMediaPricingNotConfigured, billingModel, err)
	}
	if _, err = price.UnitPrice(tier); err != nil {
		return nil, fmt.Errorf("%w for model %s resolution %s", ErrMediaPricingNotConfigured, billingModel, tier)
	}
	multiplier := key.Group.RateMultiplier
	if s.rates != nil {
		rate, rateErr := s.rates.GetByUserAndGroup(ctx, meta.UserID, *key.GroupID)
		if rateErr != nil {
			return nil, fmt.Errorf("%w: resolve user rate: %v", ErrMediaBillingUnavailable, rateErr)
		}
		if rate != nil {
			multiplier = *rate
		}
	}
	accountRate := account.BillingRateMultiplier()
	if !validMediaPrice(multiplier) || !validMediaPrice(accountRate) {
		return nil, fmt.Errorf("%w: invalid multiplier", ErrMediaPricingNotConfigured)
	}
	snap := &MediaBillingSnapshot{Version: 1, Kind: kind, UserID: meta.UserID, APIKeyID: meta.APIKeyID, AccountID: account.ID, AccountType: account.Type, GroupID: copyMediaInt64(key.GroupID), ChannelID: mapping.ChannelID, Model: billingModel, RequestedModel: model, UpstreamModel: upstreamModel, Price: *price, RateMultiplier: multiplier, AccountRateMultiplier: accountRate, ChargeAPIKeyQuota: key.Quota > 0, ChargeRateLimits: key.HasRateLimits(), ChargeAccountQuota: account.IsAPIKeyOrBedrock() && account.HasAnyQuotaLimit(), PayloadHash: HashUsageRequestPayload(meta.RequestJSON)}
	if key.Group.IsSubscriptionType() {
		if meta.Subscription == nil || meta.Subscription.UserID != meta.UserID || meta.Subscription.GroupID != *key.GroupID {
			return nil, fmt.Errorf("%w: subscription context is required", ErrMediaBillingUnavailable)
		}
		id := meta.Subscription.ID
		snap.SubscriptionID = &id
	}
	identity := &UsageLog{}
	identity.ApplyGatewayUsageContext(ctx)
	snap.OIDCIssuer, snap.OIDCSubject, snap.OIDCTenant = identity.OIDCIssuer, identity.OIDCSubject, identity.OIDCTenant
	snap.TabroRunID, snap.TabroProjectID = identity.TabroRunID, identity.TabroProjectID
	channel, chErr := s.pricing.GetChannelForGroup(ctx, *key.GroupID)
	if chErr != nil {
		return nil, fmt.Errorf("%w: load account pricing: %v", ErrMediaBillingUnavailable, chErr)
	}
	if channel != nil {
		for _, rule := range channel.AccountStatsPricingRules {
			if !matchAccountStatsRule(&rule, account.ID, *key.GroupID) {
				continue
			}
			accountPricing := findPricingForModel(rule.Pricing, account.Platform, strings.ToLower(upstreamModel))
			if accountPricing == nil {
				continue
			}
			snap.AccountPrice, err = snapshotMediaPrice(accountPricing, kind)
			if err != nil {
				return nil, fmt.Errorf("%w: account statistics price: %v", ErrMediaPricingNotConfigured, err)
			}
			if _, err = snap.AccountPrice.UnitPrice(tier); err != nil {
				return nil, fmt.Errorf("%w: account statistics resolution price", ErrMediaPricingNotConfigured)
			}
			break
		}
		if snap.AccountPrice == nil && channel.ApplyPricingToAccountStats {
			cp := snap.Price
			snap.AccountPrice = &cp
		}
	}
	return snap, nil
}

func snapshotMediaPrice(pricing *ChannelModelPricing, kind string) (*MediaPriceSnapshot, error) {
	if pricing == nil {
		return nil, ErrMediaPricingNotConfigured
	}
	validMode := pricing.BillingMode == BillingModePerRequest || (kind == MediaJobKindVideoGeneration && pricing.BillingMode == BillingModeVideo) || ((kind == MediaJobKindAudioSpeech || kind == "audio_transcription") && pricing.BillingMode == BillingModeAudio)
	if !validMode {
		return nil, fmt.Errorf("billing mode %s cannot price %s", pricing.BillingMode, kind)
	}
	p := &MediaPriceSnapshot{Mode: pricing.BillingMode, Tiers: map[string]float64{}}
	if pricing.PerRequestPrice != nil {
		if !validMediaPrice(*pricing.PerRequestPrice) {
			return nil, fmt.Errorf("price must be finite and nonnegative")
		}
		v := *pricing.PerRequestPrice
		p.Default = &v
	}
	for _, iv := range pricing.Intervals {
		if iv.PerRequestPrice == nil {
			continue
		}
		label := normalizeMediaBillingTier(iv.TierLabel)
		if label == "" || !validMediaPrice(*iv.PerRequestPrice) {
			return nil, fmt.Errorf("media tiers require a label and finite nonnegative price")
		}
		if _, exists := p.Tiers[label]; exists {
			return nil, fmt.Errorf("duplicate media pricing tier %s", label)
		}
		p.Tiers[label] = *iv.PerRequestPrice
	}
	if p.Default == nil && len(p.Tiers) == 0 {
		return nil, ErrMediaPricingNotConfigured
	}
	return p, nil
}

func (p MediaPriceSnapshot) UnitPrice(tier string) (float64, error) {
	if v, ok := p.Tiers[normalizeMediaBillingTier(tier)]; ok && validMediaPrice(v) {
		return v, nil
	}
	if p.Default != nil && validMediaPrice(*p.Default) {
		return *p.Default, nil
	}
	return 0, ErrMediaPricingNotConfigured
}
func validMediaPrice(v float64) bool            { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func normalizeMediaBillingTier(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }
func copyMediaInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func (s *MediaBillingService) Settle(ctx context.Context, job *MediaGenerationJob) error {
	if s == nil || s.ledger == nil || s.usage == nil {
		return ErrMediaBillingUnavailable
	}
	if job == nil || (job.Status != MediaJobStatusSucceeded && !(job.Kind == "voice_clone" && job.Status == MediaJobStatusCanceled && job.AudioVoice != "")) {
		return nil
	}
	var snap MediaBillingSnapshot
	if err := json.Unmarshal(job.BillingSnapshotJSON, &snap); err != nil || snap.Version != 1 {
		return fmt.Errorf("%w: invalid billing snapshot", ErrMediaBillingUnavailable)
	}
	if snap.UserID != job.UserID || snap.APIKeyID != job.APIKeyID || snap.AccountID != job.AccountID || snap.Kind != job.Kind {
		return fmt.Errorf("%w: billing identity mismatch", ErrMediaBillingUnavailable)
	}
	units, tier, err := mediaBillableUnits(job, snap.Kind)
	if err != nil && snap.Price.Mode == BillingModePerRequest && (snap.AccountPrice == nil || snap.AccountPrice.Mode == BillingModePerRequest) {
		units = 1
		err = nil
	}
	if err != nil {
		return err
	}
	price, err := snap.Price.UnitPrice(tier)
	if err != nil {
		return fmt.Errorf("%w: upstream result tier %s was not priced", ErrMediaUsageIncomplete, tier)
	}
	total := price
	if snap.Price.Mode != BillingModePerRequest {
		total *= units
	}
	actual := total * snap.RateMultiplier
	accountCost := total * snap.AccountRateMultiplier
	if snap.AccountPrice != nil {
		rate, err := snap.AccountPrice.UnitPrice(tier)
		if err != nil {
			return fmt.Errorf("%w: account result tier", ErrMediaUsageIncomplete)
		}
		accountCost = rate
		if snap.AccountPrice.Mode != BillingModePerRequest {
			accountCost *= units
		}
	}
	if !validMediaPrice(total) || !validMediaPrice(actual) || !validMediaPrice(accountCost) {
		return fmt.Errorf("%w: invalid calculated cost", ErrMediaUsageIncomplete)
	}
	mode := string(snap.Price.Mode)
	billingType := BillingTypeBalance
	if snap.SubscriptionID != nil {
		billingType = BillingTypeSubscription
	}
	durationMs := int(math.Ceil(units * 1000))
	if snap.Kind == MediaJobKindAudioSpeech || snap.Kind == "voice_clone" {
		durationMs = 0
	}
	log := &UsageLog{UserID: snap.UserID, APIKeyID: snap.APIKeyID, AccountID: snap.AccountID, RequestID: job.PublicID, GroupID: snap.GroupID, SubscriptionID: snap.SubscriptionID, Model: snap.Model, RequestedModel: snap.RequestedModel, UpstreamModel: optionalNonEqualStringPtr(snap.UpstreamModel, snap.RequestedModel), ChannelID: optionalInt64Ptr(snap.ChannelID), BillingMode: &mode, BillingTier: optionalTrimmedStringPtr(tier), BillingType: billingType, RequestType: RequestTypeSync, DurationMs: &durationMs, ImageCount: job.VideoCount, MediaType: optionalTrimmedStringPtr(tier), OutputCost: total, TotalCost: total, ActualCost: actual, RateMultiplier: snap.RateMultiplier, AccountRateMultiplier: &snap.AccountRateMultiplier, AccountStatsCost: &accountCost, OIDCIssuer: snap.OIDCIssuer, OIDCSubject: snap.OIDCSubject, OIDCTenant: snap.OIDCTenant, TabroRunID: snap.TabroRunID, TabroProjectID: snap.TabroProjectID, CreatedAt: job.CreatedAt}
	cmd := &UsageBillingCommand{RequestID: job.PublicID, UpstreamRequestID: job.UpstreamRequestID, RequestPayloadHash: snap.PayloadHash, UserID: snap.UserID, APIKeyID: snap.APIKeyID, AccountID: snap.AccountID, AccountType: snap.AccountType, SubscriptionID: snap.SubscriptionID, Model: snap.Model, RequestedModel: snap.RequestedModel, UpstreamModel: log.UpstreamModel, BillingType: billingType, BillingMode: mode, ImageCount: job.VideoCount, MediaType: tier, OutputCost: total, TotalCost: total, ActualCost: actual, RateMultiplier: snap.RateMultiplier, AccountRateMultiplier: &snap.AccountRateMultiplier, OIDCIssuer: snap.OIDCIssuer, OIDCSubject: snap.OIDCSubject, OIDCTenant: snap.OIDCTenant, TabroRunID: snap.TabroRunID, TabroProjectID: snap.TabroProjectID}
	if snap.SubscriptionID != nil {
		cmd.SubscriptionCost = total
	} else {
		cmd.BalanceCost = actual
	}
	if snap.ChargeAPIKeyQuota {
		cmd.APIKeyQuotaCost = actual
	}
	if snap.ChargeRateLimits {
		cmd.APIKeyRateLimitCost = actual
	}
	if snap.ChargeAccountQuota {
		cmd.AccountQuotaCost = accountCost
	}
	billingCtx, cancel := detachedBillingContext(ctx)
	defer cancel()
	if _, err = s.ledger.Apply(billingCtx, cmd); err != nil {
		return fmt.Errorf("%w: %v", ErrMediaBillingUnavailable, err)
	}
	// Invalidation is retry-safe even if a prior transaction committed before its caller disconnected.
	if s.cache != nil {
		if snap.SubscriptionID != nil && snap.GroupID != nil {
			_ = s.cache.InvalidateSubscription(billingCtx, snap.UserID, *snap.GroupID)
		} else {
			_ = s.cache.InvalidateUserBalance(billingCtx, snap.UserID)
		}
		if s.cache.cache != nil && snap.ChargeRateLimits {
			_ = s.cache.cache.InvalidateAPIKeyRateLimit(billingCtx, snap.APIKeyID)
		}
	}
	if s.apiKeys != nil {
		s.apiKeys.InvalidateAuthCacheByUserID(billingCtx, snap.UserID)
	}
	if _, err = s.usage.Create(billingCtx, log); err != nil {
		return fmt.Errorf("%w: usage log: %v", ErrMediaBillingUnavailable, err)
	}
	return nil
}

func mediaBillableUnits(job *MediaGenerationJob, kind string) (float64, string, error) {
	body := job.UpstreamResponseJSON
	switch kind {
	case MediaJobKindVideoGeneration:
		var duration gjson.Result
		tier := ""
		if job.Provider == MediaProviderDashScope {
			duration = gjson.GetBytes(body, "usage.output_video_duration")
			if !duration.Exists() {
				duration = gjson.GetBytes(body, "usage.duration")
			}
			tier = gjson.GetBytes(body, "usage.resolution").String()
			if tier == "" {
				if sr := gjson.GetBytes(body, "usage.SR").String(); sr != "" {
					tier = strings.TrimSuffix(strings.ToUpper(sr), "P") + "P"
				}
			}
		} else {
			duration = gjson.GetBytes(body, "duration")
			if !duration.Exists() {
				duration = gjson.GetBytes(body, "usage.duration")
			}
			tier = firstNonEmpty(gjson.GetBytes(body, "resolution").String(), gjson.GetBytes(body, "usage.resolution").String())
			if !duration.Exists() {
				frames := gjson.GetBytes(body, "frames").Float()
				fps := gjson.GetBytes(body, "framespersecond").Float()
				if frames > 0 && fps > 0 && validMediaPrice(frames) && validMediaPrice(fps) && validMediaPrice(frames/fps) {
					return frames / fps, normalizeMediaBillingTier(tier), nil
				}
			}
		}
		seconds := duration.Float()
		if !duration.Exists() || seconds <= 0 || !validMediaPrice(seconds) {
			return 0, normalizeMediaBillingTier(tier), fmt.Errorf("%w: generated video duration is missing", ErrMediaUsageIncomplete)
		}
		return seconds, normalizeMediaBillingTier(tier), nil
	case MediaJobKindAudioSpeech:
		n := gjson.GetBytes(body, "usage.characters").Float()
		if n <= 0 {
			n = float64(job.AudioCharacterCount)
		}
		if n <= 0 || !validMediaPrice(n) {
			return 0, "", fmt.Errorf("%w: speech character count", ErrMediaUsageIncomplete)
		}
		return n, "", nil
	case "audio_transcription":
		n := gjson.GetBytes(body, "usage.seconds").Float()
		if n <= 0 || !validMediaPrice(n) {
			return 0, "", fmt.Errorf("%w: transcription duration", ErrMediaUsageIncomplete)
		}
		return n, "", nil
	case "voice_clone":
		return 1, "", nil
	default:
		return 0, "", fmt.Errorf("%w: unsupported media kind %s", ErrMediaUsageIncomplete, kind)
	}
}

// A terminal video response may arrive before usage metadata. Only unpriced,
// incomplete results may be refreshed; complete results stay immutable for dedup.
func MediaJobNeedsUsageRefresh(job *MediaGenerationJob) bool {
	if job == nil || job.Kind != MediaJobKindVideoGeneration || job.Status != MediaJobStatusSucceeded || job.UsageRecordedAt != nil || len(job.BillingSnapshotJSON) == 0 {
		return false
	}
	var snap MediaBillingSnapshot
	if json.Unmarshal(job.BillingSnapshotJSON, &snap) != nil {
		return false
	}
	_, tier, err := mediaBillableUnits(job, snap.Kind)
	needsDuration := snap.Price.Mode != BillingModePerRequest || (snap.AccountPrice != nil && snap.AccountPrice.Mode != BillingModePerRequest)
	if err != nil && needsDuration {
		return true
	}
	return tier == "" && (snap.Price.Default == nil || (snap.AccountPrice != nil && snap.AccountPrice.Default == nil))
}
