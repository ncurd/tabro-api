-- Explicit per-key registration references; never store the gateway key or
-- bearer token here. Enrollment/revocation is an authenticated administrative
-- workflow against Auth's authoritative credential registry.
CREATE TABLE IF NOT EXISTS billing_center_credential_bindings (
 api_key_id bigint PRIMARY KEY REFERENCES api_keys(id) ON DELETE RESTRICT,
 local_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 credential_binding_id varchar(200) NOT NULL UNIQUE,
 credential_version bigint NOT NULL CHECK(credential_version>0),
 oidc_issuer text NOT NULL,
 actor_user_id varchar(200) NOT NULL,
 tenant_id varchar(200) NOT NULL,
 enabled boolean NOT NULL DEFAULT TRUE,
 created_at timestamptz NOT NULL DEFAULT NOW(),
 updated_at timestamptz NOT NULL DEFAULT NOW()
);
