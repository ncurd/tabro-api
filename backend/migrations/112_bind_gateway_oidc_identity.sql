-- Bind a verified external OAuth subject to the internal managed API key used
-- for routing and billing. Email/username are intentionally not part of this
-- mapping because they are mutable and are not stable security identifiers.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS oidc_issuer VARCHAR(512),
    ADD COLUMN IF NOT EXISTS oidc_subject VARCHAR(512);

-- A half-bound identity must never be queryable or silently reused. Keep the
-- pair atomic at the database boundary as well as in the service API.
ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS api_keys_oidc_identity_pair_check;
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_oidc_identity_pair_check CHECK (
        (oidc_issuer IS NULL AND oidc_subject IS NULL)
        OR
        (oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL)
    );

CREATE INDEX IF NOT EXISTS idx_api_keys_oidc_identity
    ON api_keys (oidc_issuer, oidc_subject);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_oidc_identity_active_unique
    ON api_keys (oidc_issuer, oidc_subject)
    WHERE deleted_at IS NULL
      AND oidc_issuer IS NOT NULL
      AND oidc_subject IS NOT NULL;

COMMENT ON COLUMN api_keys.oidc_issuer IS
    'Exact iss claim of the verified external OAuth access token';
COMMENT ON COLUMN api_keys.oidc_subject IS
    'Stable sub claim of the verified external OAuth access token';
