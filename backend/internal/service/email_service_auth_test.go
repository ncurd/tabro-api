//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildSMTPAuth_Plain(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: SMTPAuthProtocolPlain,
	})

	require.NoError(t, err)
	require.NotNil(t, auth)
}

func TestBuildSMTPAuth_RejectsUnsupportedProtocol(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: "login",
	})

	require.ErrorContains(t, err, "unsupported smtp auth protocol")
	require.Nil(t, auth)
}

func TestBuildSMTPAuth_EmptyCredentialsDisablesAuth(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		AuthProtocol: SMTPAuthProtocolPlain,
	})

	require.NoError(t, err)
	require.Nil(t, auth)
}
