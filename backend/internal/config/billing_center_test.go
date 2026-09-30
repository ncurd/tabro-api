package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBillingCenterConnectorValidation(t *testing.T) {
	valid := BillingCenterConfig{Enabled: true, BaseURL: "https://auth.example", TokenURL: "https://auth.example/connect/token", ProducerClientID: "gateway-billing", ClientSecret: "test-only", TimeoutSeconds: 15}
	require.NoError(t, valid.Validate())
	for _, mutate := range []func(*BillingCenterConfig){func(c *BillingCenterConfig) { c.ClientSecret = "" }, func(c *BillingCenterConfig) { c.BaseURL = "http://auth.example" }, func(c *BillingCenterConfig) { c.TokenURL = "https://user:password@auth.example/connect/token" }, func(c *BillingCenterConfig) { c.TimeoutSeconds = 120 }, func(c *BillingCenterConfig) { c.BaseURL = "https://auth.example?redirect=elsewhere" }} {
		cfg := valid
		mutate(&cfg)
		require.Error(t, cfg.Validate())
	}
	local := valid
	local.BaseURL = "http://127.0.0.1:39999"
	local.TokenURL = local.BaseURL + "/connect/token"
	require.Error(t, local.Validate())
	local.InsecureLocal = true
	require.NoError(t, local.Validate())
	require.NoError(t, (BillingCenterConfig{}).Validate())
}
