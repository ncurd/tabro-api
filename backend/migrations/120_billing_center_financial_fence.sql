-- One ownership record governs every legacy financial writer, including older
-- application processes which do not understand the Billing Center connector.
ALTER TABLE users ADD COLUMN IF NOT EXISTS billing_authority_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE billing_center_account_routes DROP CONSTRAINT IF EXISTS billing_center_account_routes_billing_mode_check;
ALTER TABLE billing_center_account_routes ADD CONSTRAINT billing_center_account_routes_billing_mode_check
 CHECK (billing_mode IN ('local','shadow','draining','fenced','frozen','central'));
ALTER TABLE billing_center_account_routes ADD COLUMN IF NOT EXISTS migration_batch_id varchar(200);
ALTER TABLE billing_center_account_routes ADD COLUMN IF NOT EXISTS frozen_at timestamptz;
ALTER TABLE billing_center_account_routes ADD COLUMN IF NOT EXISTS snapshot_sha256 varchar(64);
ALTER TABLE billing_center_account_routes ADD COLUMN IF NOT EXISTS target_receipt_id varchar(200);

CREATE TABLE IF NOT EXISTS billing_center_migration_audit (
 id bigserial PRIMARY KEY,
 local_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 batch_id varchar(200) NOT NULL,
 action varchar(40) NOT NULL,
 owner_epoch bigint NOT NULL,
 evidence jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(local_user_id,batch_id,action)
);

-- Serializing on the user row also covers the race where a route is inserted
-- while an old writer sees no route. Freeze acquires this same lock first.
CREATE OR REPLACE FUNCTION billing_center_assert_legacy_write(target_user bigint) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE authority text;
BEGIN
 IF target_user IS NULL THEN RETURN; END IF;
 PERFORM 1 FROM users WHERE id=target_user FOR UPDATE;
 SELECT billing_mode INTO authority FROM billing_center_account_routes WHERE local_user_id=target_user;
 IF authority IN ('fenced','frozen','central') THEN
  RAISE EXCEPTION 'legacy financial write fenced for user %',target_user USING ERRCODE='55000';
 END IF;
END $$;

CREATE OR REPLACE FUNCTION billing_center_guard_legacy_money() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE old_record jsonb; new_record jsonb; old_user bigint; new_user bigint;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_record:=to_jsonb(OLD); END IF;
 IF TG_OP <> 'DELETE' THEN new_record:=to_jsonb(NEW); END IF;
 IF TG_TABLE_NAME='users' THEN
  -- Profile edits do not change the customer's monetary authority.
  IF TG_OP='UPDATE' AND (old_record->'balance',old_record->'total_recharged',old_record->'deleted_at')
    IS NOT DISTINCT FROM (new_record->'balance',new_record->'total_recharged',new_record->'deleted_at') THEN RETURN NEW; END IF;
  old_user:=(old_record->>'id')::bigint; new_user:=(new_record->>'id')::bigint;
 ELSIF TG_TABLE_NAME='redeem_codes' THEN
  old_user:=(old_record->>'used_by')::bigint; new_user:=(new_record->>'used_by')::bigint;
 ELSIF TG_TABLE_NAME='api_keys' THEN
  -- Usage timestamps and immediate local revocation remain possible. Financial
  -- policy, ownership and window counters are migrated and cannot be reset here.
  IF TG_OP='UPDATE' AND
   (old_record - ARRAY['updated_at','last_used_at','name','status','deleted_at','ip_whitelist','ip_blacklist'])
    IS NOT DISTINCT FROM
   (new_record - ARRAY['updated_at','last_used_at','name','status','deleted_at','ip_whitelist','ip_blacklist']) THEN RETURN NEW; END IF;
  old_user:=(old_record->>'user_id')::bigint; new_user:=(new_record->>'user_id')::bigint;
 ELSE
  old_user:=(old_record->>'user_id')::bigint; new_user:=(new_record->>'user_id')::bigint;
 END IF;
 -- Always lock in ID order if an unsafe legacy writer attempts reassignment.
 IF old_user IS NOT NULL AND new_user IS NOT NULL AND old_user>new_user THEN
  PERFORM billing_center_assert_legacy_write(new_user);
  PERFORM billing_center_assert_legacy_write(old_user);
 ELSE
  PERFORM billing_center_assert_legacy_write(old_user);
  IF new_user IS DISTINCT FROM old_user THEN PERFORM billing_center_assert_legacy_write(new_user); END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS billing_center_users_money_fence ON users;
CREATE TRIGGER billing_center_users_money_fence BEFORE UPDATE OR DELETE ON users
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();
DROP TRIGGER IF EXISTS billing_center_subscriptions_money_fence ON user_subscriptions;
CREATE TRIGGER billing_center_subscriptions_money_fence BEFORE INSERT OR UPDATE OR DELETE ON user_subscriptions
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();
DROP TRIGGER IF EXISTS billing_center_keys_money_fence ON api_keys;
CREATE TRIGGER billing_center_keys_money_fence BEFORE INSERT OR UPDATE OR DELETE ON api_keys
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();
DROP TRIGGER IF EXISTS billing_center_orders_money_fence ON payment_orders;
CREATE TRIGGER billing_center_orders_money_fence BEFORE INSERT OR UPDATE OR DELETE ON payment_orders
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();
DROP TRIGGER IF EXISTS billing_center_redeem_money_fence ON redeem_codes;
CREATE TRIGGER billing_center_redeem_money_fence BEFORE INSERT OR UPDATE OR DELETE ON redeem_codes
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();

-- Group limits are shared legacy subscription policy. Changing one after an
-- account freezes would mutate its exported entitlement without a wallet write.
CREATE OR REPLACE FUNCTION billing_center_guard_group_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE member_id bigint;
BEGIN
 IF TG_OP='UPDATE' AND (to_jsonb(OLD)-ARRAY['name','description','updated_at'])
   IS NOT DISTINCT FROM (to_jsonb(NEW)-ARRAY['name','description','updated_at']) THEN RETURN NEW; END IF;
 FOR member_id IN SELECT user_id FROM user_subscriptions WHERE group_id=OLD.id UNION SELECT user_id FROM api_keys WHERE group_id=OLD.id ORDER BY user_id LOOP
  PERFORM billing_center_assert_legacy_write(member_id);
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS billing_center_groups_policy_fence ON groups;
CREATE TRIGGER billing_center_groups_policy_fence BEFORE UPDATE OR DELETE ON groups
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_group_policy();

CREATE OR REPLACE FUNCTION billing_center_guard_authority_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.billing_mode NOT IN ('local','shadow') THEN RAISE EXCEPTION 'billing authority cannot be deleted' USING ERRCODE='55000'; END IF;
  RETURN OLD;
 END IF;
 PERFORM 1 FROM users WHERE id=NEW.local_user_id FOR UPDATE;
 -- Touch the common fence row, not just a lock: a legacy REPEATABLE READ
 -- transaction with a pre-freeze snapshot must get a serialization failure
 -- instead of seeing an obsolete/missing route after it waits for this lock.
 UPDATE users SET billing_authority_revision=billing_authority_revision+1 WHERE id=NEW.local_user_id;
 IF TG_OP='INSERT' THEN
  IF NEW.billing_mode NOT IN ('local','shadow') THEN RAISE EXCEPTION 'new authority must start local or shadow' USING ERRCODE='55000'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.local_user_id<>OLD.local_user_id OR NEW.owner_epoch<OLD.owner_epoch THEN
  RAISE EXCEPTION 'invalid billing authority identity/epoch' USING ERRCODE='55000';
 END IF;
 IF OLD.billing_mode NOT IN ('local','shadow') AND
  (NEW.oidc_issuer,NEW.actor_user_id,NEW.tenant_id,NEW.origin_app_id,NEW.billing_account_id,NEW.migration_batch_id)
   IS DISTINCT FROM
  (OLD.oidc_issuer,OLD.actor_user_id,OLD.tenant_id,OLD.origin_app_id,OLD.billing_account_id,OLD.migration_batch_id) THEN
  RAISE EXCEPTION 'frozen migration identity cannot change' USING ERRCODE='55000';
 END IF;
 IF NEW.billing_mode IS DISTINCT FROM OLD.billing_mode THEN
  IF NOT ((OLD.billing_mode IN ('local','shadow') AND NEW.billing_mode IN ('local','shadow','draining')) OR
          (OLD.billing_mode='draining' AND NEW.billing_mode IN ('frozen','fenced')) OR
          (OLD.billing_mode IN ('frozen','fenced') AND NEW.billing_mode='central')) THEN
   RAISE EXCEPTION 'invalid billing authority transition' USING ERRCODE='55000';
  END IF;
  IF NEW.billing_mode='draining' AND (NEW.migration_batch_id IS NULL OR NEW.migration_batch_id='') THEN
   RAISE EXCEPTION 'migration batch is required' USING ERRCODE='55000';
  END IF;
  IF NEW.billing_mode IN ('frozen','fenced') AND NEW.frozen_at IS NULL THEN
   RAISE EXCEPTION 'freeze timestamp is required' USING ERRCODE='55000';
  END IF;
  IF NEW.billing_mode='central' AND (NEW.owner_epoch<>OLD.owner_epoch+1 OR NEW.snapshot_sha256 !~ '^[0-9a-f]{64}$' OR
     NEW.snapshot_sha256 IS NULL OR NEW.target_receipt_id IS NULL OR NEW.target_receipt_id='') THEN
   RAISE EXCEPTION 'central activation requires reconciled snapshot, target receipt and next epoch' USING ERRCODE='55000';
  END IF;
 ELSIF NEW.owner_epoch<>OLD.owner_epoch THEN
  RAISE EXCEPTION 'epoch changes only at an ownership transfer' USING ERRCODE='55000';
 END IF;
 IF OLD.billing_mode='central' AND
  (NEW.snapshot_sha256,NEW.target_receipt_id,NEW.frozen_at) IS DISTINCT FROM (OLD.snapshot_sha256,OLD.target_receipt_id,OLD.frozen_at) THEN
  RAISE EXCEPTION 'activated migration evidence is immutable' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS billing_center_authority_transition ON billing_center_account_routes;
CREATE TRIGGER billing_center_authority_transition BEFORE INSERT OR UPDATE OR DELETE ON billing_center_account_routes
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_authority_transition();

CREATE OR REPLACE FUNCTION billing_center_immutable_migration_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'billing migration audit is append-only' USING ERRCODE='55000'; END $$;
DROP TRIGGER IF EXISTS billing_center_migration_audit_immutable ON billing_center_migration_audit;
CREATE TRIGGER billing_center_migration_audit_immutable BEFORE UPDATE OR DELETE ON billing_center_migration_audit
 FOR EACH ROW EXECUTE FUNCTION billing_center_immutable_migration_audit();

-- Customer-specific multipliers and channel overrides are exported policy too.
DROP TRIGGER IF EXISTS billing_center_user_rates_fence ON user_group_rate_multipliers;
CREATE TRIGGER billing_center_user_rates_fence BEFORE INSERT OR UPDATE OR DELETE ON user_group_rate_multipliers
 FOR EACH ROW EXECUTE FUNCTION billing_center_guard_legacy_money();
CREATE OR REPLACE FUNCTION billing_center_guard_channel_policy() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_doc jsonb; new_doc jsonb; channel_ids bigint[]; group_ids bigint[]; member_id bigint;
BEGIN
 IF TG_OP<>'INSERT' THEN old_doc:=to_jsonb(OLD); END IF;
 IF TG_OP<>'DELETE' THEN new_doc:=to_jsonb(NEW); END IF;
 IF TG_OP='UPDATE' AND (old_doc-ARRAY['name','description','updated_at','features']) IS NOT DISTINCT FROM
  (new_doc-ARRAY['name','description','updated_at','features']) THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='channels' THEN channel_ids:=ARRAY[(old_doc->>'id')::bigint,(new_doc->>'id')::bigint];
 ELSIF TG_TABLE_NAME='channel_pricing_intervals' THEN
  SELECT array_agg(channel_id) INTO channel_ids FROM channel_model_pricing
   WHERE id IN ((old_doc->>'pricing_id')::bigint,(new_doc->>'pricing_id')::bigint);
 ELSE channel_ids:=ARRAY[(old_doc->>'channel_id')::bigint,(new_doc->>'channel_id')::bigint]; END IF;
 SELECT array_agg(group_id) INTO group_ids FROM channel_groups WHERE channel_id=ANY(channel_ids);
 IF TG_TABLE_NAME='channel_groups' THEN group_ids:=COALESCE(group_ids,ARRAY[]::bigint[]) || ARRAY[(old_doc->>'group_id')::bigint,(new_doc->>'group_id')::bigint]; END IF;
 FOR member_id IN SELECT user_id FROM user_subscriptions WHERE group_id=ANY(group_ids)
  UNION SELECT user_id FROM api_keys WHERE group_id=ANY(group_ids) ORDER BY user_id LOOP
  PERFORM billing_center_assert_legacy_write(member_id);
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS billing_center_channels_fence ON channels;
CREATE TRIGGER billing_center_channels_fence BEFORE INSERT OR UPDATE OR DELETE ON channels FOR EACH ROW EXECUTE FUNCTION billing_center_guard_channel_policy();
DROP TRIGGER IF EXISTS billing_center_channel_groups_fence ON channel_groups;
CREATE TRIGGER billing_center_channel_groups_fence BEFORE INSERT OR UPDATE OR DELETE ON channel_groups FOR EACH ROW EXECUTE FUNCTION billing_center_guard_channel_policy();
DROP TRIGGER IF EXISTS billing_center_channel_prices_fence ON channel_model_pricing;
CREATE TRIGGER billing_center_channel_prices_fence BEFORE INSERT OR UPDATE OR DELETE ON channel_model_pricing FOR EACH ROW EXECUTE FUNCTION billing_center_guard_channel_policy();
DROP TRIGGER IF EXISTS billing_center_channel_intervals_fence ON channel_pricing_intervals;
CREATE TRIGGER billing_center_channel_intervals_fence BEFORE INSERT OR UPDATE OR DELETE ON channel_pricing_intervals FOR EACH ROW EXECUTE FUNCTION billing_center_guard_channel_policy();
