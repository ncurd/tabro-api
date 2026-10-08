package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultOIDCBillingRateMultiplier     = 1.0
	DefaultOIDCBillingSettlementTime     = "00:00"
	DefaultOIDCBillingSettlementTimezone = "Asia/Shanghai"
)

// OIDCBillingPolicy is snapshotted for each operation. Changes only affect new
// reservations; pending reservations retain their original amount and due time.
type OIDCBillingPolicy struct {
	Enabled            bool
	RateMultiplier     float64
	SettlementTime     string
	SettlementTimezone string
}

func defaultOIDCBillingPolicy() OIDCBillingPolicy {
	return OIDCBillingPolicy{RateMultiplier: DefaultOIDCBillingRateMultiplier, SettlementTime: DefaultOIDCBillingSettlementTime, SettlementTimezone: DefaultOIDCBillingSettlementTimezone}
}

func (p OIDCBillingPolicy) Validate() error {
	if math.IsNaN(p.RateMultiplier) || math.IsInf(p.RateMultiplier, 0) || p.RateMultiplier <= 0 {
		return infraerrors.BadRequest("OIDC_BILLING_INVALID_MULTIPLIER", "OIDC billing rate multiplier must be finite and greater than zero")
	}
	parsed, err := time.Parse("15:04", p.SettlementTime)
	if err != nil || parsed.Format("15:04") != p.SettlementTime {
		return infraerrors.BadRequest("OIDC_BILLING_INVALID_SETTLEMENT_TIME", "OIDC billing settlement time must use HH:MM (00:00 to 23:59)")
	}
	if p.SettlementTimezone == "" || p.SettlementTimezone == "Local" {
		return infraerrors.BadRequest("OIDC_BILLING_INVALID_TIMEZONE", "OIDC billing settlement timezone must be an explicit IANA timezone")
	}
	if _, err := time.LoadLocation(p.SettlementTimezone); err != nil {
		return infraerrors.BadRequest("OIDC_BILLING_INVALID_TIMEZONE", "OIDC billing settlement timezone must be an explicit IANA timezone")
	}
	return nil
}

// NextSettlementAt returns the next daily boundary strictly after the supplied
// instant. Calendar arithmetic keeps the configured local time across DST.
func (p OIDCBillingPolicy) NextSettlementAt(at time.Time) (time.Time, error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, err
	}
	location, _ := time.LoadLocation(p.SettlementTimezone)
	clock, _ := time.Parse("15:04", p.SettlementTime)
	local := at.In(location)
	boundary := func(day int) time.Time {
		due := time.Date(local.Year(), local.Month(), day, clock.Hour(), clock.Minute(), 0, 0, location)
		// A nonexistent wall time during a spring-forward transition moves
		// forward by the skipped interval instead of settling prematurely.
		wantedMinute := clock.Hour()*60 + clock.Minute()
		actualMinute := due.Hour()*60 + due.Minute()
		if actualMinute < wantedMinute {
			due = due.Add(time.Duration(wantedMinute-actualMinute) * time.Minute)
		}
		return due
	}
	due := boundary(local.Day())
	if !due.After(at) {
		due = boundary(local.Day() + 1)
	}
	return due.UTC(), nil
}

// IsOIDCBillingSupported describes the configured connector capability, not a
// live probe or an assertion about the billing authority's availability.
func (s *SettingService) IsOIDCBillingSupported() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := s.GetOIDCBillingConnectionConfig(ctx)
	return err == nil && oidcBillingConnectionSupported(connection)
}

func oidcBillingPolicyFromSettings(values map[string]string) (OIDCBillingPolicy, error) {
	p := defaultOIDCBillingPolicy()
	p.Enabled = values[SettingKeyOIDCBillingEnabled] == "true"
	if raw, ok := values[SettingKeyOIDCBillingRateMultiplier]; ok {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return p, infraerrors.BadRequest("OIDC_BILLING_INVALID_MULTIPLIER", "OIDC billing rate multiplier must be finite and greater than zero")
		}
		p.RateMultiplier = value
	}
	if raw, ok := values[SettingKeyOIDCBillingSettlementTime]; ok {
		p.SettlementTime = raw
	}
	if raw, ok := values[SettingKeyOIDCBillingSettlementTimezone]; ok {
		p.SettlementTimezone = raw
	}
	return p, p.Validate()
}

func (s *SettingService) validateOIDCBillingPrerequisites(ctx context.Context, p OIDCBillingPolicy, oidcOnly, oidcEnabled bool, issuer string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !p.Enabled {
		return nil
	}
	if !oidcOnly || !oidcEnabled {
		return infraerrors.BadRequest("OIDC_BILLING_REQUIRES_ONLY_MODE", "OIDC billing requires OIDC-only mode and OIDC login to remain enabled")
	}
	connection, err := s.GetOIDCBillingConnectionConfig(ctx)
	if err != nil {
		return err
	}
	if !oidcBillingConnectionSupported(connection) {
		return infraerrors.BadRequest("OIDC_BILLING_NOT_SUPPORTED", "OIDC billing requires a valid enabled billing-center connector and gateway OIDC resource server")
	}
	if strings.TrimSpace(issuer) != strings.TrimSpace(connection.ResourceServer.IssuerURL) {
		return infraerrors.BadRequest("OIDC_BILLING_ISSUER_MISMATCH", "OIDC login and gateway billing must use the same issuer")
	}
	return nil
}

// GetOIDCBillingPolicy fails closed if an enabled policy loses its prerequisites.
// Callers must propagate errors instead of falling back to local balances.
func (s *SettingService) GetOIDCBillingPolicy(ctx context.Context) (OIDCBillingPolicy, error) {
	p := defaultOIDCBillingPolicy()
	if s == nil {
		return p, nil
	}
	if s.settingRepo == nil {
		return p, fmt.Errorf("OIDC billing settings unavailable")
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOIDCBillingEnabled, SettingKeyOIDCBillingRateMultiplier, SettingKeyOIDCBillingSettlementTime, SettingKeyOIDCBillingSettlementTimezone, SettingKeyOIDCOnlyEnabled, SettingKeyOIDCConnectEnabled, SettingKeyOIDCConnectIssuerURL})
	if err != nil {
		return p, fmt.Errorf("get OIDC billing settings: %w", err)
	}
	p, err = oidcBillingPolicyFromSettings(values)
	if err != nil {
		return p, err
	}
	oidcEnabled := s.cfg != nil && s.cfg.OIDC.Enabled
	issuer := ""
	if s.cfg != nil {
		issuer = s.cfg.OIDC.IssuerURL
	}
	if raw, ok := values[SettingKeyOIDCConnectEnabled]; ok {
		oidcEnabled = raw == "true"
	}
	if raw := strings.TrimSpace(values[SettingKeyOIDCConnectIssuerURL]); raw != "" {
		issuer = raw
	}
	return p, s.validateOIDCBillingPrerequisites(ctx, p, values[SettingKeyOIDCOnlyEnabled] == "true", oidcEnabled, issuer)
}

// IsOIDCBillingEnabled is for guarding local financial features. A read failure
// must never reopen local charging, credits, subscriptions, or vouchers.
func (s *SettingService) IsOIDCBillingEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOIDCBillingEnabled)
	if err != nil {
		return !errors.Is(err, ErrSettingNotFound)
	}
	return value == "true"
}

func (s *SettingService) normalizeOIDCBillingSettings(ctx context.Context, settings *SystemSettings) error {
	// Existing callers that predate these fields provide zero values while off.
	if settings.OIDCBillingRateMultiplier == 0 && !settings.OIDCBillingEnabled {
		settings.OIDCBillingRateMultiplier = DefaultOIDCBillingRateMultiplier
	}
	if settings.OIDCBillingSettlementTime == "" {
		settings.OIDCBillingSettlementTime = DefaultOIDCBillingSettlementTime
	}
	if settings.OIDCBillingSettlementTimezone == "" {
		settings.OIDCBillingSettlementTimezone = DefaultOIDCBillingSettlementTimezone
	}
	p := OIDCBillingPolicy{Enabled: settings.OIDCBillingEnabled, RateMultiplier: settings.OIDCBillingRateMultiplier, SettlementTime: settings.OIDCBillingSettlementTime, SettlementTimezone: settings.OIDCBillingSettlementTimezone}
	issuer := settings.OIDCConnectIssuerURL
	if strings.TrimSpace(issuer) == "" && s.cfg != nil {
		issuer = s.cfg.OIDC.IssuerURL
	}
	return s.validateOIDCBillingPrerequisites(ctx, p, settings.OIDCOnlyEnabled, settings.OIDCConnectEnabled, issuer)
}
