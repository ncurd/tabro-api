-- Persist the revocation generation embedded in local access and refresh
-- tokens. Existing users/tokens remain on generation zero until a password
-- change increments the value.
ALTER TABLE users
ADD COLUMN IF NOT EXISTS token_version BIGINT NOT NULL DEFAULT 0;
