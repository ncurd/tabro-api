-- Older installations created this entity through ent schema setup. New databases
-- need the durable task table before accepting asynchronous provider work.
CREATE TABLE IF NOT EXISTS media_generation_jobs (
 id BIGSERIAL PRIMARY KEY,
 public_id VARCHAR(80) NOT NULL UNIQUE,
 kind VARCHAR(40) NOT NULL,
 provider VARCHAR(40) NOT NULL,
 platform VARCHAR(40) NOT NULL,
 status VARCHAR(30) NOT NULL,
 upstream_status VARCHAR(80), upstream_task_id VARCHAR(160), upstream_request_id VARCHAR(160),
 user_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL, group_id BIGINT, account_id BIGINT NOT NULL,
 model VARCHAR(160) NOT NULL, request_json JSONB, upstream_response_json JSONB,
 result_url TEXT, result_content_type VARCHAR(120), expires_at TIMESTAMPTZ,
 audio_voice VARCHAR(160), audio_format VARCHAR(80), audio_character_count INTEGER NOT NULL DEFAULT 0,
 video_duration_seconds INTEGER NOT NULL DEFAULT 0, video_resolution VARCHAR(40), video_ratio VARCHAR(40), video_count INTEGER NOT NULL DEFAULT 0,
 error_code VARCHAR(120), error_message TEXT, usage_recorded_at TIMESTAMPTZ,
 submitted_at TIMESTAMPTZ, completed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE media_generation_jobs ADD COLUMN IF NOT EXISTS billing_snapshot_json JSONB;
ALTER TABLE media_generation_jobs ADD COLUMN IF NOT EXISTS next_poll_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS media_generation_jobs_reconcile_idx ON media_generation_jobs (next_poll_at)
 WHERE usage_recorded_at IS NULL AND billing_snapshot_json IS NOT NULL;
CREATE INDEX IF NOT EXISTS media_generation_jobs_user_created_idx ON media_generation_jobs(user_id, created_at);
CREATE INDEX IF NOT EXISTS media_generation_jobs_api_key_created_idx ON media_generation_jobs(api_key_id, created_at);
