//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSMTPAuthProtocolInput_DefaultsToAuto(t *testing.T) {
	protocol, err := normalizeSMTPAuthProtocolInput("", "")
	require.NoError(t, err)
	require.Equal(t, service.SMTPAuthProtocolAuto, protocol)
}

func TestNormalizeSMTPAuthProtocolInput_AcceptsLogin(t *testing.T) {
	protocol, err := normalizeSMTPAuthProtocolInput("login")
	require.NoError(t, err)
	require.Equal(t, service.SMTPAuthProtocolLogin, protocol)
}

func TestNormalizeSMTPAuthProtocolInput_RejectsUnsupportedProtocol(t *testing.T) {
	_, err := normalizeSMTPAuthProtocolInput("xoauth2")
	require.ErrorContains(t, err, "SMTP auth protocol must be one of")
}
