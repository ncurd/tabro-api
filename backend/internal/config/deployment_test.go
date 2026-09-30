package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestInternalOnlyDeploymentRequiresTrustedAccountCenter(t *testing.T) {
	require.NoError(t, (DeploymentConfig{}).Validate())
	for _, target := range []string{"", "//auth.example/account", "javascript:alert(1)", "http://auth.example/account", "https://user:secret@auth.example/account", "https://auth.example/account?token=secret", "https://auth.example/#account"} {
		require.Error(t, (DeploymentConfig{InternalOnly: true, AccountCenterURL: target}).Validate(), target)
	}
	for _, target := range []string{"https://auth.example/account", "http://127.0.0.1:9000/account", "http://[::1]:9000/account"} {
		require.NoError(t, (DeploymentConfig{InternalOnly: true, AccountCenterURL: target}).Validate())
	}
}
