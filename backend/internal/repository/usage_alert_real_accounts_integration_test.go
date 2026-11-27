//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
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

func TestResolveShadowRealAccountPreservesConcurrentFamilyReassignment(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &usageAlertRepository{client: client}

	realA, err := client.RealAccount.Create().SetName("Real A").SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	realB, err := client.RealAccount.Create().SetName("Real B").SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)

	parent, err := client.Account.Create().
		SetName("Parent").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetRealAccountID(realA.ID).
		Save(ctx)
	require.NoError(t, err)
	shadow, err := client.Account.Create().
		SetName("Parent (Spark)").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetParentAccountID(parent.ID).
		SetQuotaDimension(service.QuotaDimensionSpark).
		Save(ctx)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = client.Account.Delete().Where(dbaccount.IDIn(shadow.ID, parent.ID)).Exec(context.Background())
		_ = client.RealAccount.DeleteOneID(realA.ID).Exec(context.Background())
		_ = client.RealAccount.DeleteOneID(realB.ID).Exec(context.Background())
	})

	// Simulate an administrator committing a family move to realB after an
	// observation previously saw shadow unbound with parent pointing at realA.
	require.NoError(t, repo.AttachAccount(ctx, realB.ID, parent.ID))

	resolved, err := repo.ResolveShadowRealAccount(ctx, shadow.ID, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, realB.ID, resolved.ID)

	reloadedParent, err := client.Account.Get(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, reloadedParent.RealAccountID)
	require.Equal(t, realB.ID, *reloadedParent.RealAccountID)

	reloadedShadow, err := client.Account.Get(ctx, shadow.ID)
	require.NoError(t, err)
	require.NotNil(t, reloadedShadow.RealAccountID)
	require.Equal(t, realB.ID, *reloadedShadow.RealAccountID)
}
