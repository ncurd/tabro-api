-- Preserve historical key isolation; public access is explicit for new keys.
ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS group_scope VARCHAR(20) NOT NULL DEFAULT 'single',
  ADD COLUMN IF NOT EXISTS group_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE api_keys ADD CONSTRAINT api_keys_group_scope_check
  CHECK (group_scope IN ('single', 'public', 'selected'));

-- Technical first-use Auth identities had an automatically chosen group, not
-- a user-selected restriction. Ordinary and manually managed keys stay bounded.
UPDATE api_keys SET group_scope = 'public', group_id = NULL
WHERE auth_billing_only = TRUE AND oidc_managed = TRUE;

COMMENT ON COLUMN api_keys.group_scope IS 'single legacy group, public active standard groups, or selected group IDs';
COMMENT ON COLUMN api_keys.group_ids IS 'Allowed group IDs when group_scope is selected';
