-- Provider request IDs are the primary join key for billing reconciliation.
-- Build this as a partial concurrent index so production writes are not
-- blocked and rows without a provider-assigned identifier do not bloat it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gateway_usage_ledger_upstream_request
    ON gateway_usage_ledger (upstream_request_id)
    WHERE upstream_request_id IS NOT NULL;
