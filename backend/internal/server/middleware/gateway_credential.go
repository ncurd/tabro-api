package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errInvalidGatewayCredential = errors.New("invalid gateway credential")

// resolveGatewayCredential keeps API keys as the canonical routing/billing
// principal while accepting a verified external OAuth token only when it came
// from Authorization: Bearer. JWT-shaped bearer values never downgrade to an
// API-key lookup after validation fails.
func resolveGatewayCredential(
	ctx context.Context,
	credential string,
	fromAuthorization bool,
	apiKeyService *service.APIKeyService,
	resourceServer *service.GatewayResourceServer,
) (*service.APIKey, *service.GatewayOIDCPrincipal, error) {
	credential = strings.TrimSpace(credential)
	if credential == "" || apiKeyService == nil {
		return nil, nil, errInvalidGatewayCredential
	}

	if looksLikeGatewayJWT(credential) {
		// OAuth access tokens are bearer credentials. Reject JWT-shaped values
		// supplied through API-key headers/query parameters before any API-key
		// repository lookup so they can neither downgrade nor leak into DB
		// instrumentation.
		if !fromAuthorization || resourceServer == nil || !resourceServer.Enabled() {
			return nil, nil, errInvalidGatewayCredential
		}
		apiKey, principal, err := resourceServer.Authenticate(ctx, credential)
		if err != nil {
			return nil, nil, err
		}
		return apiKey, principal, nil
	}

	apiKey, err := apiKeyService.GetByKey(ctx, credential)
	if err != nil {
		return nil, nil, err
	}
	// Managed keys are internal billing records and are never bearer secrets.
	if apiKey == nil || apiKey.OIDCManaged {
		return nil, nil, errInvalidGatewayCredential
	}
	return apiKey, nil, nil
}

func looksLikeGatewayJWT(value string) bool {
	// API keys issued by this service cannot contain dots. Treat every compact
	// three-segment credential as JWT-shaped even when it is malformed (padding,
	// empty segments, bad characters, or excessive size), so a failed JWT can
	// never downgrade into a database API-key lookup.
	return strings.Count(strings.TrimSpace(value), ".") == 2
}

func gatewayRequiredScopeChallenge(cfg *config.Config) string {
	if cfg == nil {
		return "llm.invoke"
	}
	scopes := strings.FieldsFunc(cfg.Gateway.ResourceServer.RequiredScopes, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(scopes) == 0 {
		return "llm.invoke"
	}
	return strings.Join(scopes, " ")
}

// resolveGatewayAPIKey remains as a narrow compatibility helper for internal
// callers/tests. Local Tabro JWTs are intentionally no longer accepted.
func resolveGatewayAPIKey(
	ctx context.Context,
	credential string,
	_ bool,
	apiKeyService *service.APIKeyService,
	_ *service.AuthService,
) (*service.APIKey, error) {
	apiKey, _, err := resolveGatewayCredential(ctx, credential, false, apiKeyService, nil)
	return apiKey, err
}
