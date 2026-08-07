//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration194BackfillsAndPreservesUsageAlertGenerationMetadata(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	_, err = tx.ExecContext(ctx, `
		CREATE SCHEMA usage_alert_generation_migration_test;
		SET LOCAL search_path TO usage_alert_generation_migration_test;
		CREATE TABLE usage_alert_states (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE real_account_usage_snapshots (
			id BIGSERIAL PRIMARY KEY,
			platform VARCHAR(50) NOT NULL,
			quota_dimension VARCHAR(32) NOT NULL,
			snapshot_json JSONB NOT NULL,
			sampled_at TIMESTAMPTZ NOT NULL
		);
		INSERT INTO real_account_usage_snapshots (
			platform, quota_dimension, snapshot_json, sampled_at
		) VALUES (
			'openai',
			'overall',
			'{"windows":{"5h":{"used_percent":10},"7d":{"used_percent":20,"reset_at":"2026-08-08T11:42:00Z"}}}'::jsonb,
			'2026-08-07T11:00:00Z'
		);
	`)
	require.NoError(t, err)

	content, err := migrations.FS.ReadFile("194_usage_alert_generation.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(content))
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `INSERT INTO usage_alert_states DEFAULT VALUES`)
	require.NoError(t, err)
	var lastGeneration int64
	err = tx.QueryRowContext(ctx, `
		SELECT last_generation
		FROM usage_alert_states
		LIMIT 1
	`).Scan(&lastGeneration)
	require.NoError(t, err)
	require.Zero(t, lastGeneration)

	var generation int64
	var sampledAt, boundaryAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT
			(snapshot_json->'windows'->'7d'->>'generation')::BIGINT,
			(snapshot_json->'windows'->'7d'->>'sampled_at')::TIMESTAMPTZ,
			(snapshot_json->'windows'->'7d'->>'boundary_at')::TIMESTAMPTZ
		FROM real_account_usage_snapshots
	`).Scan(&generation, &sampledAt, &boundaryAt)
	require.NoError(t, err)
	require.Equal(t, int64(1), generation)
	require.Equal(t, time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC), sampledAt.UTC())
	require.Equal(t, time.Date(2026, 8, 8, 11, 42, 0, 0, time.UTC), boundaryAt.UTC())

	_, err = tx.ExecContext(ctx, `
		UPDATE real_account_usage_snapshots
		SET snapshot_json = jsonb_set(
			jsonb_set(snapshot_json, '{windows,7d,generation}', '9'::jsonb),
			'{windows,7d,sampled_at}', '"2026-08-07T12:00:00Z"'::jsonb
		)
	`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(content))
	require.NoError(t, err, "migration must remain idempotent")

	err = tx.QueryRowContext(ctx, `
		SELECT
			(snapshot_json->'windows'->'7d'->>'generation')::BIGINT,
			(snapshot_json->'windows'->'7d'->>'sampled_at')::TIMESTAMPTZ
		FROM real_account_usage_snapshots
	`).Scan(&generation, &sampledAt)
	require.NoError(t, err)
	require.Equal(t, int64(9), generation)
	require.Equal(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), sampledAt.UTC())
}
