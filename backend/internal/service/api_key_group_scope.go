package service

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// EffectiveGroupScope keeps historical records and older cache fixtures scoped
// to their single group. An empty value never grants public access.
func (k *APIKey) EffectiveGroupScope() string {
	if k == nil || strings.TrimSpace(k.GroupScope) == "" {
		return APIKeyGroupScopeSingle
	}
	return k.GroupScope
}

func (s *APIKeyService) normalizeAPIKeyGroupScope(ctx context.Context, user *User, scope string, groupID *int64, groupIDs []int64) (string, *int64, []int64, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		if groupID != nil {
			scope = APIKeyGroupScopeSingle
		} else if len(groupIDs) > 0 {
			scope = APIKeyGroupScopeSelected
		} else {
			scope = APIKeyGroupScopePublic
		}
	}
	validate := func(id int64) error {
		if id <= 0 || s.groupRepo == nil {
			return ErrGroupNotAllowed
		}
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if group == nil || group.Status != StatusActive || !s.canUserBindGroup(ctx, user, group) {
			return ErrGroupNotAllowed
		}
		return nil
	}
	switch scope {
	case APIKeyGroupScopePublic:
		if len(groupIDs) > 0 {
			return "", nil, nil, infraerrors.BadRequest("INVALID_GROUP_SCOPE", "Public keys cannot specify selected group IDs")
		}
		return scope, nil, []int64{}, nil
	case APIKeyGroupScopeSingle:
		if len(groupIDs) > 0 {
			return "", nil, nil, infraerrors.BadRequest("INVALID_GROUP_SCOPE", "Single-group keys must use group_id")
		}
		// Legacy ungrouped keys remain representable without broadening access.
		if groupID != nil {
			if err := validate(*groupID); err != nil {
				return "", nil, nil, err
			}
		}
		return scope, groupID, []int64{}, nil
	case APIKeyGroupScopeSelected:
		if len(groupIDs) == 0 || len(groupIDs) > 100 {
			return "", nil, nil, infraerrors.BadRequest("INVALID_GROUP_IDS", "Select between 1 and 100 groups")
		}
		seen := make(map[int64]bool, len(groupIDs))
		ids := make([]int64, 0, len(groupIDs))
		for _, id := range groupIDs {
			if seen[id] {
				continue
			}
			if err := validate(id); err != nil {
				return "", nil, nil, err
			}
			seen[id] = true
			ids = append(ids, id)
		}
		return scope, nil, ids, nil
	default:
		return "", nil, nil, infraerrors.BadRequest("INVALID_GROUP_SCOPE", "group_scope must be single, public or selected")
	}
}
