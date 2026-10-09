package service

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

// gatewayAccountModelIDs is the configured provider catalogue shared by API
// discovery and all-platform group prices. It preserves provider retirement
// rules and explicit aliases instead of inferring an unrelated default list.
func gatewayAccountModelIDs(account *Account) []string {
	if DefaultMediaModels(account.Platform) != nil {
		ids := make([]string, 0)
		for _, model := range account.AvailableMediaModels() {
			ids = append(ids, model.ID)
		}
		return ids
	}
	if account.IsOpenAI() {
		ids := make([]string, 0)
		for _, model := range account.AvailableOpenAIModels() {
			ids = append(ids, model.ID)
		}
		return ids
	}
	mapping := account.GetModelMapping()
	if len(mapping) > 0 {
		ids := make([]string, 0, len(mapping))
		for model, upstream := range mapping {
			if !account.IsRetiredModel(upstream) {
				ids = append(ids, model)
			}
		}
		sort.Strings(ids)
		return ids
	}
	var ids []string
	switch account.Platform {
	case PlatformAnthropic:
		ids = make([]string, 0, len(claude.DefaultModels))
		for _, model := range claude.DefaultModels {
			ids = append(ids, model.ID)
		}
	case PlatformGemini:
		ids = make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
	case PlatformAntigravity:
		ids = make([]string, 0)
		for _, model := range antigravity.DefaultModels() {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func gatewayAccountsModelIDs(accounts []Account) []string {
	models := make(map[string]struct{})
	for i := range accounts {
		for _, id := range gatewayAccountModelIDs(&accounts[i]) {
			models[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Catalogue eligibility follows gateway admission. A group requirement cannot
// expose a model merely because an ineligible account still belongs to its pool.
func gatewayEligibleGroupAccounts(group *Group, accounts []Account) []Account {
	eligible := make([]Account, 0, len(accounts))
	if group == nil {
		return eligible
	}
	for _, account := range accounts {
		if account.Platform == PlatformAll || !account.IsSchedulable() || account.IsQuotaExceeded() ||
			(group.RequireOAuthOnly && account.Type == AccountTypeAPIKey) ||
			(group.RequirePrivacySet && !account.IsPrivacySet()) {
			continue
		}
		eligible = append(eligible, account)
	}
	return eligible
}

// gatewayModelPlatform follows the gateway's stable protocol preference, so a
// shared alias is priced from the same provider pool used by Responses calls.
func gatewayModelPlatform(ctx context.Context, group *Group, accounts []Account, model string, channels *ChannelService) (string, error) {
	request := GatewayGroupRequest{Path: "/v1/responses", Model: model}
	candidate := GatewayRoutingCandidate{Group: group, Accounts: accounts}
	for _, requireExplicit := range []bool{true, false} {
		if requireExplicit && gatewayNaturalModelPlatform(model) != "" {
			continue
		}
		for _, platform := range gatewayPreferredPlatforms(request) {
			target, channelMapped := model, false
			platformCandidate := candidate
			if channels != nil {
				effective := *group
				effective.Platform, effective.Hydrated = platform, true
				modelCtx := context.WithValue(ctx, ctxkey.Group, &effective)
				if _, err := channels.GetChannelForGroup(modelCtx, group.ID); err != nil {
					return "", err
				}
				mapping := channels.ResolveChannelMapping(modelCtx, group.ID, model)
				target, channelMapped = mapping.MappedModel, mapping.Mapped
				billingModel := billingModelForRestriction(mapping.BillingModelSource, model, target)
				if billingModel != "" && channels.IsModelRestricted(modelCtx, group.ID, billingModel) {
					continue
				}
				if mapping.BillingModelSource == BillingModelSourceUpstream {
					platformCandidate.Accounts = make([]Account, 0, len(accounts))
					for _, account := range accounts {
						upstream := resolveAccountUpstreamModel(&account, target)
						if upstream != "" && !channels.IsModelRestricted(modelCtx, group.ID, upstream) {
							platformCandidate.Accounts = append(platformCandidate.Accounts, account)
						}
					}
				}
			}
			if gatewayCandidateSupports(platformCandidate, platform, target, request, requireExplicit, channelMapped) {
				return platform, nil
			}
		}
	}
	// Media models use their explicit provider catalogue and endpoint family.
	for i := range accounts {
		if DefaultMediaModels(accounts[i].Platform) == nil {
			continue
		}
		for _, id := range gatewayAccountModelIDs(&accounts[i]) {
			if id == model {
				return accounts[i].Platform, nil
			}
		}
	}
	return "", nil
}
