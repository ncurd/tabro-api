package service

import (
	"context"
	"fmt"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GatewayRoutingCandidate contains one authorized routing pool and only its
// explicitly linked, currently schedulable accounts. Provider credentials stay
// in process and must never be returned in an HTTP response.
type GatewayRoutingCandidate struct {
	Group    *Group
	Accounts []Account
}

// GatewayRoutingCandidateRepository is optional so existing group consumers
// and tests do not need to implement routing. Empty groupIDs means all public
// standard groups only when publicOnly is true; otherwise it grants no groups.
type GatewayRoutingCandidateRepository interface {
	ListGatewayRoutingCandidates(ctx context.Context, groupIDs []int64, publicOnly bool) ([]GatewayRoutingCandidate, error)
}

// validateUniversalGroupAccess keeps the shared default pool open to everyone
// without altering any existing group's permissions or subscriptions.
func validateUniversalGroupAccess(platform string, exclusive bool, subscriptionType string) error {
	if platform != PlatformAll {
		return nil
	}
	if exclusive || subscriptionType != SubscriptionTypeStandard {
		return infraerrors.BadRequest("INVALID_UNIVERSAL_GROUP", "All-platform groups must be public standard groups")
	}
	return nil
}

func groupHasOAuthOnlyRequirement(g *Group) bool {
	if g == nil || !g.RequireOAuthOnly {
		return false
	}
	switch g.Platform {
	case PlatformAll, PlatformOpenAI, PlatformAntigravity, PlatformAnthropic, PlatformGemini:
		return true
	default:
		return false
	}
}

// Check before any account write or binding. The provider stored on an account
// remains its real provider even when its destination group supports all of
// them; that must not weaken a group's existing OAuth-only restriction.
func validateAccountOAuthOnlyGroups(ctx context.Context, repo GroupRepository, accountType string, groupIDs []int64) error {
	if accountType != AccountTypeAPIKey || len(groupIDs) == 0 {
		return nil
	}
	if repo == nil {
		return fmt.Errorf("group repository not configured")
	}
	for _, id := range groupIDs {
		g, err := repo.GetByIDLite(ctx, id)
		if err != nil {
			return err
		}
		if groupHasOAuthOnlyRequirement(g) {
			return infraerrors.BadRequest("ACCOUNT_OAUTH_REQUIRED", fmt.Sprintf("分组 [%s] 仅允许 OAuth 账号，apikey 类型账号无法加入", g.Name))
		}
	}
	return nil
}
