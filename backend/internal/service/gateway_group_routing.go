package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrGatewayRouteUnavailable = infraerrors.ServiceUnavailable("GATEWAY_ROUTE_UNAVAILABLE", "No available account in an allowed group for this model")

type gatewayRoutingKeyContextKey struct{}

// The original scope is retained for WebSocket selection after its first frame.
// The cached credential is read-only; effective routing always uses a clone.
func WithGatewayRoutingKey(ctx context.Context, key *APIKey) context.Context {
	return context.WithValue(ctx, gatewayRoutingKeyContextKey{}, key)
}

func GatewayRoutingKeyFromContext(ctx context.Context) *APIKey {
	key, _ := ctx.Value(gatewayRoutingKeyContextKey{}).(*APIKey)
	return key
}

// GatewayGroupRequest describes routing inputs, never credentials or prompts.
type GatewayGroupRequest struct {
	Path              string
	Model             string
	Voice             string
	Catalog           bool
	MetadataOnly      bool
	ForcePlatform     string
	RequiredTransport OpenAIUpstreamTransport
}

// ResolveGatewayGroup freezes a request's routing pool before billing. The
// authenticated/cache key is immutable: parallel models may select different
// groups without changing the credential's allowed scope.
func (s *APIKeyService) ResolveGatewayGroup(ctx context.Context, key *APIKey, request GatewayGroupRequest) (*APIKey, []*Group, error) {
	if key == nil || key.User == nil {
		return nil, nil, ErrGatewayRouteUnavailable
	}
	scope := key.EffectiveGroupScope()
	if scope == APIKeyGroupScopeSingle && (key.Group == nil || key.Group.Platform != PlatformAll) {
		return key, nil, nil // Preserve legacy single-group and ungrouped routing.
	}
	if request.MetadataOnly {
		copy := *key
		return &copy, nil, nil
	}
	repo, ok := s.groupRepo.(GatewayRoutingCandidateRepository)
	if !ok {
		return nil, nil, ErrGatewayRouteUnavailable
	}
	var ids []int64
	publicOnly := scope == APIKeyGroupScopePublic
	switch scope {
	case APIKeyGroupScopeSingle:
		if key.GroupID == nil {
			return nil, nil, ErrGatewayRouteUnavailable
		}
		ids = []int64{*key.GroupID}
	case APIKeyGroupScopeSelected:
		ids = key.GroupIDs
		if len(ids) == 0 {
			return nil, nil, ErrGroupNotAllowed
		}
	case APIKeyGroupScopePublic:
	default:
		return nil, nil, ErrGroupNotAllowed
	}
	candidates, err := repo.ListGatewayRoutingCandidates(ctx, ids, publicOnly)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: routing pools unavailable", ErrGatewayRouteUnavailable)
	}
	// Recheck selected permissions from the live owner, not the abbreviated
	// authentication-cache user. Removed memberships/subscriptions take effect.
	if scope == APIKeyGroupScopeSelected {
		if s.userRepo == nil {
			return nil, nil, ErrGroupNotAllowed
		}
		owner, ownerErr := s.userRepo.GetByID(ctx, key.UserID)
		if ownerErr != nil || owner == nil || !owner.IsActive() {
			return nil, nil, ErrGroupNotAllowed
		}
		allowed := candidates[:0]
		for _, candidate := range candidates {
			if candidate.Group != nil && s.canUserBindGroup(ctx, owner, candidate.Group) {
				allowed = append(allowed, candidate)
			}
		}
		candidates = allowed
	}
	// Enforce the repository's scope again before granting request access.
	filtered := make([]GatewayRoutingCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		group := candidate.Group
		if group == nil || !group.IsActive() || (publicOnly && (group.IsExclusive || group.SubscriptionType != SubscriptionTypeStandard)) {
			continue
		}
		if !publicOnly && !gatewayContainsGroup(ids, group.ID) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if scope == APIKeyGroupScopeSelected && len(filtered) == 0 {
		return nil, nil, ErrGroupNotAllowed
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Group.SortOrder != filtered[j].Group.SortOrder {
			return filtered[i].Group.SortOrder < filtered[j].Group.SortOrder
		}
		return filtered[i].Group.ID < filtered[j].Group.ID
	})
	groups := make([]*Group, 0, len(filtered))
	for _, candidate := range filtered {
		groups = append(groups, candidate.Group)
	}
	if request.Catalog {
		copy := *key
		copy.Group, copy.GroupID = nil, nil
		if len(groups) > 0 {
			groupCopy := *groups[0]
			copy.Group, copy.GroupID = &groupCopy, &groupCopy.ID
		}
		return &copy, groups, nil
	}
	channels := s.gatewayRoutingChannels()
	transportResolver := NewOpenAIWSProtocolResolver(s.cfg)
	// Named aliases belong to explicitly configured mappings. Broad provider
	// accounts are considered only after every permitted alias route.
	for _, requireExplicit := range []bool{true, false} {
		if requireExplicit && (request.Model == "" || gatewayNaturalModelPlatform(request.Model) != "") {
			continue
		}
		for _, candidate := range filtered {
			group := candidate.Group
			for _, platform := range gatewayPreferredPlatforms(request) {
				if group.Platform != PlatformAll && !gatewayGroupSupportsPlatform(group.Platform, platform) {
					continue
				}
				groupCopy := *group
				if group.Platform == PlatformAll {
					groupCopy.Platform = platform
				}
				groupCtx := context.WithValue(ctx, ctxkey.Group, &groupCopy)
				model := request.Model
				channelMapped := false
				eligible := candidate
				if request.RequiredTransport != OpenAIUpstreamTransportAny && request.RequiredTransport != OpenAIUpstreamTransportHTTPSSE {
					eligible.Accounts = make([]Account, 0, len(candidate.Accounts))
					for _, account := range candidate.Accounts {
						if transportResolver.Resolve(&account).Transport == request.RequiredTransport {
							eligible.Accounts = append(eligible.Accounts, account)
						}
					}
				}
				if channels != nil {
					if _, channelErr := channels.GetChannelForGroup(groupCtx, group.ID); channelErr != nil {
						return nil, nil, ErrGatewayRouteUnavailable
					}
					mapping := channels.ResolveChannelMapping(groupCtx, group.ID, model)
					model, channelMapped = mapping.MappedModel, mapping.Mapped
					if billingModel := billingModelForRestriction(mapping.BillingModelSource, request.Model, model); billingModel != "" && channels.IsModelRestricted(groupCtx, group.ID, billingModel) {
						continue
					}
					if mapping.BillingModelSource == BillingModelSourceUpstream && model != "" {
						accounts := eligible.Accounts
						eligible.Accounts = make([]Account, 0, len(accounts))
						for _, account := range accounts {
							upstreamModel := resolveAccountUpstreamModel(&account, model)
							if upstreamModel != "" && !channels.IsModelRestricted(groupCtx, group.ID, upstreamModel) {
								eligible.Accounts = append(eligible.Accounts, account)
							}
						}
					}
				}
				if !gatewayCandidateSupports(eligible, platform, model, request, requireExplicit, channelMapped) {
					continue
				}
				// Post-admission fallback must not switch to another group's prices
				// or permissions. Legacy single-platform keys retain their behavior.
				groupCopy.FallbackGroupID = nil
				groupCopy.FallbackGroupIDOnInvalidRequest = nil
				copy := *key
				copy.Group, copy.GroupID = &groupCopy, &groupCopy.ID
				return &copy, groups, nil
			}
		}
	}
	return nil, groups, ErrGatewayRouteUnavailable
}

func (s *APIKeyService) gatewayRoutingChannels() *ChannelService {
	if s.GatewayBilling == nil {
		return nil
	}
	if pricer, ok := s.GatewayBilling.creditPricer.(*gatewayCreditPriceCalculator); ok && pricer.gateway != nil {
		return pricer.gateway.channelService
	}
	return nil
}

func gatewayContainsGroup(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func gatewayGroupSupportsPlatform(groupPlatform, platform string) bool {
	return groupPlatform == platform || (groupPlatform == PlatformAntigravity && (platform == PlatformAnthropic || platform == PlatformGemini))
}

func gatewayCandidateSupports(candidate GatewayRoutingCandidate, platform, model string, request GatewayGroupRequest, requireExplicit, channelMapped bool) bool {
	group := candidate.Group
	if platform == PlatformOpenAI && strings.HasSuffix(request.Path, "/messages") && !group.AllowMessagesDispatch {
		return false
	}
	for _, account := range candidate.Accounts {
		if !account.IsSchedulable() || account.IsQuotaExceeded() || (group.RequireOAuthOnly && account.Type == AccountTypeAPIKey) || (group.RequirePrivacySet && !account.IsPrivacySet()) {
			continue
		}
		compatible := account.Platform == platform
		if account.Platform == PlatformAntigravity && (platform == PlatformAnthropic || platform == PlatformGemini) {
			compatible = group.Platform == PlatformAntigravity || account.IsMixedSchedulingEnabled()
		}
		if !compatible || (model != "" && !account.IsModelSupported(model)) {
			continue
		}
		if requireExplicit && !channelMapped {
			if _, mapped := account.ResolveMappedModel(request.Model); !mapped {
				continue
			}
		}
		if account.Platform == PlatformAntigravity && model != "" {
			mapped := mapAntigravityModel(&account, model)
			if mapped == "" || gatewayNaturalModelPlatform(mapped) == PlatformOpenAI {
				continue
			}
		}
		// An unrestricted account cannot accidentally capture another
		// provider's named model; explicit mappings may bridge APIs.
		if natural := gatewayNaturalModelPlatform(model); natural != "" && natural != platform && account.Platform != PlatformAntigravity {
			if _, mapped := account.ResolveMappedModel(model); !mapped {
				continue
			}
		}
		return true
	}
	return false
}

func gatewayNaturalModelPlatform(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "claude"):
		return PlatformAnthropic
	case strings.HasPrefix(model, "gemini"):
		return PlatformGemini
	case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "chatgpt"), strings.HasPrefix(model, "codex"), strings.HasPrefix(model, "dall-e"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return PlatformOpenAI
	default:
		return ""
	}
}

func gatewayPreferredPlatforms(request GatewayGroupRequest) []string {
	path := request.Path
	if request.ForcePlatform != "" {
		return []string{request.ForcePlatform}
	}
	if strings.Contains(path, "/v1beta/") {
		return []string{PlatformGemini, PlatformAntigravity}
	}
	if strings.Contains(path, "/images/") || (request.Model == "" && strings.HasSuffix(path, "/responses")) {
		return []string{PlatformOpenAI}
	}
	if strings.Contains(path, "/audio/transcriptions") || strings.Contains(path, "/audio/voices") {
		return []string{PlatformDashScope}
	}
	if strings.Contains(path, "/audio/speech") {
		if !strings.HasSuffix(path, "/jobs") && (strings.HasPrefix(request.Voice, "voice_") || strings.HasPrefix(request.Model, "qwen3-tts-vc")) {
			return []string{PlatformDashScope}
		}
		return []string{PlatformAzureSpeech}
	}
	if strings.Contains(path, "/videos/") {
		model := strings.ToLower(request.Model)
		if strings.HasPrefix(model, "wan") || strings.Contains(model, "happyhorse") {
			return []string{PlatformDashScope}
		}
		if strings.Contains(model, "seedance") {
			return []string{PlatformVolcengineArk}
		}
		return []string{PlatformDashScope, PlatformVolcengineArk}
	}
	platforms := []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity}
	if strings.Contains(path, "/messages") {
		platforms = []string{PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformOpenAI}
	}
	if natural := gatewayNaturalModelPlatform(request.Model); natural != "" {
		preferred := []string{natural}
		for _, platform := range platforms {
			if platform != natural {
				preferred = append(preferred, platform)
			}
		}
		return preferred
	}
	return platforms
}
