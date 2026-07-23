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

			mock.ExpectExec(`(?s)INSERT INTO real_account_usage_snapshots.*WHERE NOT EXISTS.*incoming_window.value.*reset_at.*real_account_usage_snapshots.sampled_at <= EXCLUDED.sampled_at.*OR EXISTS`).
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

func TestClaimUsageAlertWebhookDeliveryDistinguishesClaimedDeliveredAndBusy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		insertRows *sqlmock.Rows
		status     string
		want       service.UsageAlertDeliveryClaim
	}{
		{
			name:       "claimed",
			insertRows: sqlmock.NewRows([]string{"claim_token"}).AddRow("token"),
			want:       service.UsageAlertDeliveryClaimed,
		},
		{
			name:       "already delivered",
			insertRows: sqlmock.NewRows([]string{"claim_token"}),
			status:     "delivered",
			want:       service.UsageAlertDeliveryAlreadyDelivered,
		},
		{
			name:       "busy",
			insertRows: sqlmock.NewRows([]string{"claim_token"}),
			status:     "pending",
			want:       service.UsageAlertDeliveryBusy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			mock.ExpectQuery(`(?s)INSERT INTO usage_alert_deliveries.*ON CONFLICT.*RETURNING claim_token`).
				WithArgs("event", int64(9), int64(7), int64(3), "token", sqlmock.AnyArg()).
				WillReturnRows(tc.insertRows)
			if tc.status != "" {
				mock.ExpectQuery(`(?s)SELECT status.*FROM usage_alert_deliveries`).
					WithArgs("event", int64(3)).
					WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(tc.status))
			}

			repo := &usageAlertRepository{sql: db}
			got, err := repo.ClaimWebhookDelivery(
				context.Background(),
				"event",
				9,
				7,
				3,
				"token",
				time.Now().UTC().Add(-time.Minute),
			)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCompleteUsageAlertWebhookDeliveryRequiresOwnedClaim(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
	}{
		{name: "owned", affected: 1},
		{name: "lost", affected: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			mock.ExpectExec(`(?s)UPDATE usage_alert_deliveries.*claim_token = \$3`).
				WithArgs("event", int64(3), "token").
				WillReturnResult(sqlmock.NewResult(0, tc.affected))

			repo := &usageAlertRepository{sql: db}
			err = repo.CompleteWebhookDelivery(context.Background(), "event", 3, "token")
			if tc.affected == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "claim was lost")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
