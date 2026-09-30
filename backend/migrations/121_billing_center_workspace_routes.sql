-- Explicit workspace authority registration. Actor membership and payer
-- selection remain checked by Auth; these rows never map users by email.
CREATE TABLE IF NOT EXISTS billing_center_workspace_routes (
 oidc_issuer text NOT NULL,
 tenant_id varchar(200) NOT NULL,
 billing_account_id varchar(200) NOT NULL,
 owner_epoch bigint NOT NULL CHECK(owner_epoch>=0),
 billing_mode varchar(20) NOT NULL CHECK(billing_mode IN ('central','draining','frozen')),
 created_at timestamptz NOT NULL DEFAULT NOW(),
 updated_at timestamptz NOT NULL DEFAULT NOW(),
 PRIMARY KEY(oidc_issuer,tenant_id),
 UNIQUE(billing_account_id)
);
