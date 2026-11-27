package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResetReservationRepoBatchLookups(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	payload12, err := json.Marshal(&service.OpenAIResetReservation{
		ID: "res-12", AccountID: 12, IdentityHash: "hash-a", Status: "pending", Version: 1,
	})
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT payload FROM openai_reset_reservations WHERE account_id IN \(\$1,\$2\)`).
		WithArgs(int64(12), int64(13)).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(payload12))

	mock.ExpectQuery(`SELECT payload FROM openai_reset_reservations WHERE identity_hash IN \(\$1\)`).
		WithArgs("hash-a").
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(payload12))

	repo := &openAIResetReservationRepository{sql: db}
	byID, err := repo.GetByAccountIDs(context.Background(), []int64{12, 13})
	require.NoError(t, err)
	require.Contains(t, byID, int64(12))
	require.Equal(t, "res-12", byID[12].ID)

	byIdent, err := repo.GetActiveByIdentities(context.Background(), []string{"hash-a"})
	require.NoError(t, err)
	require.Contains(t, byIdent, "hash-a")
	require.Equal(t, int64(12), byIdent["hash-a"].AccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}
