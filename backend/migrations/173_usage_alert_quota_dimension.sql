-- Isolate usage alert snapshots and state for OpenAI global and Spark quotas.

ALTER TABLE real_account_usage_snapshots
    ADD COLUMN IF NOT EXISTS quota_dimension VARCHAR(32) NOT NULL DEFAULT 'global';

UPDATE real_account_usage_snapshots AS snapshot
SET quota_dimension = COALESCE(NULLIF(account.quota_dimension, ''), 'global')
FROM accounts AS account
WHERE snapshot.snapshot_json->>'account_id' ~ '^[0-9]+$'
  AND account.id = (snapshot.snapshot_json->>'account_id')::BIGINT;

ALTER TABLE usage_alert_rules
    ADD COLUMN IF NOT EXISTS quota_dimension VARCHAR(32) NOT NULL DEFAULT 'global';

ALTER TABLE usage_alert_states
    ADD COLUMN IF NOT EXISTS quota_dimension VARCHAR(32) NOT NULL DEFAULT 'global';

DROP INDEX IF EXISTS real_account_usage_snapshots_real_account_id_uq;
CREATE UNIQUE INDEX IF NOT EXISTS real_account_usage_snapshots_real_dimension_uq
    ON real_account_usage_snapshots (real_account_id, quota_dimension);

DROP INDEX IF EXISTS usage_alert_states_real_rule_window_uq;
CREATE UNIQUE INDEX IF NOT EXISTS usage_alert_states_real_rule_dimension_window_uq
    ON usage_alert_states (real_account_id, rule_id, quota_dimension, "window");

CREATE INDEX IF NOT EXISTS usage_alert_rules_enabled_real_dimension_idx
    ON usage_alert_rules (enabled, real_account_id, quota_dimension)
    WHERE deleted_at IS NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'real_account_usage_snapshots_quota_dimension_check'
    ) THEN
        ALTER TABLE real_account_usage_snapshots
            ADD CONSTRAINT real_account_usage_snapshots_quota_dimension_check
            CHECK (quota_dimension IN ('global', 'spark'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'usage_alert_rules_quota_dimension_check'
    ) THEN
        ALTER TABLE usage_alert_rules
            ADD CONSTRAINT usage_alert_rules_quota_dimension_check
            CHECK (quota_dimension IN ('global', 'spark'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'usage_alert_states_quota_dimension_check'
    ) THEN
        ALTER TABLE usage_alert_states
            ADD CONSTRAINT usage_alert_states_quota_dimension_check
            CHECK (quota_dimension IN ('global', 'spark'));
    END IF;
END $$;
