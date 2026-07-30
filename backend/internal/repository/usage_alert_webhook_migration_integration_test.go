//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration192UpgradesLegacyUsageAlertWebhooks(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	_, err = tx.ExecContext(ctx, `
		CREATE SCHEMA usage_alert_webhook_migration_test;
		SET LOCAL search_path TO usage_alert_webhook_migration_test;
		CREATE TABLE usage_alert_webhooks (
			id BIGSERIAL PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			url TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			retry_count INTEGER NOT NULL DEFAULT 2,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			deleted_at TIMESTAMPTZ
		);
		INSERT INTO usage_alert_webhooks (name, url)
		VALUES ('legacy-json', 'https://example.com/hook');
	`)
	require.NoError(t, err)

	content, err := migrations.FS.ReadFile("192_usage_alert_webhook_types.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(content))
	require.NoError(t, err)

	var webhookType, config string
	err = tx.QueryRowContext(
		ctx,
		"SELECT type, config::text FROM usage_alert_webhooks WHERE name = 'legacy-json'",
	).Scan(&webhookType, &config)
	require.NoError(t, err)
	require.Equal(t, "json_post", webhookType)
	require.Equal(t, "{}", config)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO usage_alert_webhooks (name, type, url, config)
		VALUES ('telegram', 'telegram', NULL, '{"chat_id":"123"}'::jsonb)
	`)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, string(content))
	require.NoError(t, err, "migration must remain idempotent after upgrading the legacy schema")
}
