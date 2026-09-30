//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type claudeResetReconcileRepo struct {
	mockAccountRepoForGemini
	clearRateLimitCalls int
	observedLimitedAt   time.Time
	observedResetAt     time.Time
	rearmed             bool // a newer 429 replaced the observed generation
	modelLimitScope     string
	modelLimitResetAt   time.Time
	modelLimitReason    string
}

// ClearRateLimit would also drop overload; reconciliation must never use it.
func (r *claudeResetReconcileRepo) ClearRateLimit(context.Context, int64) error {
	panic("ClearRateLimit must not be used by Claude reset reconciliation")
}

func (r *claudeResetReconcileRepo) ClearAnthropicRateLimitIfObserved(_ context.Context, id int64, limitedAt, resetAt time.Time) (bool, error) {
	r.clearRateLimitCalls++
	r.observedLimitedAt, r.observedResetAt = limitedAt, resetAt
	if r.rearmed {
		return false, nil
	}
	current := r.accountsByID[id]
	return current != nil && current.RateLimitedAt != nil && current.RateLimitResetAt != nil &&
		current.RateLimitedAt.Equal(limitedAt) && current.RateLimitResetAt.Equal(resetAt), nil
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
	limitedAt := time.Now().Add(-time.Hour)
	future := time.Now().Add(3 * time.Hour)
	a := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, RateLimitedAt: &limitedAt, RateLimitResetAt: &future, Extra: map[string]any{}}
	if fableLimited {
		a.Extra[modelRateLimitsKey] = map[string]any{
			anthropicFableRateLimitKey: map[string]any{
				"rate_limited_at":     limitedAt.UTC().Format(time.RFC3339),
				"rate_limit_reset_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
			},
		}
	}
	return a
}

func TestReconcileClaudeResetClearsAccountCooldownWhenWindowsRecovered(t *testing.T) {
	account := claudeResetLimitedAccount(false)
	svc, repo, blocker := newClaudeResetReconcileService(account)
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 0}, SevenDay: &UsageProgress{Utilization: 41}}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour", "seven_day"}, usage))
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, *account.RateLimitedAt, repo.observedLimitedAt, "clear must be fenced on the observed 429 generation")
	require.Equal(t, *account.RateLimitResetAt, repo.observedResetAt)
	require.Empty(t, repo.modelLimitScope)
	require.Equal(t, []int64{7}, blocker.cleared)
}

func TestReconcileClaudeResetKeepsRearmedCooldown(t *testing.T) {
	account := claudeResetLimitedAccount(false)
	svc, repo, blocker := newClaudeResetReconcileService(account)
	repo.rearmed = true
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 0}, SevenDay: &UsageProgress{Utilization: 41}}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour"}, usage))
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Empty(t, blocker.cleared, "a 429 re-armed during reconciliation must keep the scheduling block")
}

func TestReconcileClaudeResetKeepsCooldownWhileSevenDayStillExhausted(t *testing.T) {
	// 5h-only reset while the weekly window is still at its limit.
	account := claudeResetLimitedAccount(false)
	svc, repo, blocker := newClaudeResetReconcileService(account)
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 0}, SevenDay: &UsageProgress{Utilization: 100}}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour"}, usage))
	require.Zero(t, repo.clearRateLimitCalls)
	require.Empty(t, blocker.cleared)
}

func TestReconcileClaudeResetRequiresFreshFiveHourWindow(t *testing.T) {
	account := claudeResetLimitedAccount(false)
	svc, repo, _ := newClaudeResetReconcileService(account)

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour"}, &UsageInfo{}))
	require.Zero(t, repo.clearRateLimitCalls)
}

func TestReconcileClaudeResetFiveHourOnlyKeepsFableLimit(t *testing.T) {
	account := claudeResetLimitedAccount(true)
	svc, repo, _ := newClaudeResetReconcileService(account)
	usage := &UsageInfo{
		FiveHour:      &UsageProgress{Utilization: 0},
		SevenDay:      &UsageProgress{Utilization: 30},
		SevenDayFable: &UsageProgress{Utilization: 5},
	}

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour"}, usage))
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Empty(t, repo.modelLimitScope, "Fable 7d_oi limit must survive a reset that did not clear it")
}

func TestReconcileClaudeResetExpiresFableLimitWhenCleared(t *testing.T) {
	account := claudeResetLimitedAccount(true)
	account.RateLimitResetAt = nil
	svc, repo, blocker := newClaudeResetReconcileService(account)
	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 20}, SevenDayFable: &UsageProgress{Utilization: 3}}
	before := time.Now()

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"seven_day_overage_included"}, usage))
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

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"seven_day_overage_included"}, &UsageInfo{FiveHour: &UsageProgress{}}))
	require.Empty(t, repo.modelLimitScope)
	require.Empty(t, blocker.cleared)
}

func TestReconcileClaudeResetIgnoresNonAnthropicOAuth(t *testing.T) {
	account := claudeResetLimitedAccount(false)
	account.Type = AccountTypeSetupToken
	svc, repo, _ := newClaudeResetReconcileService(account)

	require.NoError(t, svc.ReconcileClaudeReset(context.Background(), account, []string{"five_hour"}, &UsageInfo{FiveHour: &UsageProgress{}}))
	require.Zero(t, repo.clearRateLimitCalls)
}

type claudeResetUsageFetcherStub struct {
	mu    sync.Mutex
	calls int
	resp  *ClaudeUsageResponse
	// first, when set, answers only the first call and blocks until released.
	first        *ClaudeUsageResponse
	firstStarted chan struct{}
	releaseFirst chan struct{}
	onFetch      func()
}

func (f *claudeResetUsageFetcherStub) FetchUsage(context.Context, string, string) (*ClaudeUsageResponse, error) {
	return f.FetchUsageWithOptions(context.Background(), nil)
}

func (f *claudeResetUsageFetcherStub) FetchUsageWithOptions(context.Context, *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if f.onFetch != nil {
		f.onFetch()
	}
	if call == 1 && f.first != nil {
		close(f.firstStarted)
		<-f.releaseFirst
		return f.first, nil
	}
	return f.resp, nil
}

func (f *claudeResetUsageFetcherStub) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newClaudeResetUsageService(account *Account, fetcher *claudeResetUsageFetcherStub, cache *UsageCache) *AccountUsageService {
	repo := &claudeResetReconcileRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}}
	return NewAccountUsageService(repo, nil, fetcher, nil, nil, nil, nil, nil, cache, nil, nil)
}

func claudeResetUsageAccount() *Account {
	return &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "synthetic", "scope": "user:profile"}, Extra: map[string]any{}}
}

func claudeResetUsageResponse(fiveHour float64) *ClaudeUsageResponse {
	resp := &ClaudeUsageResponse{}
	resp.FiveHour.Utilization = fiveHour
	return resp
}

func TestRefreshClaudeUsageAfterResetBypassesCachedUsage(t *testing.T) {
	fetcher := &claudeResetUsageFetcherStub{resp: claudeResetUsageResponse(2)}
	cache := NewUsageCache()
	cache.apiCache.Store(int64(7), &apiUsageCache{response: claudeResetUsageResponse(100), timestamp: time.Now()})
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := newClaudeResetUsageService(claudeResetUsageAccount(), fetcher, cache)

	cached, err := svc.GetUsage(context.Background(), 7, true)
	require.NoError(t, err)
	require.Equal(t, float64(100), cached.FiveHour.Utilization, "precondition: a forced GetUsage still serves the cache")
	require.Zero(t, fetcher.callCount())

	usage, err := svc.RefreshClaudeUsageAfterReset(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.callCount())
	require.Equal(t, float64(2), usage.FiveHour.Utilization)

	after, err := svc.GetUsage(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, float64(2), after.FiveHour.Utilization, "the fresh response must replace the cached one")
	require.Equal(t, 1, fetcher.callCount())
}

func TestRefreshClaudeUsageAfterResetDoesNotJoinPreResetQuery(t *testing.T) {
	fetcher := &claudeResetUsageFetcherStub{
		resp:         claudeResetUsageResponse(2),
		first:        claudeResetUsageResponse(100),
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	cache := NewUsageCache()
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := newClaudeResetUsageService(claudeResetUsageAccount(), fetcher, cache)

	type result struct {
		usage *UsageInfo
		err   error
	}
	preReset := make(chan result, 1)
	go func() {
		usage, err := svc.GetUsage(context.Background(), 7)
		preReset <- result{usage, err}
	}()
	<-fetcher.firstStarted

	refreshed := make(chan result, 1)
	go func() {
		usage, err := svc.RefreshClaudeUsageAfterReset(context.Background(), 7)
		refreshed <- result{usage, err}
	}()
	var fresh result
	select {
	case fresh = <-refreshed:
	case <-time.After(5 * time.Second):
		close(fetcher.releaseFirst)
		t.Fatal("reconciliation waited on the pre-reset query")
	}
	require.NoError(t, fresh.err)
	require.Equal(t, float64(2), fresh.usage.FiveHour.Utilization, "reconciliation must not reuse the pre-reset query")

	close(fetcher.releaseFirst)
	old := <-preReset
	require.NoError(t, old.err)
	require.Equal(t, float64(2), old.usage.FiveHour.Utilization, "the slower pre-reset query must yield to the newer response")

	after, err := svc.GetUsage(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, float64(2), after.FiveHour.Utilization, "the pre-reset response must not overwrite the cache")
	require.Equal(t, 2, fetcher.callCount())
}

func TestRefreshClaudeUsageAfterResetIgnoresPassiveFableBackfill(t *testing.T) {
	account := claudeResetUsageAccount()
	account.Extra["passive_usage_7d_oi_utilization"] = 0.05
	account.Extra["passive_usage_7d_oi_reset"] = float64(time.Now().Add(48 * time.Hour).Unix())
	cache := NewUsageCache()
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := newClaudeResetUsageService(account, &claudeResetUsageFetcherStub{resp: claudeResetUsageResponse(2)}, cache)

	display, err := svc.GetUsage(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, display.SevenDayFable, "precondition: the display path backfills Fable from passive samples")

	usage, err := svc.RefreshClaudeUsageAfterReset(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, usage.SevenDayFable, "passive samples predate the reset and are not evidence")
}

func TestClaudeResetReconcilerKeepsLimitsRearmedDuringUsageQuery(t *testing.T) {
	g1 := claudeResetLimitedAccount(true)
	g1.Credentials = map[string]any{"access_token": "synthetic", "scope": "user:profile"}
	repo := &claudeResetReconcileRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{7: g1}}}
	rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	blocker := &claudeResetBlockRecorder{}
	rateLimit.runtimeBlocker = blocker
	fresh := claudeResetUsageResponse(0)
	fresh.SevenDayOverageIncluded.Utilization = 1
	fresh.SevenDayOverageIncluded.ResetsAt = time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fetcher := &claudeResetUsageFetcherStub{resp: fresh}
	// New 429s (account-level and Fable) land while the usage query runs.
	fetcher.onFetch = func() {
		g2 := claudeResetLimitedAccount(true)
		g2.Credentials = g1.Credentials
		rearmedAt := time.Now().Add(time.Minute)
		rearmedReset := time.Now().Add(4 * time.Hour)
		g2.RateLimitedAt, g2.RateLimitResetAt = &rearmedAt, &rearmedReset
		g2.Extra[modelRateLimitsKey].(map[string]any)[anthropicFableRateLimitKey].(map[string]any)["rate_limited_at"] = rearmedAt.UTC().Format(time.RFC3339)
		repo.accountsByID[7] = g2
	}
	cache := NewUsageCache()
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	usage := NewAccountUsageService(repo, nil, fetcher, nil, nil, nil, nil, nil, cache, nil, nil)

	newClaudeResetReconciler(usage, rateLimit)(context.Background(), 7, []string{"five_hour", "seven_day", "seven_day_overage_included"})

	require.Equal(t, 1, fetcher.callCount())
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, *g1.RateLimitedAt, repo.observedLimitedAt, "the clear must target the generation seen before the usage query")
	require.Empty(t, repo.modelLimitScope, "a Fable limit re-armed during the query must survive")
	require.Empty(t, blocker.cleared)
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
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.SetResetReconciler(func(reconcileCtx context.Context, id int64, cleared []string) {
				require.Equal(t, int64(1), id)
				cancel() // the admin request goes away mid-reconciliation
				require.NoError(t, reconcileCtx.Err())
				_, hasDeadline := reconcileCtx.Deadline()
				require.True(t, hasDeadline)
				calls = append(calls, cleared)
			})
			_, err := s.Redeem(ctx, 1, "op-reconcile")
			require.NoError(t, err)
			if tc.want == nil {
				require.Empty(t, calls)
				return
			}
			require.Equal(t, [][]string{tc.want}, calls)
		})
	}
}
