package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIAutoResetCreditExtra(t *testing.T) {
	t.Run("历史账号默认关闭", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		config := ResolveOpenAIAutoResetCreditConfig(account)
		require.False(t, config.Enabled)
		require.Equal(t, 1.0, config.Threshold5h)
		require.Equal(t, 1.0, config.Threshold7d)
	})

	t.Run("开启时补齐两个百分百阈值并剥离运行态", func(t *testing.T) {
		extra, err := normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, false, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey: true,
			OpenAIAutoResetCreditStateExtraKey:   map[string]any{"status": "success"},
		})
		require.NoError(t, err)
		require.Equal(t, 1.0, extra[OpenAIAutoResetCredit5hThresholdExtraKey])
		require.Equal(t, 1.0, extra[OpenAIAutoResetCredit7dThresholdExtraKey])
		require.NotContains(t, extra, OpenAIAutoResetCreditStateExtraKey)
	})

	t.Run("阈值和账号类型严格校验", func(t *testing.T) {
		_, err := normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, false, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 0.0009,
		})
		require.Error(t, err)

		_, err = normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, true, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey: true,
		})
		require.Error(t, err)
	})
}

func TestShouldAutoPauseOpenAIAccountByQuota_AutoResetCreditStates(t *testing.T) {
	now := time.Now().UTC()
	baseExtra := map[string]any{
		OpenAIAutoResetCreditEnabledExtraKey:     true,
		OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
		OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
		"auto_pause_7d_threshold":                0.8,
		"auto_pause_5h_disabled":                 true,
		"codex_7d_used_percent":                  90.0,
		"codex_usage_updated_at":                 now.Format(time.RFC3339),
		"codex_7d_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
	}

	t.Run("卡状态未知时暂停并触发异步查询", func(t *testing.T) {
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: cloneOpenAIAutoResetExtra(baseExtra)}
		paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.True(t, paused)
		require.Equal(t, "quota_auto_reset_credit_check_7d", decision.reason)
	})

	t.Run("明确有卡时允许继续到用卡阈值", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusAvailable, AvailableCount: 1, CheckedAt: now.Format(time.RFC3339),
		}
		account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.False(t, paused)
	})

	t.Run("达到用卡阈值后即使有卡也退出调度", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra["codex_7d_used_percent"] = 100.0
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusAvailable, AvailableCount: 1, CheckedAt: now.Format(time.RFC3339),
		}
		account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.True(t, paused)
		require.Equal(t, "quota_auto_reset_pending_7d", decision.reason)
	})

	t.Run("自然窗口重置后清除动态阻塞", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra["codex_7d_used_percent"] = 100.0
		extra["codex_7d_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusFailed, TriggerWindow: "5h", ErrorCode: "RESET_FAILED",
		}
		account := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.False(t, paused)
	})
}

func TestNotifyOpenAIAutoResetFromScheduler_CoolsDownPerAccount(t *testing.T) {
	svc := &OpenAIQuotaAutoResetService{ctx: context.Background(), queue: make(chan int64, 8)}
	setOpenAIAutoResetNotifier(svc)
	t.Cleanup(func() { clearOpenAIAutoResetNotifier(svc) })

	const accountA, accountB int64 = 9_900_001, 9_900_002
	t.Cleanup(func() {
		openAIAutoResetSchedulerNotifiedAt.Delete(accountA)
		openAIAutoResetSchedulerNotifiedAt.Delete(accountB)
	})
	drain := func() []int64 {
		var got []int64
		for {
			select {
			case id := <-svc.queue:
				svc.pending.Delete(id)
				got = append(got, id)
			default:
				return got
			}
		}
	}

	base := time.Now()
	require.True(t, notifyOpenAIAutoResetFromSchedulerAt(accountA, base))
	require.Equal(t, []int64{accountA}, drain())

	require.False(t, notifyOpenAIAutoResetFromSchedulerAt(accountA, base.Add(10*time.Second)), "冷却期内同一账号不重复通知")
	require.Empty(t, drain())

	require.True(t, notifyOpenAIAutoResetFromSchedulerAt(accountB, base.Add(10*time.Second)), "冷却按账号独立")
	require.Equal(t, []int64{accountB}, drain())

	require.True(t, notifyOpenAIAutoResetFromSchedulerAt(accountA, base.Add(openAIAutoResetSchedulerNotifyCooldown)))
	require.Equal(t, []int64{accountA}, drain())
}

func TestSelectOpenAIAutoResetCandidate_FailsClosed(t *testing.T) {
	candidates := []openAIAutoResetCreditCandidate{
		{ID: "later", ExpiresAt: "2026-09-02T00:00:00Z"},
		{ID: "earlier", ExpiresAt: "2026-09-01T00:00:00Z"},
	}
	selected, err := selectOpenAIAutoResetCandidate(candidates, 2, nil, "cycle-a")
	require.NoError(t, err)
	require.Equal(t, "earlier", selected.ID)

	_, err = selectOpenAIAutoResetCandidate([]openAIAutoResetCreditCandidate{
		{ExpiresAt: "2026-09-01T00:00:00Z"},
	}, 1, nil, "cycle-a")
	require.Error(t, err)

	_, err = selectOpenAIAutoResetCandidate(candidates, 2, &OpenAIAutoResetCreditState{
		AttemptCycleHash: "cycle-a", AttemptCreditHash: shortOpenAIAutoResetHash("missing"),
	}, "cycle-a")
	require.Error(t, err, "模糊结果后原卡消失时不得切换下一张卡")
}

func TestOpenAIQuotaAutoResetService_AssessesIndependentWindows(t *testing.T) {
	service := &OpenAIQuotaAutoResetService{}
	account := &Account{Extra: map[string]any{
		"auto_pause_5h_disabled": true,
		"auto_pause_7d_disabled": true,
	}}
	config := OpenAIAutoResetCreditConfig{Enabled: true, Threshold5h: 0.8, Threshold7d: 0.9}
	tests := []struct {
		name       string
		fiveHour   float64
		sevenDay   float64
		wantWindow string
	}{
		{name: "5h", fiveHour: 0.8, sevenDay: 0.2, wantWindow: ""},
		{name: "7d", fiveHour: 0.2, sevenDay: 0.9, wantWindow: "7d"},
		{name: "同时触发", fiveHour: 0.95, sevenDay: 0.95, wantWindow: "7d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := service.buildAssessment(account, config, test.fiveHour, test.sevenDay)
			require.Equal(t, test.wantWindow != "", assessment.resetReached)
			require.Equal(t, test.wantWindow, assessment.triggerWindow)
		})
	}
}

type autoResetTestAccountRepo struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
}

func (r *autoResetTestAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.account
	copy.Extra = cloneOpenAIAutoResetExtra(r.account.Extra)
	return &copy, nil
}

func (r *autoResetTestAccountRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if r.account != nil && r.account.ID == id {
			acc, err := r.GetByID(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, acc)
		}
	}
	return out, nil
}

func (r *autoResetTestAccountRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	return nil
}

type autoResetTestQuota struct {
	usage        *OpenAIQuotaUsage
	resetCalls   atomic.Int32
	resetEntered chan struct{}
	releaseReset chan struct{}
	enterOnce    sync.Once
	mu           sync.Mutex
	resetArgs    [][2]string
	failFirst    bool
	queryCalls   atomic.Int32
	queryErr     error
}

func (q *autoResetTestQuota) QueryUsage(context.Context, int64) (*OpenAIQuotaUsage, error) {
	q.queryCalls.Add(1)
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	copy := *q.usage
	return &copy, nil
}

func (q *autoResetTestQuota) CacheResetCreditsSnapshot(context.Context, int64, *OpenAIRateLimitResetCredits) error {
	return nil
}

func (q *autoResetTestQuota) CachePostResetSnapshot(context.Context, int64, *OpenAIQuotaUsage) error {
	return nil
}

func (q *autoResetTestQuota) ResetCreditTargeted(_ context.Context, _ int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error) {
	if creditID == "" || redeemRequestID == "" {
		panic("targeted reset identifiers must be present")
	}
	call := q.resetCalls.Add(1)
	q.mu.Lock()
	q.resetArgs = append(q.resetArgs, [2]string{creditID, redeemRequestID})
	q.mu.Unlock()
	if q.failFirst && call == 1 {
		return nil, context.DeadlineExceeded
	}
	if q.resetEntered != nil {
		q.enterOnce.Do(func() { close(q.resetEntered) })
	}
	if q.releaseReset != nil {
		<-q.releaseReset
	}
	return &OpenAIQuotaResetResult{Code: "ok", WindowsReset: 2}, nil
}

type autoResetTestRecoverer struct{}

func (autoResetTestRecoverer) RecoverAccountState(context.Context, int64, AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	return &SuccessfulTestRecoveryResult{ClearedRateLimit: true}, nil
}

func TestOpenAIQuotaAutoResetService_ConcurrentInstancesConsumeOnce(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
			OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
			"codex_7d_used_percent":                  100.0,
			"codex_usage_updated_at":                 now.Format(time.RFC3339),
			"codex_7d_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
		},
	}
	repo := &autoResetTestAccountRepo{account: account}
	usage := &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 3600, ResetAt: now.Add(time.Hour).Unix()},
			SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400, ResetAt: now.Add(24 * time.Hour).Unix()},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{
			AvailableCount: 1,
			Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)}},
		},
		autoResetCandidates: []openAIAutoResetCreditCandidate{{ID: "credit-sensitive-id", ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)}},
	}
	quota := &autoResetTestQuota{usage: usage, resetEntered: make(chan struct{}), releaseReset: make(chan struct{})}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	config := DefaultIdempotencyConfig()
	config.ObserveOnly = false
	config.ProcessingTimeout = time.Second
	serviceA := NewOpenAIQuotaAutoResetService(repo, quota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, config), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(repo, quota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, config), nil, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = serviceA.evaluateAccount(context.Background(), account.ID)
	}()
	<-quota.resetEntered
	go func() {
		defer wg.Done()
		_ = serviceB.evaluateAccount(context.Background(), account.ID)
	}()
	time.Sleep(50 * time.Millisecond)
	close(quota.releaseReset)
	wg.Wait()

	require.Equal(t, int32(1), quota.resetCalls.Load())
	repo.mu.Lock()
	state := openAIAutoResetStateFromExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	encodedState, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(encodedState), "credit-sensitive-id")
}

func TestOpenAIQuotaAutoResetService_TimeoutRetryReusesRequestBody(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
			OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
			"codex_7d_used_percent":                  100.0,
			"codex_usage_updated_at":                 now.Format(time.RFC3339),
			"codex_7d_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
		},
	}
	repo := &autoResetTestAccountRepo{account: account}
	expiresAt := now.Add(48 * time.Hour).Format(time.RFC3339)
	quota := &autoResetTestQuota{
		failFirst: true,
		usage: &OpenAIQuotaUsage{
			FetchedAt: now.Unix(),
			RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 3600, ResetAt: now.Add(time.Hour).Unix()},
			},
			RateLimitResetCredits: &OpenAIRateLimitResetCredits{
				AvailableCount: 1,
				Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: expiresAt}},
			},
			autoResetCandidates: []openAIAutoResetCreditCandidate{{ID: "retry-credit", ExpiresAt: expiresAt}},
		},
	}
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.FailedRetryBackoff = 0
	service := NewOpenAIQuotaAutoResetService(
		repo,
		quota,
		autoResetTestRecoverer{},
		NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), idempotencyConfig),
		nil, nil, nil,
	)

	require.Error(t, service.evaluateAccount(context.Background(), account.ID))
	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, int32(1), quota.resetCalls.Load(), "cooldown must suppress immediate retry")
	state := repo.stateForTest()
	state.RetryAfter = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	repo.setExtraForTest(OpenAIAutoResetCreditStateExtraKey, state)
	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	quota.mu.Lock()
	args := append([][2]string(nil), quota.resetArgs...)
	quota.mu.Unlock()
	require.Len(t, args, 2)
	require.Equal(t, args[0], args[1], "超时重试必须复用相同 credit_id 与 redeem_request_id")
}

// newResetThresholdTestFixture 构造一个 7d 用量已过用卡阈值、处于停调中的账号。
func newResetThresholdTestFixture(t *testing.T, state *OpenAIAutoResetCreditState, availableCredits int) (*OpenAIQuotaAutoResetService, *autoResetTestAccountRepo, *autoResetTestQuota) {
	t.Helper()
	now := time.Now().UTC()
	extra := map[string]any{
		OpenAIAutoResetCreditEnabledExtraKey:     true,
		OpenAIAutoResetCredit5hThresholdExtraKey: 0.95,
		OpenAIAutoResetCredit7dThresholdExtraKey: 0.95,
		"codex_7d_used_percent":                  99.0,
		"codex_usage_updated_at":                 now.Format(time.RFC3339),
		"codex_7d_reset_at":                      now.Add(48 * time.Hour).Format(time.RFC3339),
	}
	if state != nil {
		extra[OpenAIAutoResetCreditStateExtraKey] = state
	}
	account := &Account{ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: extra}
	repo := &autoResetTestAccountRepo{account: account}
	quota := &autoResetTestQuota{usage: &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 99, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 48 * 3600, ResetAt: now.Add(48 * time.Hour).Unix()},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: availableCredits},
	}}
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	service := NewOpenAIQuotaAutoResetService(repo, quota, autoResetTestRecoverer{},
		NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), idempotencyConfig), nil, nil, nil)
	return service, repo, quota
}

func (r *autoResetTestAccountRepo) setExtraForTest(key string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Extra[key] = value
}

func (r *autoResetTestAccountRepo) stateForTest() *OpenAIAutoResetCreditState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return openAIAutoResetStateFromExtra(r.account.Extra)
}

func TestOpenAIQuotaAutoResetService_ResetThresholdSkipsRequeryWhileNoCreditIsFresh(t *testing.T) {
	now := time.Now().UTC()
	service, repo, quota := newResetThresholdTestFixture(t, &OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusNoCredit, TriggerWindow: "7d", CheckedAt: now.Format(time.RFC3339), ErrorCode: "NO_RESET_CREDIT",
	}, 0)

	for i := 0; i < 3; i++ {
		require.NoError(t, service.evaluateAccount(context.Background(), 501))
	}
	require.Zero(t, quota.queryCalls.Load(), "10 分钟内已确认无卡，调度通知不应再触发上游查询")

	stale := now.Add(-openAIAutoResetSnapshotTTL - time.Minute).Format(time.RFC3339)
	repo.setExtraForTest(OpenAIAutoResetCreditStateExtraKey, &OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusNoCredit, TriggerWindow: "7d", CheckedAt: stale, ErrorCode: "NO_RESET_CREDIT",
	})
	repo.setExtraForTest("codex_usage_updated_at", stale)
	require.NoError(t, service.evaluateAccount(context.Background(), 501))
	require.Equal(t, int32(1), quota.queryCalls.Load(), "无卡结论过期后应重新查询，以便发现新发放的卡")
	require.Equal(t, OpenAIAutoResetStatusNoCredit, repo.stateForTest().Status)
}

func TestOpenAIQuotaAutoResetService_ResetThresholdStillQueriesWhenCreditAvailable(t *testing.T) {
	now := time.Now().UTC()
	service, _, quota := newResetThresholdTestFixture(t, &OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusAvailable, AvailableCount: 1, TriggerWindow: "7d", CheckedAt: now.Format(time.RFC3339),
	}, 0)

	require.NoError(t, service.evaluateAccount(context.Background(), 501))
	require.Equal(t, int32(1), quota.queryCalls.Load(), "有卡时到达用卡阈值必须立即查询并进入用卡流程")
}

func TestOpenAIQuotaAutoResetService_QueryFailureBacksOffBeforeRetry(t *testing.T) {
	service, repo, quota := newResetThresholdTestFixture(t, nil, 0)
	quota.queryErr = errors.New("upstream EOF")

	require.Error(t, service.evaluateAccount(context.Background(), 501))
	require.Equal(t, int32(1), quota.queryCalls.Load())
	state := repo.stateForTest()
	require.Equal(t, OpenAIAutoResetStatusFailed, state.Status)
	require.Equal(t, "RESET_CREDIT_QUERY_FAILED", state.ErrorCode)

	for i := 0; i < 3; i++ {
		require.NoError(t, service.evaluateAccount(context.Background(), 501))
	}
	require.Equal(t, int32(1), quota.queryCalls.Load(), "查询失败后一分钟内不应重复打上游")

	state.LastResultAt = time.Now().UTC().Add(-openAIAutoResetQueryFailureRetryAfter - time.Second).Format(time.RFC3339)
	repo.setExtraForTest(OpenAIAutoResetCreditStateExtraKey, state)
	quota.queryErr = nil
	require.NoError(t, service.evaluateAccount(context.Background(), 501))
	require.Equal(t, int32(2), quota.queryCalls.Load(), "重试间隔过后应恢复查询")
	require.Equal(t, OpenAIAutoResetStatusNoCredit, repo.stateForTest().Status)
}

type autoResetTrackingRecoverer struct {
	calls               atomic.Int32
	quotaRateLimitClear atomic.Int32
	quotaClearErr       error
}

func (r *autoResetTrackingRecoverer) RecoverAccountState(context.Context, int64, AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	r.calls.Add(1)
	return &SuccessfulTestRecoveryResult{ClearedRateLimit: true}, nil
}

func (r *autoResetTrackingRecoverer) ClearObservedOpenAIQuotaRateLimit(context.Context, *Account) error {
	r.quotaRateLimitClear.Add(1)
	return r.quotaClearErr
}

func TestOpenAIQuotaAutoResetService_RejectsUnknownOutcomeAndRecoversTimeout429(t *testing.T) {
	now := time.Now().UTC()
	rateLimitedAt := now.Add(-5 * time.Minute)
	svc, repo, quota := newResetThresholdTestFixture(t, nil, 1)
	quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit-1", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
	svc.quota = &reservationOutcomeQuota{autoResetTestQuota: quota, outcome: "unexpected_code"}
	recoverer := &autoResetTrackingRecoverer{quotaClearErr: errors.New("db unavailable")}
	svc.recoverer = recoverer

	err := svc.evaluateAccount(context.Background(), 501)
	require.Error(t, err)
	require.Zero(t, recoverer.calls.Load())
	require.Zero(t, recoverer.quotaRateLimitClear.Load())
	require.Equal(t, OpenAIAutoResetStatusFailed, repo.stateForTest().Status)

	repo.mu.Lock()
	repo.account.RateLimitedAt = &rateLimitedAt
	repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
		Status:    OpenAIAutoResetStatusResetting,
		CheckedAt: now.Add(-time.Hour).Format(time.RFC3339),
	}
	repo.mu.Unlock()
	quota.usage.RateLimit.Allowed = true
	quota.usage.RateLimit.LimitReached = false
	quota.usage.RateLimit.SecondaryWindow.UsedPercent = 0
	quota.usage.RateLimitResetCredits.AvailableCount = 0
	quota.usage.autoResetCandidates = nil
	svc.quota = quota

	// Missing or empty zero-horizon windows must NOT confirm schedulability.
	savedPrimary := quota.usage.RateLimit.PrimaryWindow
	savedSecondary := quota.usage.RateLimit.SecondaryWindow
	quota.usage.RateLimit.PrimaryWindow = nil
	quota.usage.RateLimit.SecondaryWindow = nil
	require.False(t, openAIUsageConfirmsSchedulable(quota.usage, false, now))
	quota.usage.RateLimit.SecondaryWindow = &OpenAIRateLimitWindow{}
	require.False(t, openAIUsageConfirmsSchedulable(quota.usage, false, now))
	quota.usage.RateLimit.PrimaryWindow = savedPrimary
	quota.usage.RateLimit.SecondaryWindow = savedSecondary

	// Transient recovery failure keeps AttemptUnconfirmed and TriggerWindow intact during 1m backoff.
	repo.mu.Lock()
	repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
		Status:            OpenAIAutoResetStatusResetting,
		TriggerWindow:     "7d",
		AttemptCreditHash: "hash-credit-1",
		CheckedAt:         now.Add(-time.Hour).Format(time.RFC3339),
	}
	repo.mu.Unlock()
	err = svc.evaluateAccount(context.Background(), 501)
	require.ErrorContains(t, err, "db unavailable")
	require.Equal(t, int32(1), recoverer.quotaRateLimitClear.Load())
	failedState := repo.stateForTest()
	require.Equal(t, OpenAIAutoResetStatusFailed, failedState.Status)
	require.Equal(t, OpenAIQuotaResetWarningAccountRecoveryFailed, failedState.ErrorCode)
	require.True(t, failedState.AttemptUnconfirmed)
	require.Equal(t, "hash-credit-1", failedState.AttemptCreditHash)

	// Hot notification during active backoff when !pauseReached must NOT overwrite StatusFailed.
	repo.mu.Lock()
	repo.account.Extra["codex_7d_used_percent"] = 0.0
	repo.account.Extra["codex_usage_updated_at"] = now.Add(-time.Hour).Format(time.RFC3339)
	repo.mu.Unlock()
	require.NoError(t, svc.evaluateAccount(context.Background(), 501))
	require.Equal(t, OpenAIAutoResetStatusFailed, repo.stateForTest().Status)
	require.True(t, repo.stateForTest().AttemptUnconfirmed)

	// Next cycle after backoff succeeds and clears 429 once.
	recoverer.quotaClearErr = nil
	repo.mu.Lock()
	failedState.LastResultAt = now.Add(-2 * time.Minute).Format(time.RFC3339)
	repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = failedState
	repo.mu.Unlock()
	require.NoError(t, svc.evaluateAccount(context.Background(), 501))
	require.Zero(t, recoverer.calls.Load(), "must not run broad RecoverAccountState")
	require.Equal(t, int32(2), recoverer.quotaRateLimitClear.Load())
	require.Equal(t, OpenAIAutoResetStatusNoCredit, repo.stateForTest().Status)
	require.False(t, repo.stateForTest().AttemptUnconfirmed)

	// Settled StatusSuccess -> transient QueryUsage failure -> QueryUsage success must NOT re-arm recovery.
	for _, settled := range []string{OpenAIAutoResetStatusSuccess, OpenAIAutoResetStatusNoCredit} {
		repo.mu.Lock()
		repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
			Status:            settled,
			AttemptCreditHash: "hash-credit-1",
			CheckedAt:         now.Add(-time.Hour).Format(time.RFC3339),
		}
		repo.account.Extra["codex_usage_updated_at"] = now.Add(-time.Hour).Format(time.RFC3339)
		repo.mu.Unlock()
		quota.queryErr = errors.New("temporary query failure")
		require.Error(t, svc.evaluateAccount(context.Background(), 501))
		require.Equal(t, OpenAIAutoResetStatusFailed, repo.stateForTest().Status)
		require.False(t, repo.stateForTest().AttemptUnconfirmed)

		quota.queryErr = nil
		s := repo.stateForTest()
		s.LastResultAt = now.Add(-2 * time.Minute).Format(time.RFC3339)
		repo.mu.Lock()
		repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = s
		repo.mu.Unlock()
		require.NoError(t, svc.evaluateAccount(context.Background(), 501))
		require.Zero(t, recoverer.calls.Load())
		require.Equal(t, int32(2), recoverer.quotaRateLimitClear.Load())
	}
}

type openAIObservedClearRepoStub struct {
	AccountRepository
	calls   int
	cleared bool
	err     error
}

type openAIObservedBlockRecorder struct {
	cleared []int64
}

func (b *openAIObservedBlockRecorder) BlockAccountScheduling(*Account, time.Time, string) {}
func (b *openAIObservedBlockRecorder) ClearAccountSchedulingBlock(id int64) {
	b.cleared = append(b.cleared, id)
}

func (r *openAIObservedClearRepoStub) ClearRateLimit(context.Context, int64) error {
	panic("ClearRateLimit must not be called by ClearObservedOpenAIQuotaRateLimit")
}

func (r *openAIObservedClearRepoStub) ClearTempUnschedulable(context.Context, int64) error {
	panic("ClearTempUnschedulable must not be called by ClearObservedOpenAIQuotaRateLimit")
}

func (r *openAIObservedClearRepoStub) ClearModelRateLimits(context.Context, int64) error {
	panic("ClearModelRateLimits must not be called by ClearObservedOpenAIQuotaRateLimit")
}

func (r *openAIObservedClearRepoStub) ClearError(context.Context, int64) error {
	panic("ClearError must not be called by ClearObservedOpenAIQuotaRateLimit")
}

func (r *openAIObservedClearRepoStub) ClearOpenAIRateLimitIfObserved(_ context.Context, _ int64, _, _ *time.Time) (bool, error) {
	r.calls++
	return r.cleared, r.err
}

func TestClearObservedOpenAIQuotaRateLimit_PreservesNonQuotaRuntimeState(t *testing.T) {
	now := time.Now().UTC()
	limitedAt := now.Add(-5 * time.Minute)
	resetAt := now.Add(time.Hour)
	overloadUntil := now.Add(30 * time.Minute)
	tempUnschedUntil := now.Add(20 * time.Minute)

	repo := &openAIObservedClearRepoStub{cleared: true}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	blocker := &openAIObservedBlockRecorder{}
	svc.runtimeBlocker = blocker

	account := &Account{
		ID:                     88,
		Platform:               PlatformOpenAI,
		Status:                 StatusActive,
		RateLimitedAt:          &limitedAt,
		RateLimitResetAt:       &resetAt,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		Extra:                  map[string]any{modelRateLimitsKey: map[string]any{"image": map[string]any{"rate_limit_reset_at": resetAt.Format(time.RFC3339)}}},
	}

	require.NoError(t, svc.ClearObservedOpenAIQuotaRateLimit(context.Background(), account))
	require.Equal(t, 1, repo.calls)
	require.Equal(t, []int64{88}, blocker.cleared)
	require.NotNil(t, account.OverloadUntil)
	require.NotNil(t, account.TempUnschedulableUntil)

	// Rearmed 429 generation (repository returns false) must not notify scheduler.
	repo.cleared = false
	blocker.cleared = nil
	require.NoError(t, svc.ClearObservedOpenAIQuotaRateLimit(context.Background(), account))
	require.Equal(t, 2, repo.calls)
	require.Empty(t, blocker.cleared)

	// StatusError accounts must never be touched.
	account.Status = StatusError
	require.NoError(t, svc.ClearObservedOpenAIQuotaRateLimit(context.Background(), account))
	require.Equal(t, 2, repo.calls)
}

func TestOpenAIQuotaAutoResetAndSparkUpdatesFallbackToResetAt(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(3 * time.Hour)
	usage := &OpenAIQuotaUsage{
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent: 80, LimitWindowSeconds: 604800, ResetAfterSeconds: 0, ResetAt: future.Unix(),
			},
		},
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
			MeteredFeature: "codex_bengalfox",
			RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent: 90, LimitWindowSeconds: 18000, ResetAfterSeconds: 0, ResetAt: future.Unix(),
				},
			},
		}},
	}
	overall := buildOpenAIAutoResetUsageUpdates(usage, now)
	require.Equal(t, int(3*time.Hour/time.Second), overall["codex_7d_reset_after_seconds"])
	spark := buildCodexSparkWindowExtraUpdates(usage, now)
	require.Equal(t, int(3*time.Hour/time.Second), spark["codex_5h_reset_after_seconds"])
}

type autoResetFailThirdGetRepo struct {
	*autoResetTestAccountRepo
	getCalls atomic.Int32
	failCall int32
}

func (r *autoResetFailThirdGetRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.getCalls.Add(1) == r.failCall {
		return nil, errors.New("pre-consume get failed")
	}
	return r.autoResetTestAccountRepo.GetByID(ctx, id)
}

type autoResetSwitchableRecoverer struct {
	calls               atomic.Int32
	quotaRateLimitClear atomic.Int32
	recoverErr          error
	onRecover           func()
}

func (r *autoResetSwitchableRecoverer) RecoverAccountState(context.Context, int64, AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	r.calls.Add(1)
	if r.onRecover != nil {
		r.onRecover()
	}
	if r.recoverErr != nil {
		return nil, r.recoverErr
	}
	return &SuccessfulTestRecoveryResult{ClearedRateLimit: true}, nil
}

func (r *autoResetSwitchableRecoverer) ClearObservedOpenAIQuotaRateLimit(context.Context, *Account) error {
	r.quotaRateLimitClear.Add(1)
	return nil
}

func TestOpenAIQuotaAutoReset_ReplayOutcomeAndPreConsumeGuards(t *testing.T) {
	t.Run("pre-consume GetByID failure never persists resetting or arms 429 clear", func(t *testing.T) {
		now := time.Now().UTC()
		rateLimitedAt := now.Add(-5 * time.Minute)
		svc, baseRepo, quota := newResetThresholdTestFixture(t, nil, 1)
		quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit-1", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
		baseRepo.mu.Lock()
		baseRepo.account.RateLimitedAt = &rateLimitedAt
		baseRepo.mu.Unlock()

		recoverer := &autoResetSwitchableRecoverer{}
		wrappedRepo := &autoResetFailThirdGetRepo{autoResetTestAccountRepo: baseRepo, failCall: 3}
		svc.accountRepo = wrappedRepo
		svc.recoverer = recoverer

		err := svc.evaluateAccount(context.Background(), 501)
		require.ErrorContains(t, err, "pre-consume get failed")
		require.Zero(t, quota.resetCalls.Load(), "ResetCreditTargeted must not be called")
		state := baseRepo.stateForTest()
		require.NotNil(t, state)
		require.Equal(t, OpenAIAutoResetStatusChecking, state.Status)
		require.False(t, state.AttemptUnconfirmed)

		// Next evaluation with below-threshold schedulable usage must NOT clear 429.
		svc.accountRepo = baseRepo
		quota.usage.RateLimit.Allowed = true
		quota.usage.RateLimit.LimitReached = false
		quota.usage.RateLimit.SecondaryWindow.UsedPercent = 0
		require.NoError(t, svc.evaluateAccount(context.Background(), 501))
		require.Zero(t, recoverer.calls.Load())
		require.Zero(t, recoverer.quotaRateLimitClear.Load())
	})

	t.Run("idempotency replay preserves unredacted outcome and persists after parent ctx cancel", func(t *testing.T) {
		now := time.Now().UTC()
		svc, repo, quota := newResetThresholdTestFixture(t, nil, 1)
		quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit-1", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
		idempCfg := DefaultIdempotencyConfig()
		idempCfg.ObserveOnly = false
		idempCfg.FailedRetryBackoff = 0
		svc.idempotency = NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), idempCfg)

		recoverer := &autoResetSwitchableRecoverer{recoverErr: errors.New("recover failed once")}
		svc.recoverer = recoverer

		// First attempt consumes upstream ("ok") and fails during post-processing.
		err := svc.evaluateAccount(context.Background(), 501)
		require.ErrorContains(t, err, OpenAIQuotaResetWarningAccountRecoveryFailed)
		require.Equal(t, int32(1), quota.resetCalls.Load())
		require.Equal(t, OpenAIAutoResetStatusFailed, repo.stateForTest().Status)
		require.True(t, repo.stateForTest().AttemptUnconfirmed)

		// Clear RetryAfter, allow recovery to succeed, and cancel outer ctx during post-processing.
		recoverer.recoverErr = nil
		evalCtx, cancelEval := context.WithCancel(context.Background())
		recoverer.onRecover = cancelEval
		state := repo.stateForTest()
		state.RetryAfter = now.Add(-time.Second).Format(time.RFC3339Nano)
		state.LastResultAt = now.Add(-2 * time.Minute).Format(time.RFC3339)
		repo.setExtraForTest(OpenAIAutoResetCreditStateExtraKey, state)

		require.NoError(t, svc.evaluateAccount(evalCtx, 501))
		require.Equal(t, int32(1), quota.resetCalls.Load(), "replay must reuse stored outcome without second consume")
		finalState := repo.stateForTest()
		require.Equal(t, OpenAIAutoResetStatusSuccess, finalState.Status)
		require.False(t, finalState.AttemptUnconfirmed)
	})

	t.Run("real OpenAIQuotaService pre-POST client error keeps AttemptUnconfirmed false", func(t *testing.T) {
		now := time.Now().UTC()
		rateLimitedAt := now.Add(-5 * time.Minute)
		svc, repo, quota := newResetThresholdTestFixture(t, nil, 1)
		quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit-1", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
		repo.mu.Lock()
		repo.account.RateLimitedAt = &rateLimitedAt
		repo.account.Credentials = map[string]any{
			"chatgpt_account_id": "acct-501",
			"access_token":       "tok-valid",
			"expires_at":         now.Add(time.Hour).Format(time.RFC3339),
		}
		repo.mu.Unlock()

		realQuota := NewOpenAIQuotaService(
			repo,
			nil,
			NewOpenAITokenProvider(repo, nil, nil),
			func(string) (*req.Client, error) {
				return nil, errors.New("proxy client init failed")
			},
			nil,
		)
		recoverer := &autoResetSwitchableRecoverer{}
		svc.quota = &autoResetRealClientErrorQuota{
			autoResetTestQuota: quota,
			real:               realQuota,
		}
		svc.recoverer = recoverer

		err := svc.evaluateAccount(context.Background(), 501)
		require.Error(t, err)
		require.Equal(t, "OPENAI_QUOTA_CLIENT_ERROR", infraerrors.Reason(err))
		state := repo.stateForTest()
		require.NotNil(t, state)
		require.Equal(t, OpenAIAutoResetStatusFailed, state.Status)
		require.Equal(t, "OPENAI_QUOTA_CLIENT_ERROR", state.ErrorCode)
		require.False(t, state.AttemptUnconfirmed)

		// Next schedulable evaluation must NOT clear 429 because no POST was dispatched.
		quota.usage.RateLimit.Allowed = true
		quota.usage.RateLimit.LimitReached = false
		quota.usage.RateLimit.SecondaryWindow.UsedPercent = 0
		require.NoError(t, svc.evaluateAccount(context.Background(), 501))
		require.Zero(t, recoverer.calls.Load())
		require.Zero(t, recoverer.quotaRateLimitClear.Load())
	})

	t.Run("unconfirmed attempt followed by no_credit still clears stale 429 once schedulable", func(t *testing.T) {
		now := time.Now().UTC()
		rateLimitedAt := now.Add(-5 * time.Minute)
		svc, repo, quota := newResetThresholdTestFixture(t, nil, 1)
		quota.usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "credit-1", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}
		quota.failFirst = true
		repo.mu.Lock()
		repo.account.RateLimitedAt = &rateLimitedAt
		repo.account.RateLimitResetAt = ptrTime(now.Add(24 * time.Hour))
		repo.mu.Unlock()

		// Step 1: redemption times out -> AttemptUnconfirmed=true.
		err := svc.evaluateAccount(context.Background(), 501)
		require.Error(t, err)
		state := repo.stateForTest()
		require.NotNil(t, state)
		require.True(t, state.AttemptUnconfirmed)

		// Step 2: subsequent quota read sees 0 credits while weekly usage is still 100% -> no_credit must keep AttemptUnconfirmed=true.
		quota.usage.RateLimitResetCredits.AvailableCount = 0
		quota.usage.autoResetCandidates = nil
		repo.mu.Lock()
		state.RetryAfter = now.Add(-time.Minute).Format(time.RFC3339Nano)
		repo.account.Extra[OpenAIAutoResetCreditStateExtraKey] = state
		repo.account.Extra["codex_usage_updated_at"] = now.Add(-time.Hour).Format(time.RFC3339)
		repo.mu.Unlock()

		recoverer := &autoResetSwitchableRecoverer{}
		svc.recoverer = recoverer

		require.NoError(t, svc.evaluateAccount(context.Background(), 501))
		noCreditState := repo.stateForTest()
		require.Equal(t, OpenAIAutoResetStatusNoCredit, noCreditState.Status)
		require.True(t, noCreditState.AttemptUnconfirmed)
		require.Zero(t, recoverer.quotaRateLimitClear.Load())

		// Step 3: later quota read confirms schedulable usage -> must run QueryUsage, clear 429 once, and drop AttemptUnconfirmed.
		quota.usage.RateLimit.Allowed = true
		quota.usage.RateLimit.LimitReached = false
		quota.usage.RateLimit.SecondaryWindow.UsedPercent = 0
		repo.mu.Lock()
		repo.account.Extra["codex_usage_updated_at"] = now.Format(time.RFC3339)
		repo.account.Extra["codex_7d_used_percent"] = 0.0
		repo.mu.Unlock()

		require.NoError(t, svc.evaluateAccount(context.Background(), 501))
		require.Equal(t, int32(1), recoverer.quotaRateLimitClear.Load())
		require.Equal(t, OpenAIAutoResetStatusNoCredit, repo.stateForTest().Status)
		require.False(t, repo.stateForTest().AttemptUnconfirmed)
	})
}

type autoResetRealClientErrorQuota struct {
	*autoResetTestQuota
	real *OpenAIQuotaService
}

func (q *autoResetRealClientErrorQuota) ResetCreditTargeted(ctx context.Context, accountID int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error) {
	return q.real.ResetCreditTargeted(ctx, accountID, creditID, redeemRequestID)
}
