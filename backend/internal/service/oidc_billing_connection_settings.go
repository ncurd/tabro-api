package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OIDCBillingConnectionConfig is an immutable runtime snapshot. The shared
// startup config is never modified when an administrator saves a connection.
type OIDCBillingConnectionConfig struct {
	BillingCenter  config.BillingCenterConfig         `json:"billing_center"`
	ResourceServer config.GatewayResourceServerConfig `json:"resource_server"`
}

// OIDCBillingConnectionSettings is safe to return only to administrators.
// Secrets are removed; the configured flag lets an empty input preserve them.
type OIDCBillingConnectionSettings struct {
	OIDCBillingConnectionConfig
	ClientSecretConfigured bool   `json:"client_secret_configured"`
	Source                 string `json:"source"`
	OIDCBillingSupported   bool   `json:"oidc_billing_supported"`
}

func (s *SettingService) baselineOIDCBillingConnectionConfig() OIDCBillingConnectionConfig {
	if s == nil || s.cfg == nil {
		return OIDCBillingConnectionConfig{}
	}
	return OIDCBillingConnectionConfig{BillingCenter: s.cfg.BillingCenter, ResourceServer: s.cfg.Gateway.ResourceServer}
}

func (s *SettingService) oidcBillingConnectionFromValues(values map[string]string) (OIDCBillingConnectionConfig, bool, error) {
	connection := s.baselineOIDCBillingConnectionConfig()
	raw := values[SettingKeyOIDCBillingConnection]
	if raw == "" {
		return connection, false, nil
	}
	connection = OIDCBillingConnectionConfig{}
	if err := json.Unmarshal([]byte(raw), &connection); err != nil {
		return OIDCBillingConnectionConfig{}, true, fmt.Errorf("invalid saved OIDC billing connection")
	}
	// Payment adapters use separate credentials and are outside this form.
	connection.BillingCenter.Payments = s.baselineOIDCBillingConnectionConfig().BillingCenter.Payments
	if err := s.validateOIDCBillingConnectionConfig(connection); err != nil {
		return OIDCBillingConnectionConfig{}, true, err
	}
	return connection, true, nil
}

func (s *SettingService) readOIDCBillingConnection(ctx context.Context) (OIDCBillingConnectionConfig, bool, error) {
	if s == nil || s.settingRepo == nil {
		return s.baselineOIDCBillingConnectionConfig(), false, nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOIDCBillingConnection})
	if err != nil {
		return OIDCBillingConnectionConfig{}, false, fmt.Errorf("read OIDC billing connection: %w", err)
	}
	return s.oidcBillingConnectionFromValues(values)
}

// GetOIDCBillingConnectionConfig always reads the committed override. Runtime
// clients cache immutable instances by snapshot, not a mutable config pointer.
func (s *SettingService) GetOIDCBillingConnectionConfig(ctx context.Context) (OIDCBillingConnectionConfig, error) {
	connection, _, err := s.readOIDCBillingConnection(ctx)
	return connection, err
}

func (s *SettingService) HasOIDCBillingConnectionOverride(ctx context.Context) (bool, error) {
	_, overridden, err := s.readOIDCBillingConnection(ctx)
	return overridden, err
}

func oidcBillingConnectionSupported(connection OIDCBillingConnectionConfig) bool {
	return connection.BillingCenter.Enabled && connection.BillingCenter.Validate() == nil &&
		connection.ResourceServer.Enabled && config.ValidateAbsoluteHTTPURL(strings.TrimSpace(connection.ResourceServer.IssuerURL)) == nil
}

func sanitizedOIDCBillingConnection(connection OIDCBillingConnectionConfig, overridden bool) *OIDCBillingConnectionSettings {
	result := &OIDCBillingConnectionSettings{
		OIDCBillingConnectionConfig: connection,
		ClientSecretConfigured:      strings.TrimSpace(connection.BillingCenter.ClientSecret) != "",
		Source:                      "config",
		OIDCBillingSupported:        oidcBillingConnectionSupported(connection),
	}
	if overridden {
		result.Source = "database"
	}
	result.BillingCenter.ClientSecret = ""
	result.BillingCenter.Payments = config.BillingCenterPaymentsConfig{}
	return result
}

func (s *SettingService) GetOIDCBillingConnectionSettings(ctx context.Context) (*OIDCBillingConnectionSettings, error) {
	connection, overridden, err := s.readOIDCBillingConnection(ctx)
	if err != nil {
		return nil, err
	}
	return sanitizedOIDCBillingConnection(connection, overridden), nil
}

func (s *SettingService) validateOIDCBillingConnectionConfig(connection OIDCBillingConnectionConfig) error {
	if connection.BillingCenter.Enabled && s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return infraerrors.BadRequest("OIDC_BILLING_CONNECTION_INVALID", "Auth billing requires standard run mode")
	}
	if err := connection.BillingCenter.Validate(); err != nil {
		return infraerrors.BadRequest("OIDC_BILLING_CONNECTION_INVALID", err.Error())
	}
	if err := config.ValidateGatewayResourceServer(connection.ResourceServer, connection.BillingCenter.Enabled); err != nil {
		return infraerrors.BadRequest("OIDC_BILLING_CONNECTION_INVALID", err.Error())
	}
	return nil
}

// These hooks are installed once during bootstrap. The guard covers validation
// and persistence so a new reservation cannot race a connection identity change.
func (s *SettingService) SetOIDCBillingConnectionValidator(validator func(context.Context, OIDCBillingConnectionConfig, OIDCBillingConnectionConfig) error) {
	s.oidcBillingConnectionUpdateMu.Lock()
	defer s.oidcBillingConnectionUpdateMu.Unlock()
	s.oidcBillingConnectionValidator = validator
}

func (s *SettingService) SetOIDCBillingConnectionUpdateGuard(guard func() func()) {
	s.oidcBillingConnectionUpdateMu.Lock()
	defer s.oidcBillingConnectionUpdateMu.Unlock()
	s.oidcBillingConnectionUpdateGuard = guard
}

func (s *SettingService) UpdateOIDCBillingConnectionConfig(ctx context.Context, connection OIDCBillingConnectionConfig) (*OIDCBillingConnectionSettings, error) {
	return s.updateOIDCBillingConnectionConfig(ctx, connection, nil)
}

func (s *SettingService) updateOIDCBillingConnectionConfig(ctx context.Context, connection OIDCBillingConnectionConfig, precommit func(map[string]string) error) (*OIDCBillingConnectionSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, fmt.Errorf("OIDC billing settings unavailable")
	}
	s.oidcBillingConnectionUpdateMu.Lock()
	defer s.oidcBillingConnectionUpdateMu.Unlock()
	if s.oidcBillingConnectionUpdateGuard != nil {
		unlock := s.oidcBillingConnectionUpdateGuard()
		defer unlock()
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOIDCBillingConnection, SettingKeyOIDCBillingEnabled, SettingKeyOIDCConnectIssuerURL, SettingKeyOIDCConnectDiscoveryURL, SettingKeyOIDCConnectEnabled, SettingKeyOIDCOnlyEnabled})
	if err != nil {
		return nil, fmt.Errorf("read OIDC billing settings: %w", err)
	}
	if precommit != nil {
		if err := precommit(values); err != nil {
			return nil, err
		}
	}
	previous, _, err := s.oidcBillingConnectionFromValues(values)
	if err != nil {
		return nil, err
	}
	connection.BillingCenter.BaseURL = strings.TrimSpace(connection.BillingCenter.BaseURL)
	connection.BillingCenter.TokenURL = strings.TrimSpace(connection.BillingCenter.TokenURL)
	connection.BillingCenter.ProducerClientID = strings.TrimSpace(connection.BillingCenter.ProducerClientID)
	connection.ResourceServer.IssuerURL = strings.TrimSpace(connection.ResourceServer.IssuerURL)
	connection.ResourceServer.DiscoveryURL = strings.TrimSpace(connection.ResourceServer.DiscoveryURL)
	connection.ResourceServer.JWKSURL = strings.TrimSpace(connection.ResourceServer.JWKSURL)
	if strings.TrimSpace(connection.BillingCenter.ClientSecret) == "" {
		connection.BillingCenter.ClientSecret = previous.BillingCenter.ClientSecret
	}
	connection.BillingCenter.Payments = s.baselineOIDCBillingConnectionConfig().BillingCenter.Payments
	if err := s.validateOIDCBillingConnectionConfig(connection); err != nil {
		return nil, err
	}
	if values[SettingKeyOIDCBillingEnabled] == "true" {
		if !oidcBillingConnectionSupported(connection) {
			return nil, infraerrors.BadRequest("OIDC_BILLING_CONNECTION_IN_USE", "Disable OIDC billing before disabling its connection or gateway authentication")
		}
		issuer := values[SettingKeyOIDCConnectIssuerURL]
		if strings.TrimSpace(issuer) == "" && s.cfg != nil {
			issuer = s.cfg.OIDC.IssuerURL
		}
		if strings.TrimSpace(issuer) != connection.ResourceServer.IssuerURL {
			return nil, infraerrors.BadRequest("OIDC_BILLING_ISSUER_MISMATCH", "OIDC login and gateway billing must use the same issuer")
		}
	}
	if s.oidcBillingConnectionValidator != nil {
		if err := s.oidcBillingConnectionValidator(ctx, previous, connection); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(connection)
	if err != nil {
		return nil, fmt.Errorf("encode OIDC billing connection: %w", err)
	}
	if err := s.settingRepo.SetMultiple(ctx, map[string]string{SettingKeyOIDCBillingConnection: string(encoded)}); err != nil {
		return nil, fmt.Errorf("save OIDC billing connection: %w", err)
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return sanitizedOIDCBillingConnection(connection, true), nil
}
