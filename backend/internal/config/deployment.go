package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type DeploymentConfig struct {
	InternalOnly     bool   `mapstructure:"internal_only"`
	AccountCenterURL string `mapstructure:"account_center_url"`
}

func (c DeploymentConfig) Validate() error {
	if !c.InternalOnly && strings.TrimSpace(c.AccountCenterURL) == "" {
		return nil
	}
	target, err := url.Parse(c.AccountCenterURL)
	if err != nil || target.Hostname() == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return fmt.Errorf("deployment.account_center_url must be an absolute account center URL without credentials, query or fragment")
	}
	loopback := target.Hostname() == "localhost"
	if ip := net.ParseIP(target.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && loopback) {
		return fmt.Errorf("deployment.account_center_url requires HTTPS (HTTP is allowed only for loopback tests)")
	}
	return nil
}
