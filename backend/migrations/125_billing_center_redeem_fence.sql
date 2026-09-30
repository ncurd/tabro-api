-- A transferred, still-unused legacy code cannot be redeemed by ANY old user
-- or old application instance after central issuance has claimed its identity.
ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS billing_authority_revision bigint NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS billing_center_redeem_authority (
 source_code_id bigint PRIMARY KEY REFERENCES redeem_codes(id) ON DELETE RESTRICT,
 transfer_id varchar(200) NOT NULL UNIQUE,
 code_sha256 char(64) NOT NULL UNIQUE,
 evidence jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT NOW()
);
CREATE TRIGGER billing_center_redeem_authority_immutable BEFORE UPDATE OR DELETE ON billing_center_redeem_authority
 FOR EACH ROW EXECUTE FUNCTION billing_center_immutable_migration_audit();
CREATE OR REPLACE FUNCTION billing_center_guard_transferred_redeem() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM billing_center_redeem_authority WHERE source_code_id=OLD.id) THEN
  RAISE EXCEPTION 'legacy redemption authority transferred for code %',OLD.id USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER billing_center_transferred_redeem_fence BEFORE UPDATE OR DELETE ON redeem_codes
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_transferred_redeem();
