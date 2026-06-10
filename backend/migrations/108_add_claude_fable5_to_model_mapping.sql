-- Add claude-fable-5 to persisted Antigravity account model mappings without overwriting custom mappings.
UPDATE accounts
SET credentials = jsonb_set(
    COALESCE(credentials, '{}'::jsonb),
    '{model_mapping,claude-fable-5}',
    '"claude-fable-5"'::jsonb,
    true
)
WHERE platform = 'antigravity'
  AND deleted_at IS NULL
  AND credentials->'model_mapping' IS NOT NULL
  AND credentials->'model_mapping'->>'claude-fable-5' IS NULL;
