-- No accounts are moved automatically. Operators provision verified identity
-- mappings and perform a fenced migration before selecting central mode.
CREATE TABLE IF NOT EXISTS billing_center_account_routes (
 local_user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
 billing_mode varchar(20) NOT NULL CHECK (billing_mode IN ('local','shadow','fenced','central')),
 oidc_issuer text NOT NULL,
 actor_user_id varchar(200) NOT NULL,
 tenant_id varchar(200) NOT NULL,
 origin_app_id varchar(200) NOT NULL,
 billing_account_id varchar(200) NOT NULL,
 owner_epoch bigint NOT NULL CHECK (owner_epoch >= 0),
 updated_at timestamptz NOT NULL DEFAULT NOW(),
 UNIQUE (oidc_issuer, actor_user_id, tenant_id, origin_app_id)
);

CREATE TABLE IF NOT EXISTS billing_center_shadow_observations (
 producer_client_id varchar(200) NOT NULL,
 origin_app_id varchar(200) NOT NULL,
 operation_id varchar(200) NOT NULL,
 local_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 product_key varchar(200) NOT NULL,
 request_payload_hash varchar(64) NOT NULL,
 quote jsonb NOT NULL,
 usage jsonb,
 local_actual_cost numeric(30,10),
 central_estimate jsonb,
 created_at timestamptz NOT NULL DEFAULT NOW(),
 updated_at timestamptz NOT NULL DEFAULT NOW(),
 PRIMARY KEY(producer_client_id,origin_app_id,operation_id)
);

CREATE TABLE IF NOT EXISTS billing_center_shadow_failures (
 producer_client_id varchar(200) NOT NULL,
 origin_app_id varchar(200) NOT NULL,
 operation_id varchar(200) NOT NULL,
 local_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 request_payload_hash varchar(64) NOT NULL,
 stage varchar(40) NOT NULL,
 created_at timestamptz NOT NULL DEFAULT NOW(),
 PRIMARY KEY(producer_client_id,origin_app_id,operation_id,stage)
);
