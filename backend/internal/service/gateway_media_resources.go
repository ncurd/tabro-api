package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

var ErrGatewayMediaResourceNotFound = infraerrors.NotFound("MEDIA_RESOURCE_NOT_FOUND", "Media resource not found")

// UsesDynamicGatewayGroups distinguishes scopes that resolve a pool per request
// from historical credentials that retain their one-group ownership behavior.
func (k *APIKey) UsesDynamicGatewayGroups() bool {
	return k != nil && (k.EffectiveGroupScope() != APIKeyGroupScopeSingle || (k.Group != nil && k.Group.Platform == PlatformAll))
}

// GatewayMediaAllowedGroups is for existing resource access, not scheduling.
// A group's lack of usable accounts must not hide an already completed result.
func (s *APIKeyService) GatewayMediaAllowedGroups(ctx context.Context, key *APIKey) ([]*Group, error) {
	candidates, err := s.gatewayMediaAllowedCandidates(ctx, key)
	if err != nil {
		return nil, err
	}
	groups := make([]*Group, 0, len(candidates))
	for _, candidate := range candidates {
		groups = append(groups, candidate.Group)
	}
	return groups, nil
}

func (s *APIKeyService) gatewayMediaAllowedCandidates(ctx context.Context, key *APIKey) ([]GatewayRoutingCandidate, error) {
	if key == nil || key.User == nil || !key.User.IsActive() || key.UserID != key.User.ID {
		return nil, ErrGroupNotAllowed
	}
	var ids []int64
	publicOnly := key.EffectiveGroupScope() == APIKeyGroupScopePublic
	switch key.EffectiveGroupScope() {
	case APIKeyGroupScopePublic:
	case APIKeyGroupScopeSelected:
		ids = key.GroupIDs
		if len(ids) == 0 {
			return nil, ErrGroupNotAllowed
		}
	case APIKeyGroupScopeSingle:
		if key.GroupID == nil {
			return nil, ErrGroupNotAllowed
		}
		ids = []int64{*key.GroupID}
	default:
		return nil, ErrGroupNotAllowed
	}
	repo, ok := s.groupRepo.(GatewayRoutingCandidateRepository)
	if !ok {
		return nil, ErrGatewayRouteUnavailable
	}
	candidates, err := repo.ListGatewayRoutingCandidates(ctx, ids, publicOnly)
	if err != nil {
		return nil, ErrGatewayRouteUnavailable
	}
	owner := key.User
	if key.EffectiveGroupScope() == APIKeyGroupScopeSelected {
		if s.userRepo == nil {
			return nil, ErrGroupNotAllowed
		}
		owner, err = s.userRepo.GetByID(ctx, key.UserID)
		if err != nil || owner == nil || !owner.IsActive() {
			return nil, ErrGroupNotAllowed
		}
	}
	allowed := make([]GatewayRoutingCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		g := candidate.Group
		if g == nil || !g.IsActive() || !IsGroupContextValid(g) {
			continue
		}
		if publicOnly && (g.IsExclusive || g.SubscriptionType != SubscriptionTypeStandard) {
			continue
		}
		if !publicOnly && !gatewayContainsGroup(ids, g.ID) {
			continue
		}
		if g.Platform == PlatformAll && (g.IsExclusive || g.SubscriptionType != SubscriptionTypeStandard) {
			continue
		}
		if key.EffectiveGroupScope() == APIKeyGroupScopeSelected && !s.canUserBindGroup(ctx, owner, g) {
			continue
		}
		allowed = append(allowed, candidate)
	}
	return allowed, nil
}

// ResolveGatewayMediaResource freezes the recorded pool before billing. It
// never switches an enrolled voice to a different upstream credential.
// An empty model means metadata; a nonempty model means paid synthesis.
func (s *APIKeyService) ResolveGatewayMediaResource(ctx context.Context, key *APIKey, id, kind, model string) (*APIKey, *MediaGenerationJob, error) {
	if s.GatewayMediaResources == nil {
		return nil, nil, ErrGatewayRouteUnavailable
	}
	job, err := s.GatewayMediaResources.GetByPublicID(ctx, id)
	if err != nil {
		return nil, nil, ErrGatewayRouteUnavailable
	}
	if key == nil || job == nil || job.Kind != kind || job.UserID != key.UserID || job.APIKeyID != key.ID || job.GroupID == nil {
		return nil, nil, ErrGatewayMediaResourceNotFound
	}
	candidates, err := s.gatewayMediaAllowedCandidates(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	var group *Group
	var accounts []Account
	for _, candidate := range candidates {
		if candidate.Group.ID == *job.GroupID {
			group, accounts = candidate.Group, candidate.Accounts
			break
		}
	}
	if group == nil {
		return nil, nil, ErrGatewayMediaResourceNotFound
	}
	platform := job.Platform
	if platform == "" {
		switch job.Provider {
		case MediaProviderDashScope:
			platform = PlatformDashScope
		case MediaProviderAzureSpeech:
			platform = PlatformAzureSpeech
		case MediaProviderVolcengineArk:
			platform = PlatformVolcengineArk
		}
	}
	if platform == "" || (group.Platform != PlatformAll && group.Platform != platform) {
		return nil, nil, ErrGatewayMediaResourceNotFound
	}
	if model != "" {
		if kind != MediaJobKindVoiceClone || platform != PlatformDashScope {
			return nil, nil, ErrGatewayMediaResourceNotFound
		}
		routingModel := model
		effectiveGroup := *group
		effectiveGroup.Platform = platform
		groupCtx := context.WithValue(ctx, ctxkey.Group, &effectiveGroup)
		channels := s.gatewayRoutingChannels()
		var mapping ChannelMappingResult
		if channels != nil {
			if _, channelErr := channels.GetChannelForGroup(groupCtx, group.ID); channelErr != nil {
				return nil, nil, ErrGatewayRouteUnavailable
			}
			mapping = channels.ResolveChannelMapping(groupCtx, group.ID, model)
			routingModel = mapping.MappedModel
			if billingModel := billingModelForRestriction(mapping.BillingModelSource, model, routingModel); billingModel != "" && channels.IsModelRestricted(groupCtx, group.ID, billingModel) {
				return nil, nil, ErrGatewayRouteUnavailable
			}
		}
		usable := false
		enrolledModel := gjson.GetBytes(job.RequestJSON, "upstream_model").String()
		for _, account := range accounts {
			if enrolledModel != "" && account.GetMappedModel(routingModel) != enrolledModel {
				continue
			}
			if channels != nil && mapping.BillingModelSource == BillingModelSourceUpstream {
				upstreamModel := resolveAccountUpstreamModel(&account, routingModel)
				if upstreamModel == "" || channels.IsModelRestricted(groupCtx, group.ID, upstreamModel) {
					continue
				}
			}
			if account.ID == job.AccountID && account.Platform == platform && account.IsSchedulable() && !account.IsQuotaExceeded() &&
				(!group.RequireOAuthOnly || account.Type != AccountTypeAPIKey) && (!group.RequirePrivacySet || account.IsPrivacySet()) && account.IsModelSupported(routingModel) {
				usable = true
			}
		}
		if !usable {
			return nil, nil, ErrGatewayRouteUnavailable
		}
	}
	copyGroup := *group
	copyGroup.Platform = platform
	copyGroup.FallbackGroupID, copyGroup.FallbackGroupIDOnInvalidRequest = nil, nil
	copyKey := *key
	copyKey.Group, copyKey.GroupID = &copyGroup, &copyGroup.ID
	return &copyKey, job, nil
}
