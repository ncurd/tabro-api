package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayRoutingCandidateRepository = (*groupRepository)(nil)

// ListGatewayRoutingCandidates never widens an explicit key's permissions.
// An empty explicit set grants nothing; only the caller's publicOnly mode can
// ask for the public standard pools. Accounts are read from each pool's edges,
// never inferred from another private or subscription group.
func (r *groupRepository) ListGatewayRoutingCandidates(ctx context.Context, groupIDs []int64, publicOnly bool) ([]service.GatewayRoutingCandidate, error) {
	if len(groupIDs) == 0 && !publicOnly {
		return []service.GatewayRoutingCandidate{}, nil
	}
	query := r.client.Group.Query().Where(group.StatusEQ(service.StatusActive), group.DeletedAtIsNil())
	if len(groupIDs) > 0 {
		query = query.Where(group.IDIn(groupIDs...))
	}
	if publicOnly {
		query = query.Where(group.IsExclusiveEQ(false), group.SubscriptionTypeEQ(service.SubscriptionTypeStandard))
	}
	models, err := query.
		Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).
		WithAccounts(func(q *dbent.AccountQuery) {
			q.Where(account.StatusEQ(service.StatusActive), account.SchedulableEQ(true), account.DeletedAtIsNil()).
				Order(dbent.Asc(account.FieldPriority), dbent.Asc(account.FieldID))
		}).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.GatewayRoutingCandidate, 0, len(models))
	for _, model := range models {
		g := groupEntityToService(model)
		// Corrupt or historical all-platform groups must not provide a backdoor
		// into exclusive or subscription entitlements.
		if g.Platform == service.PlatformAll && (g.IsExclusive || g.SubscriptionType != service.SubscriptionTypeStandard) {
			continue
		}
		candidate := service.GatewayRoutingCandidate{Group: g, Accounts: []service.Account{}}
		for _, entity := range model.Edges.Accounts {
			a := accountEntityToService(entity)
			if a.Platform == service.PlatformAll || !a.IsSchedulable() || a.IsQuotaExceeded() {
				continue
			}
			if g.RequireOAuthOnly && a.Type == service.AccountTypeAPIKey {
				continue
			}
			if g.RequirePrivacySet && !a.IsPrivacySet() {
				continue
			}
			a.GroupIDs = []int64{g.ID}
			candidate.Accounts = append(candidate.Accounts, *a)
		}
		out = append(out, candidate)
	}
	return out, nil
}
