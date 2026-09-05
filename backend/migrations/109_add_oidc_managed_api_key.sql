-- Mark the persistent API-key records used only as OIDC token billing
-- identities. A dedicated column keeps the security decision independent of
-- any configurable API-key string prefix.
ALTER TABLE api_keys
ADD COLUMN IF NOT EXISTS oidc_managed BOOLEAN NOT NULL DEFAULT FALSE;
