package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestResetPolicyWeeklyUsageOnly(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		weekly   float64
		duration int64
		resetAt  int64
		want     bool
	}{
		{"below", 19.99, 604800, 0, false},
		{"equal", 20, 604800, 0, false},
		{"above", 20.01, 604800, 0, true},
		{"expired", 100, 604800, now.Unix(), false},
		{"5h excluded", 100, 18000, 0, false},
		{"unknown duration", 100, 0, 0, false},
		{"invalid", math.NaN(), 604800, 0, false},
		{"infinite", math.Inf(1), 604800, 0, false},
		{"out of range", 101, 604800, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 18000},
				SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: tc.weekly, LimitWindowSeconds: tc.duration, ResetAt: tc.resetAt},
			}}
			require.Equal(t, tc.want, openAIResetUsageEligible(usage, now))
			usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow = usage.RateLimit.SecondaryWindow, nil
			require.Equal(t, tc.want, openAIResetUsageEligible(usage, now), "7d can be the primary slot")
		})
	}
	require.False(t, openAIResetUsageEligible(nil, now))
	require.False(t, openAIResetUsageEligible(&OpenAIQuotaUsage{AdditionalRateLimits: []OpenAIAdditionalRateLimit{
		{RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 604800}}},
	}}, now))
}

func TestResetReservationLowUsageBackoffAndRestart(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 20)
	require.NoError(t, err)
	now := view.ExecuteAt
	s.now = func() time.Time { return now }
	quota.usage.RateLimit.PrimaryWindow.UsedPercent = 20
	quota.usage.RateLimit.SecondaryWindow = &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 18000}
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "pending", r.Status)
	require.Nil(t, r.AttemptedAt)
	require.Equal(t, "OPENAI_RESET_USAGE_TOO_LOW", r.ErrorCode)
	queries := quota.queryCalls.Load()
	// A new service instance uses the persisted next check, not an in-memory timer.
	restarted := NewOpenAIResetReservationService(repo, s.accounts, quota, s.locks, s.idempotency, s.recoverer)
	restarted.now = s.now
	now = now.Add(10*time.Minute - time.Nanosecond)
	for i := 0; i < 20; i++ {
		_, err = restarted.Process(ctx, 12)
		require.NoError(t, err)
	}
	require.Equal(t, queries, quota.queryCalls.Load())
	require.Zero(t, quota.resetCalls.Load())
	quota.usage.RateLimit.PrimaryWindow.UsedPercent = 20.01
	now = now.Add(time.Nanosecond)
	_, err = restarted.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, int32(1), quota.resetCalls.Load())
}

func TestResetReservationNoopRetryAndUncertainty(t *testing.T) {
	for _, scenario := range []string{"noop only", "earlier timeout", "card disappeared"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, _, quota := reservationFixture(t)
			ctx := context.Background()
			s.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: "nothingToReset"}
			view, err := s.Create(ctx, 12, 40)
			require.NoError(t, err)
			now := view.ExecuteAt
			s.now = func() time.Time { return now }
			quota.failFirst = scenario == "earlier timeout"
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			require.Equal(t, int32(1), quota.resetCalls.Load())
			now = now.Add(10 * time.Minute)
			time.Sleep(2 * time.Millisecond) // idempotency coordinator's independent clock
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			require.Len(t, quota.resetArgs, 2)
			require.Equal(t, quota.resetArgs[0], quota.resetArgs[1])
			r, _ := repo.Get(ctx, 12)
			require.Nil(t, r.ConsumedAt)
			require.Equal(t, scenario != "earlier timeout", r.OnlyNoopAttempts)
			if scenario == "card disappeared" {
				quota.usage.autoResetCandidates = quota.usage.autoResetCandidates[:1]
				quota.usage.RateLimitResetCredits.AvailableCount = 1
			}
			now = view.ExpiresAt.Add(-openAIResetReservationCutoff)
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			r, _ = repo.Get(ctx, 12)
			want := "unknown"
			if scenario == "noop only" {
				want = "expired"
			}
			require.Equal(t, want, r.Status)
			require.Equal(t, int32(2), quota.resetCalls.Load())
		})
	}
}

func TestResetReservationLeadPolicy(t *testing.T) {
	for _, lead := range []int{0, 20, 40, 1440, 5, 10, 30, 1441} {
		s, _, _, _ := reservationFixture(t)
		view, err := s.Create(context.Background(), 12, lead)
		if lead == 0 || (lead >= 20 && lead <= 1440 && lead%20 == 0) {
			require.NoError(t, err)
			if lead == 0 {
				require.Equal(t, 20*time.Minute, view.ExpiresAt.Sub(view.ExecuteAt))
			}
		} else {
			require.Equal(t, "OPENAI_RESET_LEAD_INVALID", infraerrors.Reason(err))
		}
	}
}

func TestResetCreditPolicySharedAcrossAliasesAndManualTargetedPaths(t *testing.T) {
	account := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "shared-subscription"}}
	alias := *account
	alias.ID = 101
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{100: account, 101: &alias}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		OpenAITokenCacheKey(account): "fake-token", OpenAITokenCacheKey(&alias): "fake-token",
	}}
	posts := 0
	weekly := 20.0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			_, _ = w.Write([]byte(`{"code":"nothing_to_reset","windows_reset":0}`))
		} else {
			_, _ = fmt.Fprintf(w, `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":18000},"secondary_window":{"used_percent":%g,"limit_window_seconds":604800}}}`, weekly)
		}
	}))
	defer srv.Close()
	locks := &fakeLeaderLockCache{}
	reservations := NewOpenAIResetReservationService(&reservationTestRepo{}, repo, nil, locks, nil, nil)
	svc := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, tokenCache, nil), newQuotaRedirectingFactory(srv), nil)
	svc.reservations = reservations
	reconciler := &openAIQuotaResetReconcilerRecorder{}
	svc.SetResetReconciler(reconciler)
	_, err := svc.ResetCreditTargeted(context.Background(), 100, "credit", "request")
	require.Equal(t, "OPENAI_RESET_USAGE_TOO_LOW", infraerrors.Reason(err))
	require.Zero(t, posts)
	weekly = 30
	_, err = svc.ResetCreditTargeted(context.Background(), 100, "credit", "request")
	require.Equal(t, "OPENAI_RESET_NOTHING_TO_RESET", infraerrors.Reason(err))
	_, err = svc.ResetCredit(context.Background(), 101)
	require.Equal(t, "OPENAI_RESET_COOLDOWN", infraerrors.Reason(err))
	require.Equal(t, 1, posts)
	require.Empty(t, reconciler.accountIDs)
}

func TestResetReservationLocalRefusalDoesNotBecomeUnknown(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	s.quota = &resetPolicyRefusalQuota{autoResetTestQuota: quota}
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 20)
	require.NoError(t, err)
	s.now = func() time.Time { return view.ExecuteAt }
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "pending", r.Status)
	require.Nil(t, r.AttemptedAt)
	require.Equal(t, "OPENAI_RESET_COOLDOWN", r.ErrorCode)
	s.now = func() time.Time { return view.ExpiresAt }
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ = repo.Get(ctx, 12)
	require.Equal(t, "expired", r.Status)
}

type resetPolicyRefusalQuota struct{ *autoResetTestQuota }

func (q *resetPolicyRefusalQuota) ResetCreditTargeted(context.Context, int64, string, string) (*OpenAIQuotaResetResult, error) {
	return nil, infraerrors.Conflict("OPENAI_RESET_COOLDOWN", "cooling down")
}

func TestResetReservationExistingThirtyMinuteAppointmentAndLowUsageExpiry(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 20)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	r.ExecuteAt = view.ExpiresAt.Add(-30 * time.Minute) // persisted by the previous version
	require.NoError(t, s.save(ctx, r))
	s.now = func() time.Time { return r.ExecuteAt }
	quota.usage.RateLimit.PrimaryWindow.UsedPercent = 20
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Zero(t, quota.resetCalls.Load())
	r, _ = repo.Get(ctx, 12)
	require.Equal(t, "OPENAI_RESET_USAGE_TOO_LOW", r.ErrorCode)
	s.now = func() time.Time { return view.ExpiresAt }
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ = repo.Get(ctx, 12)
	require.Equal(t, "expired", r.Status)
	require.Nil(t, r.AttemptedAt)
}

func TestResetPolicySchedulerIgnoresFiveHourRedemptionAndHonorsWeeklyFloor(t *testing.T) {
	now := time.Now()
	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		OpenAIAutoResetCreditEnabledExtraKey:     true,
		OpenAIAutoResetCredit7dThresholdExtraKey: 0.1,
		"auto_pause_5h_disabled":                 true, "auto_pause_7d_disabled": true,
		"codex_5h_used_percent": 100.0, "codex_7d_used_percent": 20.0,
		"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
		"codex_7d_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
		"codex_usage_updated_at": now.Format(time.RFC3339),
	}}
	paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
	require.False(t, paused, "a threshold below the weekly floor must not strand the account")
	account.Extra["codex_7d_used_percent"] = 20.01
	paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
	require.True(t, paused)
	require.Equal(t, "quota_auto_reset_pending_7d", decision.reason)
	account.Extra["codex_7d_used_percent"] = 20.0
	account.Extra["auto_pause_5h_disabled"] = false
	account.Extra["auto_pause_5h_threshold"] = 0.8
	account.Extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusAvailable, AvailableCount: 2, CheckedAt: now.Format(time.RFC3339),
	}
	paused, decision = shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
	require.True(t, paused, "available credits cannot bypass ordinary 5h pause")
	require.Equal(t, "5h", decision.window)
	require.Empty(t, decision.reason, "ordinary pause must not request automatic redemption")
}

func TestResetPolicyThresholdNoopPreservesCreditAndBacksOff(t *testing.T) {
	auto, repo, quota := newResetThresholdTestFixture(t, nil, 1)
	quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}
	auto.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: "nothing_to_reset"}
	require.NoError(t, auto.evaluateAccount(context.Background(), 501))
	state := repo.stateForTest()
	require.Equal(t, OpenAIAutoResetStatusAvailable, state.Status)
	require.Equal(t, 1, state.AvailableCount)
	require.Equal(t, "OPENAI_RESET_NOTHING_TO_RESET", state.ErrorCode)
	queries := quota.queryCalls.Load()
	for i := 0; i < 10; i++ {
		require.NoError(t, auto.evaluateAccount(context.Background(), 501))
	}
	require.Equal(t, int32(1), quota.resetCalls.Load())
	require.Equal(t, queries, quota.queryCalls.Load())
}

func TestResetPolicyUnidentifiedWeeklyWindowBacksOffWithoutFailure(t *testing.T) {
	auto, repo, quota := newResetThresholdTestFixture(t, nil, 1)
	quota.usage.RateLimit.SecondaryWindow.LimitWindowSeconds = 0
	quota.usage.RateLimit.PrimaryWindow = quota.usage.RateLimit.SecondaryWindow
	quota.usage.RateLimit.SecondaryWindow = nil
	require.NoError(t, auto.evaluateAccount(context.Background(), 501))
	state := repo.stateForTest()
	require.Equal(t, OpenAIAutoResetStatusAvailable, state.Status)
	require.Equal(t, "OPENAI_RESET_USAGE_TOO_LOW", state.ErrorCode)
	require.NoError(t, auto.evaluateAccount(context.Background(), 501))
	require.Equal(t, int32(1), quota.queryCalls.Load())
	require.Zero(t, quota.resetCalls.Load())
}

type resetPolicySlowNoopQuota struct {
	*autoResetTestQuota
	now *time.Time
}

func (q *resetPolicySlowNoopQuota) ResetCreditTargeted(ctx context.Context, id int64, credit, request string) (*OpenAIQuotaResetResult, error) {
	result, err := q.autoResetTestQuota.ResetCreditTargeted(ctx, id, credit, request)
	*q.now = q.now.Add(8 * time.Second)
	result.Code = "nothing_to_reset"
	return result, err
}

func TestResetReservationCooldownStartsAfterSlowAttempt(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 20)
	require.NoError(t, err)
	now := view.ExecuteAt
	s.now = func() time.Time { return now }
	s.quota = &resetPolicySlowNoopQuota{autoResetTestQuota: quota, now: &now}
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, view.ExecuteAt.Add(10*time.Minute+8*time.Second), *r.NextCheckAt)
	now = view.ExecuteAt.Add(10 * time.Minute)
	queries := quota.queryCalls.Load()
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, queries, quota.queryCalls.Load(), "do not check before the upstream cooldown can expire")
	time.Sleep(2 * time.Millisecond)
	now = *r.NextCheckAt
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, int32(2), quota.resetCalls.Load(), "retain the second attempt before expiry")
}

func TestResetReservationNoopDoesNotEraseCrashOrUnavailableEvidence(t *testing.T) {
	for _, scenario := range []string{"expired", "disabled", "crashed"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, accounts, quota := reservationFixture(t)
			ctx := context.Background()
			view, err := s.Create(ctx, 12, 40)
			require.NoError(t, err)
			now := view.ExecuteAt
			s.now = func() time.Time { return now }
			s.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: "nothing_to_reset"}
			if scenario == "crashed" {
				r, _ := repo.Get(ctx, 12)
				r.AttemptedAt, r.Status, r.OnlyNoopAttempts = &now, "resetting", false
				require.NoError(t, s.save(ctx, r))
				now = now.Add(10 * time.Minute)
			}
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			now = view.ExpiresAt.Add(-openAIResetReservationCutoff)
			if scenario == "expired" {
				now = view.ExpiresAt
			}
			if scenario == "disabled" {
				accounts.account.Status = StatusDisabled
			}
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			r, _ := repo.Get(ctx, 12)
			require.Equal(t, "unknown", r.Status)
			require.Nil(t, r.ConsumedAt)
		})
	}
}
