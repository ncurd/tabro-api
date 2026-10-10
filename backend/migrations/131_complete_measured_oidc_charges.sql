-- Settle already measured gateway usage promptly, so the unused part of an
-- old maximum reservation can be returned without waiting for midnight.
-- Keep the immutable operation, evidence and receipt identity unchanged.
-- Unknown/incomplete work and blocked deliveries require reconciliation.
UPDATE billing_center_outbox AS e
SET available_at = NOW(), updated_at = NOW()
FROM billing_center_operations AS o
WHERE (e.producer_client_id, e.origin_app_id, e.operation_id) =
      (o.producer_client_id, o.origin_app_id, o.operation_id)
  AND o.billing_mode = 'central'
  AND o.state = 'settlement_pending'
  AND o.gateway_pricing_snapshot IS NOT NULL
  AND o.request_payload -> 'maximum_usage' ->> 'credit_amount' IS NOT NULL
  AND (o.request_payload -> 'maximum_usage') - 'credit_amount' = '{}'::jsonb
  AND e.kind = 'settle'
  AND e.status = 'pending'
  AND e.available_at > NOW()
  AND e.payload ->> 'usage_complete' = 'true'
  AND (e.payload -> 'usage') - 'credit_amount' = '{}'::jsonb
  AND e.payload -> 'usage' ->> 'credit_amount' IS NOT NULL;
