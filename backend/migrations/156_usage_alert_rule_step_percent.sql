ALTER TABLE usage_alert_rules
    ADD COLUMN IF NOT EXISTS step_percent DECIMAL(10,4);

ALTER TABLE usage_alert_rules
    DROP CONSTRAINT IF EXISTS usage_alert_rules_step_percent_check;

ALTER TABLE usage_alert_rules
    ADD CONSTRAINT usage_alert_rules_step_percent_check
        CHECK (step_percent IS NULL OR (step_percent >= 0 AND step_percent <= 100));
