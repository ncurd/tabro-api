package setup

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestDecideAdminBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalUsers int64
		adminUsers int64
		should     bool
		reason     string
	}{
		{
			name:       "empty database should create admin",
			totalUsers: 0,
			adminUsers: 0,
			should:     true,
			reason:     adminBootstrapReasonEmptyDatabase,
		},
		{
			name:       "admin exists should skip",
			totalUsers: 10,
			adminUsers: 1,
			should:     false,
			reason:     adminBootstrapReasonAdminExists,
		},
		{
			name:       "users exist without admin should skip",
			totalUsers: 5,
			adminUsers: 0,
			should:     false,
			reason:     adminBootstrapReasonUsersExistWithoutAdmin,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideAdminBootstrap(tc.totalUsers, tc.adminUsers)
			if got.shouldCreate != tc.should {
				t.Fatalf("shouldCreate=%v, want %v", got.shouldCreate, tc.should)
			}
			if got.reason != tc.reason {
				t.Fatalf("reason=%q, want %q", got.reason, tc.reason)
			}
		})
	}
}

func TestSetupDefaultAdminConcurrency(t *testing.T) {
	t.Run("simple mode admin uses higher concurrency", func(t *testing.T) {
		t.Setenv("RUN_MODE", "simple")
		if got := setupDefaultAdminConcurrency(); got != simpleModeAdminConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, simpleModeAdminConcurrency)
		}
	})

	t.Run("standard mode keeps existing default", func(t *testing.T) {
		t.Setenv("RUN_MODE", "standard")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})
}

func TestWriteConfigFileKeepsDefaultUserConcurrency(t *testing.T) {
	t.Setenv("RUN_MODE", "simple")
	t.Setenv("DATA_DIR", t.TempDir())

	if err := writeConfigFile(&SetupConfig{}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "user_concurrency: 5") {
		t.Fatalf("config missing default user concurrency, got:\n%s", string(data))
	}
}

func TestEnsureSetupSecretsGeneratesMissingKeys(t *testing.T) {
	cfg := &SetupConfig{}

	if err := ensureSetupSecrets(cfg); err != nil {
		t.Fatalf("ensureSetupSecrets() error = %v", err)
	}

	assertHexSecret(t, "jwt", cfg.JWT.Secret, 64)
	assertHexSecret(t, "totp", cfg.Totp.EncryptionKey, 64)
	assertHexSecret(t, "payment", cfg.Payment.EncryptionKey, 64)
}

func TestEnsureSetupSecretsPreservesConfiguredKeys(t *testing.T) {
	cfg := &SetupConfig{
		JWT: JWTConfig{
			Secret:     "jwt-secret",
			ExpireHour: 24,
		},
		Totp: TotpConfig{
			EncryptionKey: "totp-secret",
		},
		Payment: PaymentConfig{
			EncryptionKey: "payment-secret",
		},
	}

	if err := ensureSetupSecrets(cfg); err != nil {
		t.Fatalf("ensureSetupSecrets() error = %v", err)
	}

	if cfg.JWT.Secret != "jwt-secret" {
		t.Fatalf("JWT secret changed unexpectedly: %q", cfg.JWT.Secret)
	}
	if cfg.Totp.EncryptionKey != "totp-secret" {
		t.Fatalf("TOTP key changed unexpectedly: %q", cfg.Totp.EncryptionKey)
	}
	if cfg.Payment.EncryptionKey != "payment-secret" {
		t.Fatalf("payment key changed unexpectedly: %q", cfg.Payment.EncryptionKey)
	}
}

func TestWriteConfigFilePersistsSecurityEncryptionKeys(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	cfg := &SetupConfig{
		JWT: JWTConfig{
			Secret:     "jwt-secret",
			ExpireHour: 24,
		},
		Totp: TotpConfig{
			EncryptionKey: "totp-secret",
		},
		Payment: PaymentConfig{
			EncryptionKey: "payment-secret",
		},
	}

	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "totp:\n    encryption_key: totp-secret\n") {
		t.Fatalf("config missing totp encryption key, got:\n%s", content)
	}
	if !strings.Contains(content, "payment:\n    encryption_key: payment-secret\n") {
		t.Fatalf("config missing payment encryption key, got:\n%s", content)
	}
}

func assertHexSecret(t *testing.T, name, value string, expectedLength int) {
	t.Helper()

	if len(value) != expectedLength {
		t.Fatalf("%s secret length = %d, want %d", name, len(value), expectedLength)
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("%s secret is not valid hex: %v", name, err)
	}
}
