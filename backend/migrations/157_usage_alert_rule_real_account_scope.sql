ALTER TABLE usage_alert_rules
    ADD COLUMN IF NOT EXISTS real_account_id BIGINT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_name = 'usage_alert_rules_real_account_id_fkey'
          AND table_name = 'usage_alert_rules'
    ) THEN
        ALTER TABLE usage_alert_rules
            ADD CONSTRAINT usage_alert_rules_real_account_id_fkey
            FOREIGN KEY (real_account_id)
            REFERENCES real_accounts (id)
            ON DELETE CASCADE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS usage_alert_rules_enabled_real_account_idx
    ON usage_alert_rules (enabled, real_account_id)
    WHERE deleted_at IS NULL AND real_account_id IS NOT NULL;
