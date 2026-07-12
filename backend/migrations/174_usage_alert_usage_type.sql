-- Generalize alert quota dimensions into extensible usage types. The physical
-- column name is retained so deployed databases can upgrade without rewriting
-- custom alert tables; application APIs expose this value as usage_type.

ALTER TABLE real_account_usage_snapshots
    ALTER COLUMN quota_dimension SET DEFAULT 'overall';
ALTER TABLE usage_alert_rules
    ALTER COLUMN quota_dimension SET DEFAULT 'overall';
ALTER TABLE usage_alert_states
    ALTER COLUMN quota_dimension SET DEFAULT 'overall';

DELETE FROM real_account_usage_snapshots AS legacy
USING real_account_usage_snapshots AS current
WHERE legacy.real_account_id = current.real_account_id
  AND legacy.quota_dimension = 'global'
  AND current.quota_dimension = 'overall';

DELETE FROM usage_alert_states AS legacy
USING usage_alert_states AS current
WHERE legacy.real_account_id = current.real_account_id
  AND legacy.rule_id = current.rule_id
  AND legacy."window" = current."window"
  AND legacy.quota_dimension = 'global'
  AND current.quota_dimension = 'overall';

UPDATE real_account_usage_snapshots SET quota_dimension = 'overall' WHERE quota_dimension = 'global';
UPDATE usage_alert_rules SET quota_dimension = 'overall' WHERE quota_dimension = 'global';
UPDATE usage_alert_states SET quota_dimension = 'overall' WHERE quota_dimension = 'global';

ALTER TABLE real_account_usage_snapshots
    DROP CONSTRAINT IF EXISTS real_account_usage_snapshots_quota_dimension_check;
ALTER TABLE usage_alert_rules
    DROP CONSTRAINT IF EXISTS usage_alert_rules_quota_dimension_check;
ALTER TABLE usage_alert_states
    DROP CONSTRAINT IF EXISTS usage_alert_states_quota_dimension_check;

COMMENT ON COLUMN real_account_usage_snapshots.quota_dimension IS
    'Extensible usage type key (overall, spark, fable, ...).';
COMMENT ON COLUMN usage_alert_rules.quota_dimension IS
    'Extensible usage type selected by this rule.';
COMMENT ON COLUMN usage_alert_states.quota_dimension IS
    'Usage type whose trigger state is tracked.';
