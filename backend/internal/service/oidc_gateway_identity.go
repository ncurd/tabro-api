package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func normalizeOIDCGatewayIdentity(issuer, subject string) (string, string, error) {
	issuer = strings.TrimSpace(issuer)
	subject = strings.TrimSpace(subject)
	if issuer == "" || subject == "" || len(issuer) > 512 || len(subject) > 512 {
		return "", "", infraerrors.BadRequest("INVALID_OIDC_IDENTITY", "issuer and subject are required and must be at most 512 characters")
	}
	parsedIssuer, err := url.Parse(issuer)
	if err != nil || parsedIssuer == nil || parsedIssuer.Hostname() == "" || (parsedIssuer.Scheme != "https" && parsedIssuer.Scheme != "http") || parsedIssuer.User != nil || parsedIssuer.RawQuery != "" || parsedIssuer.ForceQuery || parsedIssuer.Fragment != "" {
		return "", "", infraerrors.BadRequest("INVALID_OIDC_ISSUER", "issuer must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	return issuer, subject, nil
}

// ProvisionOIDCGatewayIdentity prepares an active user's internal billing key
// and binds a stable external identity without requiring the user to log in.
// The managed key's opaque value is an internal record, never a client secret.
func (s *APIKeyService) ProvisionOIDCGatewayIdentity(ctx context.Context, userID int64, issuer, subject string) (*APIKey, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER_ID", "user ID must be positive")
	}
	var err error
	issuer, subject, err = normalizeOIDCGatewayIdentity(issuer, subject)
	if err != nil {
		return nil, err
	}
	if s == nil || s.userRepo == nil || s.apiKeyRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OIDC_GATEWAY_PROVISION_UNAVAILABLE", "OIDC gateway provisioning is unavailable")
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}
	// Check an existing global binding before creating a new managed key. A
	// mistaken payer selection must fail without leaving an unbound record.
	existing, err := s.GetOIDCGatewayKeyByIdentity(ctx, issuer, subject)
	if err == nil {
		if existing.UserID != userID || !existing.OIDCManaged {
			return nil, ErrOIDCGatewayIdentityConflict
		}
		return existing, nil
	}
	if !errors.Is(err, ErrAPIKeyNotFound) {
		return nil, err
	}

	key, err := s.EnsureOIDCGatewayKey(ctx, userID)
	if err != nil {
		return nil, err
	}
	if key == nil || key.UserID != userID || !key.OIDCManaged {
		return nil, ErrOIDCGatewayKeyConflict
	}
	// Re-read the exact record before binding. A stale cache or a colliding key
	// must never direct an administrator's identity to a different payer.
	key, err = s.apiKeyRepo.GetByID(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	if key == nil || key.UserID != userID || !key.OIDCManaged {
		return nil, ErrOIDCGatewayKeyConflict
	}
	if err := s.BindOIDCGatewayIdentity(ctx, key.ID, issuer, subject); err != nil {
		return nil, err
	}
	key.OIDCIssuer = issuer
	key.OIDCSubject = subject
	return key, nil
}
