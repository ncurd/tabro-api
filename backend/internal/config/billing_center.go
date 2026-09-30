package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// BillingCenterConfig enables the connector, never changes an account's mode.
// Account ownership/mode remains a database decision even when disabled.
type BillingCenterConfig struct {
	Enabled          bool                        `mapstructure:"enabled"`
	BaseURL          string                      `mapstructure:"base_url"`
	TokenURL         string                      `mapstructure:"token_url"`
	ProducerClientID string                      `mapstructure:"producer_client_id"`
	ClientSecret     string                      `mapstructure:"client_secret"`
	TimeoutSeconds   int                         `mapstructure:"timeout_seconds"`
	InsecureLocal    bool                        `mapstructure:"insecure_local"`
	Payments         BillingCenterPaymentsConfig `mapstructure:"payments"`
}

// Payment adapters use a separate least-privileged billing.credit client.
type BillingCenterPaymentsConfig struct {
	Enabled              bool   `mapstructure:"enabled"`
	ProducerClientID     string `mapstructure:"producer_client_id"`
	ClientSecret         string `mapstructure:"client_secret"`
	CallbackBaseURL      string `mapstructure:"callback_base_url"`
	AccountCenterBaseURL string `mapstructure:"account_center_base_url"`
}

func (c BillingCenterConfig) Validate() error {
	if c.Payments.Enabled && !c.Enabled {
		return fmt.Errorf("central payments require billing_center.enabled")
	}
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.ProducerClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" {
		return fmt.Errorf("billing_center producer_client_id and client_secret are required")
	}
	for _, raw := range []string{c.BaseURL, c.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid billing_center endpoint")
		}
		ip := net.ParseIP(u.Hostname())
		local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
		if u.Scheme != "https" && !(u.Scheme == "http" && c.InsecureLocal && local) {
			return fmt.Errorf("billing_center endpoints require HTTPS")
		}
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 60 {
		return fmt.Errorf("billing_center.timeout_seconds must be between 0 and 60")
	}
	if c.Payments.Enabled {
		if c.Payments.ProducerClientID == "" || c.Payments.ClientSecret == "" || c.Payments.ProducerClientID == c.ProducerClientID {
			return fmt.Errorf("central payments require a separate payment producer client and secret")
		}
		for _, raw := range []string{c.Payments.CallbackBaseURL, c.Payments.AccountCenterBaseURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("invalid central payment public endpoint")
			}
			ip := net.ParseIP(u.Hostname())
			local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
			if u.Scheme != "https" && !(u.Scheme == "http" && c.InsecureLocal && local) {
				return fmt.Errorf("central payment public endpoints require HTTPS")
			}
		}
	}
	return nil
}
