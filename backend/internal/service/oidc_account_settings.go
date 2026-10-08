package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// OIDCAccountConfiguration contains only the public identity-provider details
// needed to compare the browser's Auth session with its verified API login.
// No service credentials or caller-supplied user/workspace assertions are used.
type OIDCAccountConfiguration struct {
	Enabled  bool
	Issuer   string
	ClientID string
}

func (s *SettingService) GetOIDCAccountConfiguration(ctx context.Context) (OIDCAccountConfiguration, error) {
	var result OIDCAccountConfiguration
	policy, err := s.GetOIDCBillingPolicy(ctx)
	if err != nil || !policy.Enabled {
		return result, err
	}
	connection, err := s.GetOIDCBillingConnectionConfig(ctx)
	if err != nil {
		return result, err
	}
	if err := s.validateOIDCBillingConnectionConfig(connection); err != nil {
		return result, err
	}
	if !connection.BillingCenter.Enabled || !connection.ResourceServer.Enabled {
		return result, fmt.Errorf("OIDC billing connection is unavailable")
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyOIDCConnectClientID, SettingKeyOIDCConnectIssuerURL,
		SettingKeyOIDCConnectEnabled, SettingKeyOIDCOnlyEnabled,
	})
	if err != nil {
		return result, fmt.Errorf("read OIDC account configuration: %w", err)
	}
	issuer, clientID, loginEnabled := "", "", false
	if s.cfg != nil {
		issuer, clientID, loginEnabled = s.cfg.OIDC.IssuerURL, s.cfg.OIDC.ClientID, s.cfg.OIDC.Enabled
	}
	if value := strings.TrimSpace(values[SettingKeyOIDCConnectIssuerURL]); value != "" {
		issuer = value
	}
	if value := strings.TrimSpace(values[SettingKeyOIDCConnectClientID]); value != "" {
		clientID = value
	}
	if value, ok := values[SettingKeyOIDCConnectEnabled]; ok {
		loginEnabled = value == "true"
	}
	issuer, clientID = strings.TrimSpace(issuer), strings.TrimSpace(clientID)
	if !loginEnabled || values[SettingKeyOIDCOnlyEnabled] != "true" ||
		issuer != strings.TrimSpace(connection.ResourceServer.IssuerURL) {
		return result, fmt.Errorf("OIDC login and billing configuration no longer match")
	}
	provider, err := url.Parse(issuer)
	if err != nil || provider.Scheme != "https" || provider.Hostname() == "" ||
		provider.User != nil || provider.RawQuery != "" || provider.ForceQuery || provider.Fragment != "" || provider.Opaque != "" {
		return result, fmt.Errorf("OIDC account issuer must be a trusted HTTPS URL")
	}
	if clientID == "" || len(clientID) > 512 || strings.IndexFunc(clientID, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return result, fmt.Errorf("OIDC login client ID is unavailable")
	}
	return OIDCAccountConfiguration{Enabled: true, Issuer: issuer, ClientID: clientID}, nil
}
