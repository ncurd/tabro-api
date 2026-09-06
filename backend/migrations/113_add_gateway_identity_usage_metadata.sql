-- Persist the verified OAuth identity and Tabro correlation metadata alongside
-- the provider-reported usage record. Correlation headers are not identity or
-- billing authorities; oidc_issuer/oidc_subject come only from verified claims.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS oidc_issuer TEXT,
    ADD COLUMN IF NOT EXISTS oidc_subject TEXT,
    ADD COLUMN IF NOT EXISTS oidc_tenant TEXT,
    ADD COLUMN IF NOT EXISTS tabro_run_id TEXT,
    ADD COLUMN IF NOT EXISTS tabro_project_id TEXT;

COMMENT ON COLUMN usage_logs.oidc_issuer IS
    'Exact issuer of the verified OAuth access token';
COMMENT ON COLUMN usage_logs.oidc_subject IS
    'Stable subject of the verified OAuth access token';
COMMENT ON COLUMN usage_logs.oidc_tenant IS
    'Tenant derived from the verified OAuth access token';
COMMENT ON COLUMN usage_logs.tabro_run_id IS
    'Untrusted Tabro run correlation identifier';
COMMENT ON COLUMN usage_logs.tabro_project_id IS
    'Untrusted Tabro project correlation identifier';
