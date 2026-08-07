package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration194BackfillsUsageAlertWindowOrderingWithoutOverwritingMetadata(t *testing.T) {
	raw, err := FS.ReadFile("194_usage_alert_generation.sql")
	require.NoError(t, err)
	sql := string(raw)

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS last_generation BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "UPDATE real_account_usage_snapshots AS snapshot")
	require.Contains(t, sql, "'generation', 1")
	require.Contains(t, sql, "'sampled_at', to_jsonb(snapshot.sampled_at)")
	require.Contains(t, sql, "jsonb_build_object('boundary_at', window_entry.value->'reset_at')")

	defaultsAt := strings.Index(sql, "jsonb_build_object(\n                    'generation', 1")
	existingMetadataAt := strings.Index(sql, "|| window_entry.value")
	require.Greater(t, defaultsAt, -1)
	require.Greater(t, existingMetadataAt, defaultsAt,
		"existing window metadata must merge last so rerunning the migration preserves newer values")
}
