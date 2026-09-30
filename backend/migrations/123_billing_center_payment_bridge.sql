-- Provider-side execution state only. Customer orders, funds and refunds are
-- authoritative in Auth; these tables never update the legacy customer wallet.
CREATE TABLE IF NOT EXISTS billing_center_payment_jobs (
 producer_client_id varchar(200) NOT NULL,
 order_id varchar(200) NOT NULL,
 out_trade_no varchar(80) NOT NULL UNIQUE,
 frozen_order jsonb NOT NULL,
 frozen_fingerprint char(64) NOT NULL,
 provider_instance_id varchar(80) NOT NULL,
 provider_key varchar(40) NOT NULL,
 supported_types text NOT NULL DEFAULT '',
 state varchar(32) NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','dispatching','ready','paid','cancelled','reconciliation_required')),
 checkout_json jsonb,
 provider_transaction_id varchar(200),
 updated_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(producer_client_id,order_id)
);
CREATE TABLE IF NOT EXISTS billing_center_refund_jobs (
 producer_client_id varchar(200) NOT NULL,
 refund_id varchar(200) NOT NULL,
 order_id varchar(200) NOT NULL,
 frozen_refund jsonb NOT NULL,
 frozen_fingerprint char(64) NOT NULL,
 provider_instance_id varchar(80) NOT NULL,
 provider_key varchar(40) NOT NULL,
 out_trade_no varchar(80) NOT NULL,
 state varchar(32) NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','dispatching','provider_pending','succeeded','failed','reconciliation_required')),
 provider_refund_id varchar(200),
 updated_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(producer_client_id,refund_id)
);
CREATE TABLE IF NOT EXISTS billing_center_payment_outbox (
 producer_client_id varchar(200) NOT NULL,
 event_id varchar(200) NOT NULL,
 entity_id varchar(200) NOT NULL,
 kind varchar(30) NOT NULL CHECK(kind IN ('checkout','paid','cancelled','refund_complete')),
 payload jsonb NOT NULL,
 fingerprint char(64) NOT NULL,
 delivered_at timestamptz,
 last_error_code varchar(80),
 attempts int NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(producer_client_id,event_id)
);
CREATE INDEX IF NOT EXISTS billing_center_payment_outbox_pending ON billing_center_payment_outbox(producer_client_id,created_at) WHERE delivered_at IS NULL;
