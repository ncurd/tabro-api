-- Bind durable financial evidence to the exact frozen Auth operation. No
-- mutable local user/key mapping is used to authorize the read-only detail API.
ALTER TABLE gateway_usage_ledger
 ADD COLUMN IF NOT EXISTS billing_producer_client_id VARCHAR(200),
 ADD COLUMN IF NOT EXISTS billing_origin_app_id VARCHAR(200),
 ADD COLUMN IF NOT EXISTS billing_operation_id VARCHAR(200);

DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='gateway_usage_ledger'::regclass
  AND conname='gateway_usage_ledger_billing_identity_check') THEN
 ALTER TABLE gateway_usage_ledger ADD CONSTRAINT gateway_usage_ledger_billing_identity_check CHECK (
  (billing_producer_client_id IS NULL AND billing_origin_app_id IS NULL AND billing_operation_id IS NULL)
  OR
  (billing_producer_client_id IS NOT NULL AND billing_origin_app_id IS NOT NULL AND billing_operation_id IS NOT NULL)
 );
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='gateway_usage_ledger'::regclass
  AND conname='gateway_usage_ledger_billing_operation_fk') THEN
 ALTER TABLE gateway_usage_ledger ADD CONSTRAINT gateway_usage_ledger_billing_operation_fk FOREIGN KEY
 (billing_producer_client_id,billing_origin_app_id,billing_operation_id)
 REFERENCES billing_center_operations(producer_client_id,origin_app_id,operation_id) ON DELETE RESTRICT;
 END IF;
END $$;

-- Historical rows are included only when the committed terminal event and
-- verified actor/workspace identify one unambiguous operation for the evidence.
WITH matches AS (
 SELECT g.id, min(o.producer_client_id) AS producer_client_id,
  min(o.origin_app_id) AS origin_app_id,min(o.operation_id) AS operation_id
 FROM gateway_usage_ledger g
 JOIN billing_center_outbox e ON e.kind='settle'
  AND e.payload->>'evidence_reference'='gateway_usage_ledger:' || g.request_id
 JOIN billing_center_operations o ON o.producer_client_id=e.producer_client_id
  AND o.origin_app_id=e.origin_app_id AND o.operation_id=e.operation_id
 WHERE g.billing_mode='central' AND o.billing_mode='central'
  AND g.oidc_subject=o.actor_user_id AND g.oidc_tenant=o.tenant_id
  AND e.payload->'usage'->>'credit_amount' ~ '^[0-9]+([.][0-9]+)?$'
  AND CASE WHEN e.payload->'usage'->>'credit_amount' ~ '^[0-9]+([.][0-9]+)?$'
   THEN (e.payload->'usage'->>'credit_amount')::numeric=g.actual_cost ELSE false END
  AND g.billing_operation_id IS NULL
 GROUP BY g.id HAVING count(*)=1
)
UPDATE gateway_usage_ledger g SET billing_producer_client_id=m.producer_client_id,
 billing_origin_app_id=m.origin_app_id,billing_operation_id=m.operation_id
FROM matches m WHERE g.id=m.id;

CREATE INDEX IF NOT EXISTS idx_gateway_usage_ledger_billing_identity_created
 ON gateway_usage_ledger(oidc_issuer,oidc_subject,oidc_tenant,billing_origin_app_id,created_at DESC,id DESC)
 WHERE billing_operation_id IS NOT NULL;
