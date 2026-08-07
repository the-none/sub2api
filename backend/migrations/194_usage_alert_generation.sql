-- Track logical usage-window generations independently from reset_at, whose
-- projected value can drift between observations and across manual resets.

ALTER TABLE usage_alert_states
    ADD COLUMN IF NOT EXISTS last_generation BIGINT NOT NULL DEFAULT 0;

-- Seed per-window ordering metadata before new instances start comparing it.
-- Without this backfill, the first post-upgrade writer would necessarily win
-- against a legacy window even when that writer carried a late sample.
UPDATE real_account_usage_snapshots AS snapshot
SET snapshot_json = jsonb_set(
    snapshot.snapshot_json,
    '{windows}',
    COALESCE((
        SELECT jsonb_object_agg(
            window_entry.key,
            jsonb_build_object(
                    'generation', 1,
                    'sampled_at', to_jsonb(snapshot.sampled_at)
                )
                || CASE
                    WHEN snapshot.platform = 'openai'
                        AND snapshot.quota_dimension = 'overall'
                        AND window_entry.key = '7d'
                        AND window_entry.value ? 'reset_at'
                    THEN jsonb_build_object('boundary_at', window_entry.value->'reset_at')
                    ELSE '{}'::jsonb
                END
                || window_entry.value
        )
        FROM jsonb_each(COALESCE(snapshot.snapshot_json->'windows', '{}'::jsonb)) AS window_entry
    ), '{}'::jsonb),
    true
)
WHERE jsonb_typeof(snapshot.snapshot_json->'windows') = 'object';

COMMENT ON COLUMN usage_alert_states.last_generation IS
    'Logical quota-window generation; reset_at is display data, not the generation identity.';
