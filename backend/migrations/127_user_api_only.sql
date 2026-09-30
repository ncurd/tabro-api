-- Keep API-only users active for model requests while disabling gateway web login.
ALTER TABLE users
ADD COLUMN IF NOT EXISTS api_only BOOLEAN NOT NULL DEFAULT FALSE;
