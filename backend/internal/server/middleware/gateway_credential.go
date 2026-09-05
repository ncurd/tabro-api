package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errInvalidGatewayCredential = errors.New("invalid gateway credential")

// resolveGatewayAPIKey keeps API keys as the canonical gateway principal. For
// an OIDC-issued local JWT it resolves the persistent billing key whose ID was
// signed into the token, then the caller runs the exact same authorization and
// billing policy as a request authenticated with that key's secret.
func resolveGatewayAPIKey(
	ctx context.Context,
	credential string,
	allowOIDCToken bool,
	apiKeyService *service.APIKeyService,
	authService *service.AuthService,
) (*service.APIKey, error) {
	// Existing custom API keys cannot contain '.', so avoid JWT parsing work for
	// normal keys while also avoiding a database miss on every OIDC request.
	// Invalid JWT-shaped values still fall through to API-key lookup for
	// compatibility with imported legacy keys that may contain dots.
	if allowOIDCToken && authService != nil && strings.Count(credential, ".") == 2 {
		claims, tokenErr := authService.ValidateToken(credential)
		if tokenErr == nil {
			return resolveOIDCGatewayAPIKey(ctx, claims, apiKeyService)
		}
		if claims != nil && errors.Is(tokenErr, service.ErrTokenExpired) {
			return nil, errInvalidGatewayCredential
		}
	}
	apiKey, err := apiKeyService.GetByKey(ctx, credential)
	if err != nil {
		return nil, err
	}
	// The persistent record behind an OIDC token is a billing identity, not a
	// bearer secret. The persisted marker—not its configurable-looking string
	// prefix—is authoritative, so ordinary keys remain compatible with every
	// supported prefix configuration.
	if apiKey.OIDCManaged {
		return nil, errInvalidGatewayCredential
	}
	return apiKey, nil
}

func resolveOIDCGatewayAPIKey(ctx context.Context, claims *service.JWTClaims, apiKeyService *service.APIKeyService) (*service.APIKey, error) {
	if claims == nil || claims.AuthMethod != service.AuthMethodOIDC || claims.BillingAPIKeyID <= 0 {
		return nil, errInvalidGatewayCredential
	}

	apiKey, err := apiKeyService.GetByID(ctx, claims.BillingAPIKeyID)
	if err != nil {
		if errors.Is(err, service.ErrAPIKeyNotFound) {
			return nil, errInvalidGatewayCredential
		}
		return nil, err
	}
	if apiKey == nil || !apiKey.OIDCManaged || apiKey.User == nil {
		return nil, errInvalidGatewayCredential
	}
	if apiKey.UserID != claims.UserID || apiKey.User.ID != claims.UserID {
		return nil, errInvalidGatewayCredential
	}
	if claims.TokenVersion != apiKey.User.TokenVersion {
		return nil, service.ErrTokenRevoked
	}
	return apiKey, nil
}
