package service

import "context"

// GatewayRequestSubscription loads the payer for the actual request group.
// A WebSocket's group is selected only once its first model is available.
func (s *APIKeyService) GatewayRequestSubscription(ctx context.Context, key *APIKey) (*UserSubscription, error) {
	if key == nil || key.Group == nil || !key.Group.IsSubscriptionType() {
		return nil, nil
	}
	if s.userSubRepo == nil {
		return nil, ErrSubscriptionNotFound
	}
	sub, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, key.UserID, key.Group.ID)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.UserID != key.UserID || sub.GroupID != key.Group.ID || !sub.IsActive() {
		return nil, ErrSubscriptionNotFound
	}
	copy := *sub
	return &copy, nil
}
