package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration224DecouplesAccountDeliveryReceiptsFromRules(t *testing.T) {
	raw, err := FS.ReadFile("224_usage_alert_account_delivery_receipts.sql")
	require.NoError(t, err)

	sql := string(raw)
	require.Contains(t, sql, "ALTER COLUMN rule_id DROP NOT NULL")
	require.Contains(t, sql, "ON DELETE SET NULL")
	require.Equal(t, 1, strings.Count(sql, "ADD CONSTRAINT usage_alert_deliveries_rule_id_fkey"))
}
