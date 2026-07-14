package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpsertUsageAlertSnapshotRejectsOlderSamples(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rowsAffected int64
		accepted     bool
	}{
		{name: "new sample", rowsAffected: 1, accepted: true},
		{name: "older sample", rowsAffected: 0, accepted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			mock.ExpectExec(`(?s)INSERT INTO real_account_usage_snapshots.*WHERE real_account_usage_snapshots.sampled_at <= EXCLUDED.sampled_at`).
				WillReturnResult(sqlmock.NewResult(0, tc.rowsAffected))

			repo := &usageAlertRepository{sql: db}
			accepted, err := repo.UpsertSnapshot(context.Background(), &service.UsageAlertSnapshot{
				AccountID:     2,
				RealAccountID: 9,
				UsageType:     service.UsageAlertTypeOverall,
				Platform:      service.UsageAlertPlatformAnthropic,
				Source:        service.UsageAlertSourceClaudeHeaders,
				SampledAt:     time.Now().UTC(),
				Windows: map[string]service.UsageAlertWindowSnapshot{
					service.UsageAlertWindow7d: {UsedPercent: 5, RemainingPercent: 95},
				},
			})

			require.NoError(t, err)
			require.Equal(t, tc.accepted, accepted)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
