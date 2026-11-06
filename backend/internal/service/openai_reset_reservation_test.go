package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type reservationTestRepo struct {
	mu           sync.Mutex
	record       *OpenAIResetReservation
	failConsumed bool
}

func cloneReservation(r *OpenAIResetReservation) *OpenAIResetReservation {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

func (r *reservationTestRepo) Get(_ context.Context, id int64) (*OpenAIResetReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record != nil && r.record.AccountID != id {
		return nil, nil
	}
	return cloneReservation(r.record), nil
}

func (r *reservationTestRepo) GetActiveByIdentity(_ context.Context, identity string) (*OpenAIResetReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record.active() && r.record.IdentityHash == identity {
		return cloneReservation(r.record), nil
	}
	return nil, nil
}

func (r *reservationTestRepo) CompareAndSwap(_ context.Context, next *OpenAIResetReservation, expected int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if next.Status == "consumed" && r.failConsumed {
		r.failConsumed = false
		return false, errors.New("simulated outcome write failure")
	}
	if (r.record == nil && expected != 0) || (r.record != nil && r.record.Version != expected) {
		return false, nil
	}
	r.record = cloneReservation(next)
	return true, nil
}

func (r *reservationTestRepo) ListRunnable(context.Context, int64, int) ([]int64, error) {
	return nil, nil
}

type reservationTestRecovery struct{ fail bool }

func (r *reservationTestRecovery) RecoverAccountState(context.Context, int64, AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	if r.fail {
		return nil, errors.New("recovery unavailable")
	}
	return &SuccessfulTestRecoveryResult{}, nil
}

type reservationOutcomeQuota struct {
	*autoResetTestQuota
	outcome  string
	cacheErr error
}

func (q *reservationOutcomeQuota) ResetCreditTargeted(ctx context.Context, id int64, credit, request string) (*OpenAIQuotaResetResult, error) {
	result, err := q.autoResetTestQuota.ResetCreditTargeted(ctx, id, credit, request)
	if result != nil {
		result.Code = q.outcome
	}
	return result, err
}

func (q *reservationOutcomeQuota) CachePostResetSnapshot(context.Context, int64, *OpenAIQuotaUsage) error {
	return q.cacheErr
}

func reservationFixture(t *testing.T) (*OpenAIResetReservationService, *reservationTestRepo, *autoResetTestAccountRepo, *autoResetTestQuota) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	repo := &reservationTestRepo{}
	accounts := &autoResetTestAccountRepo{account: &Account{
		ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: false, Credentials: map[string]any{"chatgpt_account_id": "subscription-a"},
	}}
	quota := &autoResetTestQuota{usage: &OpenAIQuotaUsage{
		AccountID: "subscription-a", FetchedAt: now.Unix(),
		RateLimit:             &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 10, LimitWindowSeconds: 18000}},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 2},
		autoResetCandidates: []openAIAutoResetCreditCandidate{
			{ID: "second-secret", ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)},
			{ID: "first-secret", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)},
		},
	}}
	cfg := DefaultIdempotencyConfig()
	cfg.FailedRetryBackoff = time.Millisecond
	cfg.ObserveOnly = false
	s := NewOpenAIResetReservationService(repo, accounts, quota, &fakeLeaderLockCache{},
		NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg), &reservationTestRecovery{})
	s.now = func() time.Time { return now }
	return s, repo, accounts, quota
}

func TestResetReservationDeadlineIndependentOfThresholdAndScheduling(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 30)
	require.NoError(t, err)
	require.Equal(t, "pending", view.Status)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "hash")
	require.NotContains(t, string(encoded), "secret")
	handled, err := s.Process(ctx, 12)
	require.NoError(t, err)
	require.False(t, handled, "pending appointments must preserve legacy quota-state maintenance")
	require.Equal(t, int32(1), quota.queryCalls.Load(), "waiting must not repeatedly query upstream")
	require.Zero(t, quota.resetCalls.Load())
	s.now = func() time.Time { return view.ExecuteAt }
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, int32(1), quota.resetCalls.Load())
	require.Equal(t, "first-secret", quota.resetArgs[0][0])
	current, _ := repo.Get(ctx, 12)
	require.Equal(t, "completed", current.Status)
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, int32(1), quota.resetCalls.Load())
}

func TestResetReservationCancelAndRebookRetainsCreditOperation(t *testing.T) {
	s, _, _, quota := reservationFixture(t)
	ctx := context.Background()
	first, err := s.Create(ctx, 12, 30)
	require.NoError(t, err)
	_, err = s.Cancel(ctx, 12, first.ID)
	require.NoError(t, err)
	second, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	_, err = s.Cancel(ctx, 12, first.ID)
	require.Equal(t, "OPENAI_RESET_RESERVATION_CHANGED", infraerrors.Reason(err))
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, int32(1), quota.resetCalls.Load())
	_, err = s.Cancel(ctx, 12, second.ID)
	require.Equal(t, "OPENAI_RESET_ALREADY_STARTED", infraerrors.Reason(err))
}

func TestResetReservationUnavailableNeverSelectsNextCredit(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not attempted", true: "ambiguous attempt"}[attempted], func(t *testing.T) {
			s, repo, _, quota := reservationFixture(t)
			ctx := context.Background()
			_, err := s.Create(ctx, 12, 60)
			require.NoError(t, err)
			if attempted {
				r, _ := repo.Get(ctx, 12)
				now := s.now()
				r.AttemptedAt, r.Status = &now, "resetting"
				require.NoError(t, s.save(ctx, r))
			}
			quota.usage.autoResetCandidates = quota.usage.autoResetCandidates[:1]
			quota.usage.RateLimitResetCredits.AvailableCount = 1
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			r, _ := repo.Get(ctx, 12)
			expected := "unavailable"
			if attempted {
				expected = "unknown"
			}
			require.Equal(t, expected, r.Status)
			require.Zero(t, quota.resetCalls.Load())
		})
	}
}

func TestResetReservationReceiptSurvivesOutcomeWriteFailure(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	_, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	repo.failConsumed = true
	_, err = s.Process(ctx, 12)
	require.Error(t, err)
	require.Equal(t, int32(1), quota.resetCalls.Load())
	// On restart the upstream card is gone. The durable receipt must be read
	// first, not misclassified as an unconfirmed external consumption.
	quota.usage.autoResetCandidates = quota.usage.autoResetCandidates[:1]
	quota.usage.RateLimitResetCredits.AvailableCount = 1
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "completed", r.Status)
	require.Equal(t, int32(1), quota.resetCalls.Load())
}

func TestResetReservationRecoveryNeverConsumesAgain(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	recovery, ok := s.recoverer.(*reservationTestRecovery)
	require.True(t, ok)
	recovery.fail = true
	_, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "consumed", r.Status)
	recovery.fail = false
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ = repo.Get(ctx, 12)
	require.Equal(t, "completed", r.Status)
	require.Equal(t, int32(1), quota.resetCalls.Load())
}

func TestResetReservationRetryUsesSameBodyAfterWindowChanges(t *testing.T) {
	s, _, _, quota := reservationFixture(t)
	ctx := context.Background()
	quota.failFirst = true
	_, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	quota.usage.RateLimit.PrimaryWindow.ResetAt += 5000
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Len(t, quota.resetArgs, 2)
	require.Equal(t, quota.resetArgs[0], quota.resetArgs[1])
}

func TestResetReservationDisabledIdentityExpiryAndEarlyThreshold(t *testing.T) {
	for _, scenario := range []string{"disabled", "identity", "expiry", "early threshold"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, accounts, quota := reservationFixture(t)
			ctx := context.Background()
			view, err := s.Create(ctx, 12, 30)
			require.NoError(t, err)
			expected := "pending"
			switch scenario {
			case "disabled":
				accounts.account.Status = StatusDisabled
				s.now = func() time.Time { return view.ExecuteAt }
			case "identity":
				accounts.account.Credentials["chatgpt_account_id"] = "other"
				expected = "invalidated"
			case "expiry":
				s.now = func() time.Time { return view.ExpiresAt.Add(-time.Minute) }
				expected = "expired"
			case "early threshold":
				accounts.account.Extra = map[string]any{
					OpenAIAutoResetCreditEnabledExtraKey: true,
					"codex_5h_used_percent":              100.0, "codex_usage_updated_at": s.now().Format(time.RFC3339),
					"codex_5h_reset_at": s.now().Add(time.Hour).Format(time.RFC3339),
				}
				quota.usage.RateLimit.PrimaryWindow.UsedPercent = 100
				expected = "completed"
			}
			_, err = s.Process(ctx, 12)
			require.NoError(t, err)
			r, _ := repo.Get(ctx, 12)
			require.Equal(t, expected, r.Status)
			if scenario != "early threshold" {
				require.Zero(t, quota.resetCalls.Load())
			}
		})
	}
}

func TestResetReservationConcurrentWorkerCancelAndManualGuard(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	_, _, err = s.guardConsumption(ctx, 12)
	require.Equal(t, "OPENAI_RESET_RESERVED", infraerrors.Reason(err))
	quota.resetEntered, quota.releaseReset = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := s.Process(ctx, 12); done <- err }()
	<-quota.resetEntered
	_, err = s.Cancel(ctx, 12, view.ID)
	require.Equal(t, "OPENAI_RESET_BUSY", infraerrors.Reason(err))
	_, err = s.Process(ctx, 12)
	require.Equal(t, "OPENAI_RESET_BUSY", infraerrors.Reason(err))
	close(quota.releaseReset)
	require.NoError(t, <-done)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "completed", r.Status)
	require.Equal(t, int32(1), quota.resetCalls.Load())
}

func TestResetReservationLockFailureAndInvalidDetailsFailClosed(t *testing.T) {
	s, _, _, quota := reservationFixture(t)
	locks, ok := s.locks.(*fakeLeaderLockCache)
	require.True(t, ok)
	locks.acquireErr = errors.New("redis unavailable")
	_, err := s.Create(context.Background(), 12, 30)
	require.Equal(t, "OPENAI_RESET_LOCK_UNAVAILABLE", infraerrors.Reason(err))
	require.Zero(t, quota.queryCalls.Load())
	locks.acquireErr = nil
	quota.usage.autoResetCandidates = quota.usage.autoResetCandidates[:1]
	_, err = s.Create(context.Background(), 12, 30)
	require.Equal(t, "OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE", infraerrors.Reason(err))
	require.Zero(t, quota.resetCalls.Load())
}

func TestResetReservationUnknownFenceIsBoundedAndBlocksAliases(t *testing.T) {
	s, repo, accounts, quota := reservationFixture(t)
	ctx := context.Background()
	view, err := s.Create(ctx, 12, 60)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	now := s.now()
	r.Status, r.AttemptedAt = "unknown", &now
	require.NoError(t, s.save(ctx, r))
	// An alias has the same upstream identity and a different local account ID.
	accounts.account.ID = 13
	_, _, err = s.guardConsumption(ctx, 13)
	require.Equal(t, "OPENAI_RESET_RESERVED", infraerrors.Reason(err))
	_, err = s.Create(ctx, 13, 60)
	require.Equal(t, "OPENAI_RESET_RESERVED", infraerrors.Reason(err))
	_, err = s.Create(ctx, 12, 60)
	require.Equal(t, "OPENAI_RESET_RESERVED", infraerrors.Reason(err))
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	require.Zero(t, quota.resetCalls.Load())
	s.now = func() time.Time { return view.ExpiresAt.Add(2*time.Minute + time.Second) }
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ = repo.Get(ctx, 12)
	require.Equal(t, "unconfirmed", r.Status)
	require.Zero(t, quota.resetCalls.Load())
	_, release, err := s.guardConsumption(ctx, 13)
	require.NoError(t, err)
	release()
}

func TestResetReservationInsufficientBudgetDoesNotStartRedemption(t *testing.T) {
	s, repo, _, quota := reservationFixture(t)
	_, err := s.Create(context.Background(), 12, 60)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.Process(ctx, 12)
	require.NoError(t, err)
	r, _ := repo.Get(ctx, 12)
	require.Equal(t, "pending", r.Status)
	require.Nil(t, r.AttemptedAt)
	require.Zero(t, quota.resetCalls.Load())
}

func TestResetReservationUpstreamOutcomes(t *testing.T) {
	for _, outcome := range []string{"ok", "success", " SUCCESS ", "NO_CREDIT", "unexpected"} {
		t.Run(outcome, func(t *testing.T) {
			s, repo, _, quota := reservationFixture(t)
			s.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: outcome}
			_, err := s.Create(context.Background(), 12, 60)
			require.NoError(t, err)
			_, err = s.Process(context.Background(), 12)
			require.NoError(t, err)
			r, _ := repo.Get(context.Background(), 12)
			expected := "completed"
			switch outcome {
			case "NO_CREDIT":
				expected = "unavailable"
			case "unexpected":
				expected = "resetting"
			}
			require.Equal(t, expected, r.Status)
			require.Equal(t, int32(1), quota.resetCalls.Load())
		})
	}
}

func TestResetReservationRecoveryFenceBoundedAndCacheWarningsNonblocking(t *testing.T) {
	t.Run("recovery unavailable", func(t *testing.T) {
		s, repo, _, quota := reservationFixture(t)
		s.recoverer = &reservationTestRecovery{fail: true}
		_, err := s.Create(context.Background(), 12, 60)
		require.NoError(t, err)
		_, err = s.Process(context.Background(), 12)
		require.NoError(t, err)
		r, _ := repo.Get(context.Background(), 12)
		require.Equal(t, "consumed", r.Status)
		s.now = func() time.Time { return r.ConsumedAt.Add(11 * time.Minute) }
		_, err = s.Process(context.Background(), 12)
		require.NoError(t, err)
		r, _ = repo.Get(context.Background(), 12)
		require.Equal(t, "completed", r.Status)
		require.Equal(t, OpenAIQuotaResetWarningAccountRecoveryFailed, r.ErrorCode)
		require.Equal(t, int32(1), quota.resetCalls.Load())
	})
	t.Run("cache warning", func(t *testing.T) {
		s, repo, _, quota := reservationFixture(t)
		s.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: "ok", cacheErr: errors.New("cache unavailable")}
		_, err := s.Create(context.Background(), 12, 60)
		require.NoError(t, err)
		_, err = s.Process(context.Background(), 12)
		require.NoError(t, err)
		r, _ := repo.Get(context.Background(), 12)
		require.Equal(t, "completed", r.Status)
		require.Equal(t, OpenAIQuotaResetWarningCacheRefreshFailed, r.ErrorCode)
	})
}

func TestResetReservationWaitingPreservesLegacyAvailableCreditState(t *testing.T) {
	s, _, accounts, quota := reservationFixture(t)
	accounts.account.Schedulable = true
	accounts.account.Extra = map[string]any{OpenAIAutoResetCreditEnabledExtraKey: true}
	quota.usage.RateLimit.PrimaryWindow.UsedPercent = 90
	_, err := s.Create(context.Background(), 12, 30)
	require.NoError(t, err)
	auto := NewOpenAIQuotaAutoResetService(accounts, quota, s.recoverer, s.idempotency, nil, nil, nil)
	auto.reservations = s
	require.NoError(t, auto.evaluateAccount(context.Background(), 12))
	state := accounts.stateForTest()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusAvailable, state.Status)
	require.Equal(t, int32(2), quota.queryCalls.Load(), "creation plus one legacy refresh")
	require.NoError(t, auto.evaluateAccount(context.Background(), 12))
	require.Equal(t, int32(2), quota.queryCalls.Load(), "fresh waiting state must not amplify upstream queries")
	require.Zero(t, quota.resetCalls.Load())
}
