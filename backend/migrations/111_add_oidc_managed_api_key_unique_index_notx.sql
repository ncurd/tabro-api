-- Multiple application instances may process one user's first OIDC login at
-- the same time. Keep all resulting tokens on one active billing identity;
-- soft-deleting it permits a replacement on the next OIDC login.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_api_keys_oidc_managed_user
ON api_keys (user_id)
WHERE deleted_at IS NULL AND oidc_managed = TRUE;
