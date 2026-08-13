-- Account-level reset deliveries are shared by every matching rule. Keep their
-- receipts independent from any one rule so deleting a rule cannot erase the
-- account/channel idempotency record.
ALTER TABLE usage_alert_deliveries
    DROP CONSTRAINT IF EXISTS usage_alert_deliveries_rule_id_fkey;

ALTER TABLE usage_alert_deliveries
    ALTER COLUMN rule_id DROP NOT NULL;

ALTER TABLE usage_alert_deliveries
    ADD CONSTRAINT usage_alert_deliveries_rule_id_fkey
    FOREIGN KEY (rule_id) REFERENCES usage_alert_rules(id) ON DELETE SET NULL;
