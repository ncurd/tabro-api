-- Keep the provider-assigned request identifier separately from the stable
-- gateway billing/dedup request_id. Both are needed for incident
-- reconciliation and must never overwrite each other.
ALTER TABLE gateway_usage_ledger
    ADD COLUMN IF NOT EXISTS upstream_request_id TEXT;

COMMENT ON COLUMN gateway_usage_ledger.upstream_request_id IS
    'Provider-assigned request identifier retained separately from the gateway billing dedup key';
