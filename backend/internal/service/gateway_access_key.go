package service

import (
	"context"
	"errors"
	"net/http"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type gatewayAccessKeyAuthority interface {
	ValidateAccessKey(context.Context, string, string) (bc.AccessKeyValidation, error)
}

// RequiresAuthCredentials uses the live billing policy and fails closed if its
// settings cannot be read. A static auto-provisioning deployment is Auth-only too.
func (s *APIKeyService) RequiresAuthCredentials(ctx context.Context) (bool, error) {
	if s == nil {
		return false, ErrGatewayOIDCUnavailable
	}
	if s.GatewayBilling != nil {
		current, err := s.GatewayBilling.current(ctx)
		if err != nil {
			return false, ErrGatewayOIDCUnavailable
		}
		policy, err := current.oidcBillingPolicy(ctx)
		if err != nil {
			return false, ErrGatewayOIDCUnavailable
		}
		required := policy.Enabled || current.connection.ResourceServer.Enabled || current.connection.ResourceServer.AutoProvision
		if required || current.owner != nil {
			// A runtime child carries the authoritative connection settings. Do
			// not revive static defaults after an explicit runtime mode change.
			return required, nil
		}
	}
	return s.cfg != nil && (s.cfg.Gateway.ResourceServer.Enabled || s.cfg.Gateway.ResourceServer.AutoProvision), nil
}

func (s *APIKeyService) requireLocalAPIKeyManagement(ctx context.Context) error {
	disabled, err := s.RequiresAuthCredentials(ctx)
	if err != nil {
		return infraerrors.ServiceUnavailable("AUTH_KEY_AUTHORITY_UNAVAILABLE", "Auth key authority is unavailable")
	}
	if disabled {
		return infraerrors.Forbidden("AUTH_ACCESS_KEYS_REQUIRED", "Manage access keys in the Auth account center; local API keys are disabled in OIDC mode")
	}
	return nil
}

// AuthenticateAccessKey is called for every request, including reads and
// non-billable writes. Only producer OAuth tokens may call the Auth validator.
// The opaque value is never looked up locally or cached as successful auth.
func (s *GatewayResourceServer) AuthenticateAccessKey(ctx context.Context, key, permission string) (*APIKey, *GatewayOIDCPrincipal, error) {
	if s != nil && s.live {
		server, err := s.currentResourceServer(ctx)
		if err != nil {
			return nil, nil, ErrGatewayOIDCUnavailable
		}
		return server.AuthenticateAccessKey(ctx, key, permission)
	}
	principal, err := s.VerifyAccessKey(ctx, key, permission)
	if err != nil {
		return nil, nil, err
	}
	apiKey, principal, err := s.authenticatePrincipal(ctx, principal, true)
	if err != nil {
		return nil, nil, err
	}
	if !apiKey.OIDCManaged {
		return nil, nil, ErrGatewayOIDCTokenInvalid
	}
	return apiKey, principal, nil
}

// VerifyAccessKey performs the same live authorization without provisioning a
// local routing identity, for financial reads that do not dispatch suppliers.
func (s *GatewayResourceServer) VerifyAccessKey(ctx context.Context, key, permission string) (*GatewayOIDCPrincipal, error) {
	if s != nil && s.live {
		server, err := s.currentResourceServer(ctx)
		if err != nil {
			return nil, ErrGatewayOIDCUnavailable
		}
		return server.VerifyAccessKey(ctx, key, permission)
	}
	if s == nil || !s.cfg.Enabled || !s.billingCenterEnabled || s.apiKeyService == nil || s.apiKeyService.GatewayBilling == nil {
		return nil, ErrGatewayOIDCUnavailable
	}
	current, err := s.apiKeyService.GatewayBilling.current(ctx)
	if err != nil || current == nil || !current.enabled {
		return nil, ErrGatewayOIDCUnavailable
	}
	authority, ok := current.authority.(gatewayAccessKeyAuthority)
	if !ok {
		return nil, ErrGatewayOIDCUnavailable
	}
	validated, err := authority.ValidateAccessKey(ctx, key, permission)
	if err != nil {
		if errors.Is(err, bc.ErrConflict) {
			return nil, ErrGatewayOIDCTokenInvalid
		}
		var remote *bc.RemoteError
		if errors.As(err, &remote) && remote.Status == http.StatusForbidden {
			return nil, ErrGatewayOIDCScopeDenied
		}
		return nil, ErrGatewayOIDCUnavailable
	}
	principal, err := s.verify(ctx, validated.SubjectAccessToken, permission)
	if err != nil {
		return nil, err
	}
	if principal.Subject != validated.UserID || principal.Tenant != validated.TenantID || principal.ClientID != validated.OriginAppID || principal.AccessKeyID != validated.KeyID || principal.AccessKeyVersion != validated.Version {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	principal.AuthBilled = true
	principal.SubjectProof = validated.SubjectAccessToken
	return principal, nil
}
