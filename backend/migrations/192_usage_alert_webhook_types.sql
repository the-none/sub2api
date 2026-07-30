-- Repair databases that applied the original migration 154 before webhook
-- types and provider-specific configuration were added to that migration.
ALTER TABLE usage_alert_webhooks
    ADD COLUMN IF NOT EXISTS type VARCHAR(32),
    ADD COLUMN IF NOT EXISTS config JSONB;

UPDATE usage_alert_webhooks
SET type = 'json_post'
WHERE type IS NULL OR BTRIM(type) = '';

UPDATE usage_alert_webhooks
SET config = '{}'::jsonb
WHERE config IS NULL;

ALTER TABLE usage_alert_webhooks
    ALTER COLUMN type SET DEFAULT 'json_post',
    ALTER COLUMN type SET NOT NULL,
    ALTER COLUMN config SET DEFAULT '{}'::jsonb,
    ALTER COLUMN config SET NOT NULL,
    ALTER COLUMN url DROP NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'usage_alert_webhooks'::regclass
          AND conname = 'usage_alert_webhooks_type_check'
    ) THEN
        ALTER TABLE usage_alert_webhooks
            ADD CONSTRAINT usage_alert_webhooks_type_check
            CHECK (type IN ('json_post', 'telegram'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'usage_alert_webhooks'::regclass
          AND conname = 'usage_alert_webhooks_json_post_url_check'
    ) THEN
        ALTER TABLE usage_alert_webhooks
            ADD CONSTRAINT usage_alert_webhooks_json_post_url_check
            CHECK (type <> 'json_post' OR NULLIF(BTRIM(url), '') IS NOT NULL);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS usage_alert_webhooks_type_idx
    ON usage_alert_webhooks (type)
    WHERE deleted_at IS NULL;
