-- Operator decisions supersede delivery attempts; they are never recorded as
-- successful delivery of a different settle/release payload.
ALTER TABLE billing_center_operations ADD COLUMN IF NOT EXISTS reconciliation_checked_at TIMESTAMPTZ;
ALTER TABLE billing_center_outbox DROP CONSTRAINT IF EXISTS billing_center_outbox_status_check;
ALTER TABLE billing_center_outbox ADD CONSTRAINT billing_center_outbox_status_check
 CHECK (status IN ('pending','processing','delivered','blocked','superseded'));
ALTER TABLE billing_center_outbox ADD COLUMN IF NOT EXISTS superseded_by_case_id VARCHAR(200);
ALTER TABLE billing_center_outbox ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS billing_center_reconciliation_receipts (
 producer_client_id VARCHAR(200) NOT NULL,
 origin_app_id VARCHAR(200) NOT NULL,
 operation_id VARCHAR(200) NOT NULL,
 case_id VARCHAR(200) NOT NULL,
 resolution_fingerprint CHAR(64) NOT NULL,
 resolution_action VARCHAR(32) NOT NULL CHECK (resolution_action IN ('verified_usage','waive')),
 operator_user_id VARCHAR(200) NOT NULL,
 resolution JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (producer_client_id,origin_app_id,operation_id),
 UNIQUE (producer_client_id,origin_app_id,case_id),
 FOREIGN KEY (producer_client_id,origin_app_id,operation_id)
  REFERENCES billing_center_operations(producer_client_id,origin_app_id,operation_id) ON DELETE RESTRICT
);
