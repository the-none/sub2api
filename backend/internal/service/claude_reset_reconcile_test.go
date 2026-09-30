//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type claudeResetReconcileRepo struct {
	mockAccountRepoForGemini
	clearRateLimitCalls int
	modelLimitScope     string
	modelLimitResetAt   time.Time
	modelLimitReason    string
}

func (r *claudeResetReconcileRepo) ClearRateLimit(context.Context, int64) error {
	r.clearRateLimitCalls++
	return nil
}

func (r *claudeResetReconcileRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelLimitScope, r.modelLimitResetAt = scope, resetAt
	if len(reason) > 0 {
		r.modelLimitReason = reason[0]
	}
	return nil
}

type claudeResetBlockRecorder struct {
	cleared []int64
}

func (b *claudeResetBlockRecorder) BlockAccountScheduling(*Account, time.Time, string) {}
func (b *claudeResetBlockRecorder) ClearAccountSchedulingBlock(id int64) {
	b.cleared = append(b.cleared, id)
}

func newClaudeResetReconcileService(account *Account) (*RateLimitService, *claudeResetReconcileRepo, *claudeResetBlockRecorder) {
	repo := &claudeResetReconcileRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	blocker := &claudeResetBlockRecorder{}
	svc.runtimeBlocker = blocker
	return svc, repo, blocker
}

func claudeResetLimitedAccount(fableLimited bool) *Account {
	future := time.Now().Add(3 * time.Hour)
	a := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, RateLimitResetAt: &future, Extra: map[string]any{}}
	if fableLimited {
		a.Extra[modelRateLimitsKey] = map[string]any{
			anthropicFableRateLimitKey: map[string]any{"rate_limit_reset_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)},
		}
	}
	return a
}

func TestReconcileClaudeResetClearsAccountCooldownWhenWindowsRecovered(t *testing.T) {
	svc, repo, blocker := newClaudeResetReconcileService(claudeResetLimitedAccount(false))
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 0}, SevenDay: &UsageProgress{Utilization: 41}}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"five_hour", "seven_day"}, usage))
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Empty(t, repo.modelLimitScope)
	require.Equal(t, []int64{7}, blocker.cleared)
}

func TestReconcileClaudeResetKeepsCooldownWhileSevenDayStillExhausted(t *testing.T) {
	// 5h-only reset while the weekly window is still at its limit.
	svc, repo, blocker := newClaudeResetReconcileService(claudeResetLimitedAccount(false))
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 0}, SevenDay: &UsageProgress{Utilization: 100}}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"five_hour"}, usage))
	require.Zero(t, repo.clearRateLimitCalls)
	require.Empty(t, blocker.cleared)
}

func TestReconcileClaudeResetRequiresFreshFiveHourWindow(t *testing.T) {
	svc, repo, _ := newClaudeResetReconcileService(claudeResetLimitedAccount(false))

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"five_hour"}, &UsageInfo{}))
	require.Zero(t, repo.clearRateLimitCalls)
}

func TestReconcileClaudeResetFiveHourOnlyKeepsFableLimit(t *testing.T) {
	svc, repo, _ := newClaudeResetReconcileService(claudeResetLimitedAccount(true))
	usage := &UsageInfo{
		FiveHour:      &UsageProgress{Utilization: 0},
		SevenDay:      &UsageProgress{Utilization: 30},
		SevenDayFable: &UsageProgress{Utilization: 5},
	}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"five_hour"}, usage))
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Empty(t, repo.modelLimitScope, "Fable 7d_oi limit must survive a reset that did not clear it")
}

func TestReconcileClaudeResetExpiresFableLimitWhenCleared(t *testing.T) {
	account := claudeResetLimitedAccount(true)
	account.RateLimitResetAt = nil
	svc, repo, blocker := newClaudeResetReconcileService(account)
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 20}, SevenDayFable: &UsageProgress{Utilization: 3}}
	before := time.Now()

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"seven_day_overage_included"}, usage))
	require.Zero(t, repo.clearRateLimitCalls)
	require.Equal(t, anthropicFableRateLimitKey, repo.modelLimitScope)
	require.False(t, repo.modelLimitResetAt.After(time.Now()))
	require.False(t, repo.modelLimitResetAt.Before(before))
	require.Equal(t, claudeResetFableClearReason, repo.modelLimitReason)
	require.Equal(t, []int64{7}, blocker.cleared)
}

func TestReconcileClaudeResetKeepsFableLimitWithoutFreshEvidence(t *testing.T) {
	account := claudeResetLimitedAccount(true)
	account.RateLimitResetAt = nil
	svc, repo, blocker := newClaudeResetReconcileService(account)

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"seven_day_overage_included"}, &UsageInfo{FiveHour: &UsageProgress{}}))
	require.Empty(t, repo.modelLimitScope)
	require.Empty(t, blocker.cleared)
}

func TestReconcileClaudeResetIgnoresNonAnthropicOAuth(t *testing.T) {
	account := claudeResetLimitedAccount(false)
	account.Type = AccountTypeSetupToken
	svc, repo, _ := newClaudeResetReconcileService(account)

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), 7, []string{"five_hour"}, &UsageInfo{FiveHour: &UsageProgress{}}))
	require.Zero(t, repo.clearRateLimitCalls)
}

type claudeResetUsageFetcherStub struct {
	calls int
	resp  *ClaudeUsageResponse
}

func (f *claudeResetUsageFetcherStub) FetchUsage(context.Context, string, string) (*ClaudeUsageResponse, error) {
	return f.FetchUsageWithOptions(context.Background(), nil)
}

func (f *claudeResetUsageFetcherStub) FetchUsageWithOptions(context.Context, *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	f.calls++
	return f.resp, nil
}

func TestRefreshClaudeUsageAfterResetBypassesCachedUsage(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "synthetic", "scope": "user:profile"}}
	repo := &claudeResetReconcileRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{7: account}}}
	fresh := &ClaudeUsageResponse{}
	fresh.FiveHour.Utilization = 2
	fetcher := &claudeResetUsageFetcherStub{resp: fresh}
	cache := NewUsageCache()
	stale := &ClaudeUsageResponse{}
	stale.FiveHour.Utilization = 100
	cache.apiCache.Store(int64(7), &apiUsageCache{response: stale, timestamp: time.Now()})
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := NewAccountUsageService(repo, nil, fetcher, nil, nil, nil, nil, nil, cache, nil, nil)

	cached, err := svc.GetUsage(context.Background(), 7, true)
	require.NoError(t, err)
	require.Equal(t, float64(100), cached.FiveHour.Utilization, "precondition: a forced GetUsage still serves the cache")
	require.Zero(t, fetcher.calls)

	usage, err := svc.RefreshClaudeUsageAfterReset(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.calls)
	require.Equal(t, float64(2), usage.FiveHour.Utilization)
}

func TestClaudeResetRedeemRunsReconcilerOnlyForReset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim string
		want  []string
	}{
		{"reset", `{"result":"reset","cleared":["five_hour","seven_day"]}`, []string{"five_hour", "seven_day"}},
		{"not limited", `{"result":"not_limited"}`, nil},
		{"unknown", `{"result":"surprise"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newRedeemService(t, &redeemFake{claim: tc.claim})
			var calls [][]string
			s.SetResetReconciler(func(ctx context.Context, id int64, cleared []string) {
				require.Equal(t, int64(1), id)
				require.NoError(t, ctx.Err())
				calls = append(calls, cleared)
			})
			ctx, cancel := context.WithCancel(context.Background())
			_, err := s.Redeem(ctx, 1, "op-reconcile")
			cancel()
			require.NoError(t, err)
			if tc.want == nil {
				require.Empty(t, calls)
				return
			}
			require.Equal(t, [][]string{tc.want}, calls)
		})
	}
}
