//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestResetReservationRepositoryCASAndSubscriptionUniqueness(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	accounts := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	repo := &openAIResetReservationRepository{sql: tx}
	a := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "reservation", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
	})
	b := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "alias", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
	})
	r := &service.OpenAIResetReservation{
		ID: uuid.NewString(), AccountID: a.ID, IdentityHash: "subscription", CreditHash: "credit",
		Status: "pending", Version: 1, ExpiresAt: time.Now().Add(time.Hour), ExecuteAt: time.Now(),
	}
	ok, err := repo.CompareAndSwap(ctx, r, 0)
	require.NoError(t, err)
	require.True(t, ok)
	alias := *r
	alias.ID, alias.AccountID = uuid.NewString(), b.ID
	ok, err = repo.CompareAndSwap(ctx, &alias, 0)
	require.NoError(t, err)
	require.False(t, ok, "only one alias may reserve a subscription")
	replacement := *r
	replacement.ID, replacement.Version = uuid.NewString(), 2
	ok, err = repo.CompareAndSwap(ctx, &replacement, 1)
	require.NoError(t, err)
	require.False(t, ok, "a different reservation cannot replace an active operation")
	// Unrelated account edits cannot resurrect or overwrite reservation state.
	a.Extra = map[string]any{"codex_reset_credit_reservation": "untrusted"}
	require.NoError(t, accounts.Update(ctx, a))
	got, err := repo.Get(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, r.ID, got.ID)
	r.Status, r.Version = "cancelled", 2
	ok, err = repo.CompareAndSwap(ctx, r, 1)
	require.NoError(t, err)
	require.True(t, ok)
	stale := *r
	stale.Status = "resetting"
	ok, err = repo.CompareAndSwap(ctx, &stale, 1)
	require.NoError(t, err)
	require.False(t, ok, "stale worker cannot resurrect cancelled reservation")
	ok, err = repo.CompareAndSwap(ctx, &alias, 0)
	require.NoError(t, err)
	require.True(t, ok, "cancellation releases the subscription reservation")
	ids, err := repo.ListRunnable(ctx, 0, 100)
	require.NoError(t, err)
	require.Contains(t, ids, b.ID)
	require.NotContains(t, ids, a.ID)
}
