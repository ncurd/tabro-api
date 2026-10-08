package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateGatewayResourceServer validates the same trust policy for startup YAML
// and administrator-managed runtime settings.
func ValidateGatewayResourceServer(resource GatewayResourceServerConfig, billingCenterEnabled bool) error {
	if resource.AutoProvision {
		if !resource.Enabled {
			return fmt.Errorf("gateway.resource_server.auto_provision requires resource_server.enabled=true")
		}
		if !billingCenterEnabled {
			return fmt.Errorf("gateway.resource_server.auto_provision requires billing_center.enabled=true")
		}
		if !resource.RequireTenant {
			return fmt.Errorf("gateway.resource_server.auto_provision requires require_tenant=true")
		}
		if !resource.TokenExchange.RequireActor {
			return fmt.Errorf("gateway.resource_server.auto_provision requires token_exchange.require_actor=true")
		}
		if resource.TokenExchange.MaxDelegationDepth != 1 {
			return fmt.Errorf("gateway.resource_server.auto_provision requires token_exchange.max_delegation_depth=1")
		}
	}
	if resource.Enabled {
		rs := resource
		if strings.TrimSpace(rs.IssuerURL) == "" {
			return fmt.Errorf("gateway.resource_server.issuer_url is required when enabled=true")
		}
		if strings.TrimSpace(rs.Audience) == "" {
			return fmt.Errorf("gateway.resource_server.audience is required when enabled=true")
		}
		if len(splitConfigList(rs.RequiredScopes)) == 0 {
			return fmt.Errorf("gateway.resource_server.required_scopes is required when enabled=true")
		}
		for _, scope := range splitConfigList(rs.RequiredScopes) {
			for _, ch := range scope {
				// RFC 6749 scope-token: visible ASCII except DQUOTE and
				// backslash. This also keeps WWW-Authenticate safe.
				if ch < 0x21 || ch > 0x7e || ch == '"' || ch == '\\' {
					return fmt.Errorf("gateway.resource_server.required_scopes contains invalid scope %q", scope)
				}
			}
		}
		if len(splitConfigList(rs.AllowedClientIDs)) == 0 {
			return fmt.Errorf("gateway.resource_server.allowed_client_ids is required when enabled=true")
		}
		if len(splitConfigList(rs.AllowedSigningAlgs)) == 0 {
			return fmt.Errorf("gateway.resource_server.allowed_signing_algs is required when enabled=true")
		}
		if err := ValidateAbsoluteHTTPURL(rs.IssuerURL); err != nil {
			return fmt.Errorf("gateway.resource_server.issuer_url invalid: %w", err)
		}
		issuerURL, _ := url.Parse(strings.TrimSpace(rs.IssuerURL))
		if issuerURL == nil || strings.TrimSpace(issuerURL.Hostname()) == "" || issuerURL.User != nil || issuerURL.RawQuery != "" {
			return fmt.Errorf("gateway.resource_server.issuer_url invalid: issuer must not include userinfo or query")
		}
		if v := strings.TrimSpace(rs.DiscoveryURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("gateway.resource_server.discovery_url invalid: %w", err)
			}
			discoveryURL, _ := url.Parse(v)
			if discoveryURL == nil || strings.TrimSpace(discoveryURL.Hostname()) == "" || discoveryURL.User != nil || (strings.EqualFold(issuerURL.Scheme, "https") && !strings.EqualFold(discoveryURL.Scheme, "https")) {
				return fmt.Errorf("gateway.resource_server.discovery_url invalid: must not include userinfo or downgrade an HTTPS issuer")
			}
		}
		if v := strings.TrimSpace(rs.JWKSURL); v != "" {
			if err := ValidateAbsoluteHTTPURL(v); err != nil {
				return fmt.Errorf("gateway.resource_server.jwks_url invalid: %w", err)
			}
			jwksURL, _ := url.Parse(v)
			if jwksURL == nil || strings.TrimSpace(jwksURL.Hostname()) == "" || jwksURL.User != nil || (strings.EqualFold(issuerURL.Scheme, "https") && !strings.EqualFold(jwksURL.Scheme, "https")) {
				return fmt.Errorf("gateway.resource_server.jwks_url invalid: must not include userinfo or downgrade an HTTPS issuer")
			}
		}
		if rs.ClockSkewSeconds < 0 || rs.ClockSkewSeconds > 600 {
			return fmt.Errorf("gateway.resource_server.clock_skew_seconds must be between 0 and 600")
		}
		if rs.JWKSCacheTTLSeconds <= 0 {
			return fmt.Errorf("gateway.resource_server.jwks_cache_ttl_seconds must be positive")
		}
		allowedAlgorithms := map[string]bool{"RS256": true, "ES256": true, "PS256": true}
		for _, alg := range splitConfigList(rs.AllowedSigningAlgs) {
			if !allowedAlgorithms[alg] {
				return fmt.Errorf("gateway.resource_server.allowed_signing_algs contains unsupported algorithm %q", alg)
			}
		}
		if strings.TrimSpace(rs.TenantClaim) == "" && rs.RequireTenant {
			return fmt.Errorf("gateway.resource_server.tenant_claim is required when require_tenant=true")
		}
		if strings.TrimSpace(rs.TokenExchange.ActorClaim) == "" {
			return fmt.Errorf("gateway.resource_server.token_exchange.actor_claim is required")
		}
		if rs.TokenExchange.MaxDelegationDepth <= 0 || rs.TokenExchange.MaxDelegationDepth > 8 {
			return fmt.Errorf("gateway.resource_server.token_exchange.max_delegation_depth must be between 1 and 8")
		}
		if rs.TokenExchange.RequireActor && len(splitConfigList(rs.TokenExchange.AllowedActorClientIDs)) == 0 {
			return fmt.Errorf("gateway.resource_server.token_exchange.allowed_actor_client_ids is required when require_actor=true")
		}
	}
	return nil
}
