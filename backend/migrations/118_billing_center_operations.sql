-- Durable gateway intents and terminal-command outbox. These are not a wallet
-- and have no cascading foreign keys to users, API keys, or provider accounts.
CREATE TABLE IF NOT EXISTS billing_center_operations (
 producer_client_id VARCHAR(200) NOT NULL,
 origin_app_id VARCHAR(200) NOT NULL,
 operation_id VARCHAR(200) NOT NULL,
 actor_user_id VARCHAR(200) NOT NULL,
 tenant_id VARCHAR(200) NOT NULL,
 request_fingerprint CHAR(64) NOT NULL,
 billing_mode VARCHAR(16) NOT NULL CHECK (billing_mode IN ('local','shadow','central')),
 owner_epoch BIGINT NOT NULL CHECK (owner_epoch >= 0),
 state VARCHAR(32) NOT NULL DEFAULT 'intent' CHECK (state IN
  ('intent','reserved','dispatching','dispatched','settlement_pending','release_pending','settled','released','reconciliation_required')),
 version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
 attempt_id VARCHAR(200) NOT NULL DEFAULT '',
 reservation_id VARCHAR(200) NOT NULL DEFAULT '',
 remote_version BIGINT NOT NULL DEFAULT 0,
 request_payload JSONB NOT NULL DEFAULT '{}',
 snapshot JSONB NOT NULL DEFAULT '{}',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (producer_client_id, origin_app_id, operation_id)
);
CREATE INDEX IF NOT EXISTS billing_center_operations_recovery_idx
 ON billing_center_operations(state, updated_at)
 WHERE state NOT IN ('settled','released');

CREATE TABLE IF NOT EXISTS billing_center_outbox (
 producer_client_id VARCHAR(200) NOT NULL,
 origin_app_id VARCHAR(200) NOT NULL,
 operation_id VARCHAR(200) NOT NULL,
 event_id VARCHAR(200) NOT NULL,
 schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version = 1),
 kind VARCHAR(16) NOT NULL CHECK (kind IN ('settle','release')),
 reservation_id VARCHAR(200) NOT NULL,
 payload JSONB NOT NULL,
 payload_fingerprint CHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','blocked')),
 attempts INTEGER NOT NULL DEFAULT 0,
 available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 lease_token VARCHAR(80) NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 last_error_code VARCHAR(120) NOT NULL DEFAULT '',
 delivered_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (producer_client_id, origin_app_id, event_id),
 UNIQUE (producer_client_id, origin_app_id, operation_id),
 FOREIGN KEY (producer_client_id, origin_app_id, operation_id)
  REFERENCES billing_center_operations(producer_client_id, origin_app_id, operation_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS billing_center_outbox_delivery_idx
 ON billing_center_outbox(available_at, lease_until)
 WHERE status IN ('pending','processing');
