-- Operator-only registrations/replays are durable and reviewable. No credentials
-- or user bearer tokens are stored in audit evidence.
CREATE TABLE IF NOT EXISTS billing_center_operator_audit (
 event_id varchar(200) PRIMARY KEY,
 action varchar(80) NOT NULL,
 fingerprint char(64) NOT NULL,
 evidence jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT NOW()
);
CREATE TRIGGER billing_center_operator_audit_immutable BEFORE UPDATE OR DELETE ON billing_center_operator_audit
 FOR EACH ROW EXECUTE FUNCTION billing_center_immutable_migration_audit();
