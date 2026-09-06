-- Durable, append-only gateway usage ledger.
--
-- This table is intentionally separate from usage_logs: usage_logs may be
-- written asynchronously on a best-effort path, while this ledger is inserted
-- in the same transaction as usage_billing_dedup and all billing mutations.
-- A ledger insert failure therefore rolls the charge back.
CREATE TABLE IF NOT EXISTS gateway_usage_ledger (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    request_fingerprint VARCHAR(64) NOT NULL,
    user_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    subscription_id BIGINT,

    -- oidc_* values come only from the verified access token. tabro_* values
    -- are untrusted correlation metadata and never determine the billed user.
    oidc_issuer TEXT,
    oidc_subject TEXT,
    oidc_tenant TEXT,
    tabro_run_id TEXT,
    tabro_project_id TEXT,

    model VARCHAR(100) NOT NULL,
    requested_model VARCHAR(100) NOT NULL,
    upstream_model VARCHAR(100),
    service_tier VARCHAR(50),
    reasoning_effort VARCHAR(20),
    billing_type SMALLINT NOT NULL DEFAULT 0,
    billing_mode VARCHAR(20),

    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_5m_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_1h_tokens INTEGER NOT NULL DEFAULT 0,
    image_output_tokens INTEGER NOT NULL DEFAULT 0,
    image_count INTEGER NOT NULL DEFAULT 0,
    media_type VARCHAR(20),

    input_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    output_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    cache_creation_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    cache_read_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    image_output_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    total_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    actual_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    rate_multiplier NUMERIC(10, 4) NOT NULL DEFAULT 1,
    account_rate_multiplier NUMERIC(10, 4),

    -- Exact transactional effects are retained for reconciliation even when
    -- usage_logs is unavailable.
    balance_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    subscription_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    api_key_quota_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    api_key_rate_limit_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    account_quota_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT gateway_usage_ledger_oidc_identity_pair_check CHECK (
        (oidc_issuer IS NULL AND oidc_subject IS NULL)
        OR
        (oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_gateway_usage_ledger_request_api_key
    ON gateway_usage_ledger (request_id, api_key_id);

CREATE INDEX IF NOT EXISTS idx_gateway_usage_ledger_identity_created
    ON gateway_usage_ledger (oidc_issuer, oidc_subject, created_at DESC)
    WHERE oidc_subject IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_gateway_usage_ledger_created_at
    ON gateway_usage_ledger (created_at);

COMMENT ON TABLE gateway_usage_ledger IS
    'Durable usage and billing audit ledger committed atomically with each gateway charge';
COMMENT ON COLUMN gateway_usage_ledger.oidc_subject IS
    'Subject from the verified OAuth access token; never derived from correlation headers';
COMMENT ON COLUMN gateway_usage_ledger.tabro_run_id IS
    'Untrusted Tabro run correlation identifier';
COMMENT ON COLUMN gateway_usage_ledger.tabro_project_id IS
    'Untrusted Tabro project correlation identifier';
