-- Automatically provisioned OAuth identities must never debit local balance.
ALTER TABLE api_keys
ADD COLUMN IF NOT EXISTS auth_billing_only BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN api_keys.auth_billing_only IS
  'Automatically provisioned OIDC identity whose billing authority is Auth only';
