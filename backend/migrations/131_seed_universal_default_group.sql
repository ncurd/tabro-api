-- Add a shared public pool without changing existing groups or exposing their
-- accounts. Administrators explicitly choose which upstream accounts to link.
-- If the requested name is already used, preserve it and use a distinct name.
INSERT INTO groups (
    name, description, platform, subscription_type, is_exclusive, status,
    rate_multiplier, sort_order, allow_messages_dispatch, supported_model_scopes,
    messages_dispatch_model_config, created_at, updated_at
)
SELECT
    CASE
        WHEN EXISTS (SELECT 1 FROM groups WHERE name = '默认分组' AND deleted_at IS NULL)
            THEN '默认分组（全模型）'
        ELSE '默认分组'
    END,
    '公开的全模型路由分组，仅使用明确关联的账号',
    'all', 'standard', FALSE, 'active', 1.0, -100, TRUE,
    '["claude", "gemini_text", "gemini_image"]', '{}', NOW(), NOW()
WHERE NOT EXISTS (
    SELECT 1 FROM groups
    WHERE platform = 'all' AND subscription_type = 'standard'
      AND is_exclusive = FALSE AND deleted_at IS NULL
)
AND NOT EXISTS (
    SELECT 1 FROM groups WHERE name = '默认分组（全模型）' AND deleted_at IS NULL
);
