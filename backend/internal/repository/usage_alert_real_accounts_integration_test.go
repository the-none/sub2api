//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRealAccountsIdentifyDeletedAccountSources(t *testing.T) {
	ctx := context.Background()
	client := testEntTx(t).Client()
	repo := &usageAlertRepository{client: client}
	createReal := func(name string) *dbent.RealAccount {
		row, err := client.RealAccount.Create().SetName(name).SetPlatform(service.PlatformOpenAI).Save(ctx)
		require.NoError(t, err)
		return row
	}
	createAccount := func(realID int64, name string) *dbent.Account {
		row, err := client.Account.Create().SetName(name).SetPlatform(service.PlatformOpenAI).
			SetType(service.AccountTypeOAuth).SetRealAccountID(realID).Save(ctx)
		require.NoError(t, err)
		return row
	}
	list := func() map[int64]*service.RealAccount {
		rows, err := repo.ListRealAccounts(ctx)
		require.NoError(t, err)
		byID := make(map[int64]*service.RealAccount, len(rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
		return byID
	}

	unbound := createReal("new unbound source")
	retired := createReal("deleted account source")
	retiredAccount := createAccount(retired.ID, "old account")
	mixed := createReal("shared source")
	deletedChild := createAccount(mixed.ID, "deleted child")
	liveChild := createAccount(mixed.ID, "disabled but not deleted")
	_, err := client.Account.UpdateOneID(liveChild.ID).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	_, err = client.RealAccountUsageSnapshot.Create().SetRealAccountID(retired.ID).
		SetPlatform(service.PlatformOpenAI).SetSource(service.UsageAlertSourceOpenAICodexHeaders).
		SetSnapshotJSON(map[string]any{"historical": true}).SetSampledAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	require.Contains(t, list(), retired.ID)

	require.NoError(t, client.Account.DeleteOneID(retiredAccount.ID).Exec(ctx))
	require.NoError(t, client.Account.DeleteOneID(deletedChild.ID).Exec(ctx))
	visible := list()
	require.Contains(t, visible, retired.ID, "retained alert configuration still needs its source metadata")
	require.True(t, visible[retired.ID].HasOnlyDeletedAccounts)
	require.Contains(t, visible, unbound.ID, "new sources must remain available for account attachment")
	require.False(t, visible[unbound.ID].HasOnlyDeletedAccounts)
	require.Contains(t, visible, mixed.ID, "disabled accounts are not deleted accounts")
	require.False(t, visible[mixed.ID].HasOnlyDeletedAccounts)
	require.Len(t, visible[mixed.ID].Accounts, 1)
	require.Equal(t, liveChild.ID, visible[mixed.ID].Accounts[0].ID)

	// Hiding is a list policy, not deletion of the source or its historical data.
	detail, err := repo.GetRealAccount(ctx, retired.ID)
	require.NoError(t, err)
	require.True(t, detail.HasOnlyDeletedAccounts)
	detail.Name = "renamed retired source"
	detail, err = repo.UpdateRealAccount(ctx, detail)
	require.NoError(t, err)
	require.True(t, detail.HasOnlyDeletedAccounts, "editing a retired source must preserve its computed status")
	count, err := retired.QueryUsageSnapshot().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	_, err = client.Account.UpdateOneID(retiredAccount.ID).ClearDeletedAt().Save(ctx)
	require.NoError(t, err)
	require.False(t, list()[retired.ID].HasOnlyDeletedAccounts, "restoring an account should make its source visible again")
}
