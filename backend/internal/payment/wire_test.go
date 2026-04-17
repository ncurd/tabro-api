package payment

import (
	"encoding/hex"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProvideEncryptionKeyUsesPaymentEncryptionKey(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payment: config.PaymentSecurityConfig{
			EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		Totp: config.TotpConfig{
			EncryptionKey: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
	}

	key, err := ProvideEncryptionKey(cfg)
	require.NoError(t, err)
	expected, err := hex.DecodeString(cfg.Payment.EncryptionKey)
	require.NoError(t, err)
	require.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", cfg.Payment.EncryptionKey)
	require.Equal(t, EncryptionKey(expected), key)
}
