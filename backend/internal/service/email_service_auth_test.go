//go:build unit

package service

import (
	"net/smtp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildSMTPAuth_Plain(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: SMTPAuthProtocolPlain,
	}, "")

	require.NoError(t, err)
	require.NotNil(t, auth)
}

func TestBuildSMTPAuth_Login(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: SMTPAuthProtocolLogin,
	}, "")

	require.NoError(t, err)
	require.NotNil(t, auth)
}

func TestBuildSMTPAuth_AutoPrefersLoginWhenPlainUnavailable(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: SMTPAuthProtocolAuto,
	}, "LOGIN XOAUTH2")

	require.NoError(t, err)
	require.NotNil(t, auth)

	proto, _, err := auth.Start(&smtp.ServerInfo{
		Name: "smtp.example.com",
		TLS:  true,
		Auth: []string{"LOGIN", "XOAUTH2"},
	})
	require.NoError(t, err)
	require.Equal(t, "LOGIN", proto)
}

func TestBuildSMTPAuth_AutoPrefersPlainWhenAvailable(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: SMTPAuthProtocolAuto,
	}, "PLAIN LOGIN")

	require.NoError(t, err)
	require.NotNil(t, auth)

	proto, _, err := auth.Start(&smtp.ServerInfo{
		Name: "smtp.example.com",
		TLS:  true,
		Auth: []string{"PLAIN", "LOGIN"},
	})
	require.NoError(t, err)
	require.Equal(t, "PLAIN", proto)
}

func TestBuildSMTPAuth_RejectsUnsupportedProtocol(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		Username:     "user",
		Password:     "secret",
		AuthProtocol: "cram-md5",
	}, "")

	require.ErrorContains(t, err, "unsupported smtp auth protocol")
	require.Nil(t, auth)
}

func TestBuildSMTPAuth_EmptyCredentialsDisablesAuth(t *testing.T) {
	auth, err := buildSMTPAuth(&SMTPConfig{
		Host:         "smtp.example.com",
		AuthProtocol: SMTPAuthProtocolPlain,
	}, "")

	require.NoError(t, err)
	require.Nil(t, auth)
}
