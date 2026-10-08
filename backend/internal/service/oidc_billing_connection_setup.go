package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const tabroBillingProducerClientID = "tabro-api-gateway"

var oidcBillingSetupKeys = []string{
	SettingKeyOIDCOnlyEnabled, SettingKeyOIDCConnectEnabled,
	SettingKeyOIDCConnectIssuerURL, SettingKeyOIDCConnectDiscoveryURL,
	SettingKeyOIDCBillingConnection,
}

// SetupOIDCBillingConnection applies the Tabro trust template only after a live
// service-credential check. The login client ID and global billing switch are
// never changed. No request or response token is persisted.
func (s *SettingService) SetupOIDCBillingConnection(ctx context.Context, secret string) (*OIDCBillingConnectionSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, fmt.Errorf("OIDC billing settings unavailable")
	}
	values, err := s.settingRepo.GetMultiple(ctx, oidcBillingSetupKeys)
	if err != nil {
		return nil, fmt.Errorf("read OIDC billing setup settings: %w", err)
	}
	issuer, discoveryURL, err := s.oidcBillingSetupAuthority(values)
	if err != nil {
		return nil, err
	}
	previous, _, err := s.oidcBillingConnectionFromValues(values)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(secret) == "" {
		// An empty input may only reuse credentials for this exact authority and
		// dedicated producer; it must never send another issuer's secret.
		if previous.BillingCenter.ProducerClientID == tabroBillingProducerClientID &&
			strings.TrimRight(previous.BillingCenter.BaseURL, "/") == strings.TrimRight(issuer, "/") &&
			previous.ResourceServer.IssuerURL == issuer {
			secret = previous.BillingCenter.ClientSecret
		}
	}
	if strings.TrimSpace(secret) == "" {
		return nil, infraerrors.BadRequest("OIDC_BILLING_SETUP_SECRET_REQUIRED", "A dedicated Auth billing secret is required")
	}
	// Bound the combined metadata and token checks, including slow response bodies.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tokenURL, err := s.discoverOIDCBillingTokenURL(ctx, issuer, discoveryURL)
	if err != nil {
		return nil, err
	}
	connection := tabroOIDCBillingConnection(issuer, discoveryURL, tokenURL, secret)
	// Preserve explicit application allowlists on secret rotation. The login
	// client is not a model application and is never copied into these lists.
	if previous.ResourceServer.Enabled && previous.ResourceServer.IssuerURL == issuer {
		if v := strings.TrimSpace(previous.ResourceServer.AllowedClientIDs); v != "" {
			connection.ResourceServer.AllowedClientIDs = v
		}
		if v := strings.TrimSpace(previous.ResourceServer.TokenExchange.AllowedActorClientIDs); v != "" {
			connection.ResourceServer.TokenExchange.AllowedActorClientIDs = v
		}
	}
	connection.BillingCenter.Payments = s.baselineOIDCBillingConnectionConfig().BillingCenter.Payments
	if err := s.validateOIDCBillingConnectionConfig(connection); err != nil {
		return nil, err
	}
	tokens, err := billingcenter.NewClientCredentialsTokenSource(billingcenter.TokenConfig{
		TokenURL: tokenURL, ClientID: tabroBillingProducerClientID, ClientSecret: secret,
		Scopes:  []string{"billing.reserve", "billing.dispatch", "billing.extend", "billing.settle", "billing.release", "billing.read"},
		Timeout: 15 * time.Second,
	}, s.oidcBillingSetupTransport)
	if err == nil {
		_, err = tokens.Token(ctx)
	}
	if err != nil {
		// Remote error bodies can contain credentials; never return or log them.
		return nil, infraerrors.BadRequest("OIDC_BILLING_SETUP_CREDENTIALS_REJECTED", "Auth rejected the dedicated billing credentials or permissions")
	}
	return s.updateOIDCBillingConnectionConfig(ctx, connection, func(current map[string]string) error {
		// Network probing runs outside the reservation lock. Recheck under the
		// shared settings lock before committing so concurrent edits cannot save
		// a connection validated against an obsolete login authority or secret.
		for _, key := range oidcBillingSetupKeys {
			if current[key] != values[key] {
				return infraerrors.BadRequest("OIDC_BILLING_SETUP_CONFIG_CHANGED", "OIDC settings changed during verification; retry the setup")
			}
		}
		return nil
	})
}

func (s *SettingService) oidcBillingSetupAuthority(values map[string]string) (string, string, error) {
	enabled := s.cfg != nil && s.cfg.OIDC.Enabled
	if raw, ok := values[SettingKeyOIDCConnectEnabled]; ok {
		enabled = raw == "true"
	}
	if values[SettingKeyOIDCOnlyEnabled] != "true" || !enabled {
		return "", "", infraerrors.BadRequest("OIDC_BILLING_SETUP_REQUIRES_OIDC", "Save and enable OIDC-only login before configuring Auth billing")
	}
	issuer := strings.TrimSpace(values[SettingKeyOIDCConnectIssuerURL])
	discovery := strings.TrimSpace(values[SettingKeyOIDCConnectDiscoveryURL])
	if s.cfg != nil {
		if issuer == "" {
			issuer = strings.TrimSpace(s.cfg.OIDC.IssuerURL)
		}
		if discovery == "" {
			discovery = strings.TrimSpace(s.cfg.OIDC.DiscoveryURL)
		}
	}
	authority, ok := oidcBillingSetupHTTPSURL(issuer)
	if !ok {
		return "", "", infraerrors.BadRequest("OIDC_BILLING_SETUP_INVALID_ISSUER", "Auth issuer must be an HTTPS URL without credentials, query or fragment")
	}
	if discovery == "" {
		discovery = strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	}
	if !oidcBillingSetupSameOrigin(authority, discovery) {
		return "", "", infraerrors.BadRequest("OIDC_BILLING_SETUP_UNTRUSTED_ENDPOINT", "OIDC metadata and token endpoints must belong to the saved Auth authority")
	}
	return issuer, discovery, nil
}

func oidcBillingSetupHTTPSURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	return u, err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}

func oidcBillingSetupSameOrigin(authority *url.URL, endpoint string) bool {
	u, ok := oidcBillingSetupHTTPSURL(endpoint)
	return ok && strings.EqualFold(u.Host, authority.Host)
}

func (s *SettingService) discoverOIDCBillingTokenURL(ctx context.Context, issuer, discoveryURL string) (string, error) {
	failure := func() error {
		return infraerrors.BadRequest("OIDC_BILLING_SETUP_DISCOVERY_FAILED", "Unable to read the saved Auth OIDC metadata")
	}
	transport := s.oidcBillingSetupTransport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", failure()
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return "", failure()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", failure()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	var metadata struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err != nil || len(body) > 65536 || json.Unmarshal(body, &metadata) != nil {
		return "", failure()
	}
	authority, _ := oidcBillingSetupHTTPSURL(issuer)
	if metadata.Issuer != issuer || !oidcBillingSetupSameOrigin(authority, metadata.TokenEndpoint) {
		return "", infraerrors.BadRequest("OIDC_BILLING_SETUP_UNTRUSTED_ENDPOINT", "OIDC metadata does not match the saved Auth authority")
	}
	return metadata.TokenEndpoint, nil
}

func tabroOIDCBillingConnection(issuer, discovery, token, secret string) OIDCBillingConnectionConfig {
	return OIDCBillingConnectionConfig{
		BillingCenter: config.BillingCenterConfig{Enabled: true, BaseURL: issuer, TokenURL: token,
			ProducerClientID: tabroBillingProducerClientID, ClientSecret: secret, TimeoutSeconds: 15},
		ResourceServer: config.GatewayResourceServerConfig{
			Enabled: true, IssuerURL: issuer, DiscoveryURL: discovery, Audience: "tabro-llm",
			RequiredScopes: "llm.invoke", AllowedClientIDs: "tabro-agent", AllowedSigningAlgs: "RS256,ES256,PS256",
			ClockSkewSeconds: 120, JWKSCacheTTLSeconds: 300, TenantClaim: "tenant_id", RequireTenant: true,
			TokenExchange: config.GatewayTokenExchangeConfig{RequireActor: true, ActorClaim: "act", AllowedActorClientIDs: "tabro-agent", MaxDelegationDepth: 1},
		},
	}
}
