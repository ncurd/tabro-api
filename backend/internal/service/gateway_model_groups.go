package service

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type gatewayModelGroupsKey struct{}
type gatewayScopedModelLookupKey struct{}

const gatewayGeminiCataloguePlatform = "gemini_native_catalogue"
const gatewayAntigravityGeminiCataloguePlatform = "antigravity_gemini_catalogue"

// WithGatewayModelGroups scopes a model catalogue to groups authorized by the
// gateway. An empty scope must remain an empty catalogue, never a global list.
func WithGatewayModelGroups(ctx context.Context, groups []*Group) context.Context {
	copyGroups := make([]*Group, 0, len(groups))
	for _, group := range groups {
		if group != nil {
			copyGroup := *group
			copyGroups = append(copyGroups, &copyGroup)
		}
	}
	return context.WithValue(ctx, gatewayModelGroupsKey{}, copyGroups)
}

func GatewayModelGroups(ctx context.Context) ([]*Group, bool) {
	groups, ok := ctx.Value(gatewayModelGroupsKey{}).([]*Group)
	return groups, ok
}

func (s *GatewayService) scopedAvailableModels(ctx context.Context, groups []*Group, platform string) []string {
	// Hide this scope for the individual group lookups to avoid recursion. Each
	// lookup remains explicitly grouped; it never queries all provider accounts.
	ctx = context.WithValue(ctx, gatewayModelGroupsKey{}, nil)
	ctx = context.WithValue(ctx, gatewayScopedModelLookupKey{}, true)
	models := make(map[string]struct{})
	seen := make(map[int64]struct{})
	for _, group := range groups {
		if group == nil || group.ID <= 0 || !group.IsActive() {
			continue
		}
		if _, ok := seen[group.ID]; ok {
			continue
		}
		seen[group.ID] = struct{}{}
		groupCtx := context.WithValue(ctx, ctxkey.Group, group)
		if s.channelService != nil {
			if _, err := s.channelService.GetChannelForGroup(groupCtx, group.ID); err != nil {
				continue
			}
		}
		for _, model := range s.GetAvailableModels(groupCtx, &group.ID, platform) {
			models[model] = struct{}{}
		}
	}
	result := make([]string, 0, len(models))
	for model := range models {
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

// Native Gemini catalogues include Gemini-capable Antigravity providers while
// excluding their Claude models. They keep the gateway's authorized group scope.
func (s *GatewayService) GetAvailableGeminiModels(ctx context.Context, groupID *int64, antigravityOnly bool) []string {
	platform := gatewayGeminiCataloguePlatform
	if antigravityOnly {
		platform = gatewayAntigravityGeminiCataloguePlatform
	}
	return s.GetAvailableModels(ctx, groupID, platform)
}
