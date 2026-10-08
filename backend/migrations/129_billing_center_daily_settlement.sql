-- Freeze each request's daily settlement cutoff independently of later
-- configuration changes. NULL retains immediate delivery for existing work.
ALTER TABLE billing_center_operations
 ADD COLUMN IF NOT EXISTS settlement_not_before TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS gateway_pricing_snapshot JSONB;

COMMENT ON COLUMN billing_center_operations.settlement_not_before IS
 'Immutable daily settlement cutoff captured before Auth preauthorization; releases are never deferred';

COMMENT ON COLUMN billing_center_operations.gateway_pricing_snapshot IS
 'Frozen gateway detailed price and OIDC billing multiplier; contains no credentials';
