package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIResetReservationRepository struct{ sql sqlExecutor }

func NewOpenAIResetReservationRepository(db *sql.DB) service.OpenAIResetReservationRepository {
	return &openAIResetReservationRepository{sql: db}
}

func (r *openAIResetReservationRepository) read(ctx context.Context, query string, args ...any) (*service.OpenAIResetReservation, error) {
	var payload []byte
	if err := scanSingleRow(ctx, r.sql, query, args, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var record service.OpenAIResetReservation
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *openAIResetReservationRepository) Get(ctx context.Context, accountID int64) (*service.OpenAIResetReservation, error) {
	return r.read(ctx, `SELECT payload FROM openai_reset_reservations WHERE account_id = $1`, accountID)
}

func (r *openAIResetReservationRepository) GetActiveByIdentity(ctx context.Context, identity string) (*service.OpenAIResetReservation, error) {
	return r.read(ctx, `SELECT payload FROM openai_reset_reservations WHERE identity_hash = $1
		AND status IN ('pending', 'resetting', 'unknown', 'consumed')`, identity)
}

func (r *openAIResetReservationRepository) CompareAndSwap(ctx context.Context, record *service.OpenAIResetReservation, expected int64) (bool, error) {
	if record.Version != expected+1 {
		return false, nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	var result sql.Result
	if expected == 0 {
		result, err = r.sql.ExecContext(ctx, `INSERT INTO openai_reset_reservations
			(account_id, id, identity_hash, status, version, payload)
			SELECT $1, $2, $3, $4, $5, $6::jsonb
			WHERE EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND deleted_at IS NULL)
			ON CONFLICT DO NOTHING`, record.AccountID, record.ID, record.IdentityHash, record.Status, record.Version, string(payload))
	} else {
		result, err = r.sql.ExecContext(ctx, `UPDATE openai_reset_reservations
			SET id=$2, identity_hash=$3, status=$4, version=$5, payload=$6::jsonb, updated_at=NOW()
			WHERE account_id=$1 AND version=$7
			AND (id=$2 OR status IN ('completed', 'cancelled', 'expired', 'unavailable', 'invalidated', 'unconfirmed'))`,
			record.AccountID, record.ID, record.IdentityHash, record.Status, record.Version, string(payload), expected)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return false, infraerrors.Conflict("OPENAI_RESET_RESERVED", "this subscription already has a reservation")
		}
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (r *openAIResetReservationRepository) ListRunnable(ctx context.Context, after int64, limit int) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT account_id FROM openai_reset_reservations
		WHERE account_id > $1 AND status IN ('pending', 'resetting', 'unknown', 'consumed')
		ORDER BY account_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
