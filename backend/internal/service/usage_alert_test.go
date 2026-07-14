package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateUsageAlertWebhookByType(t *testing.T) {
	jsonPost := &UsageAlertWebhook{
		Name:       "json",
		Type:       UsageAlertWebhookTypeJSONPost,
		RetryCount: 1,
	}
	require.ErrorContains(t, validateUsageAlertWebhook(jsonPost), "webhook url is required")

	telegram := &UsageAlertWebhook{
		Name: "telegram",
		Type: UsageAlertWebhookTypeTelegram,
		Config: map[string]any{
			"bot_token": "123456:abcDEF",
			"chat_id":   "-1001234567890",
		},
		RetryCount: 1,
	}
	require.NoError(t, validateUsageAlertWebhook(telegram))
}

func TestValidateUsageAlertRuleRejectsInvalidStepPercent(t *testing.T) {
	negative := -0.1
	rule := validUsageAlertRuleForTest()
	rule.StepPercent = &negative
	require.ErrorContains(t, validateUsageAlertRule(rule), "step_percent must be between 0 and 100")

	tooLarge := 100.1
	rule = validUsageAlertRuleForTest()
	rule.StepPercent = &tooLarge
	require.ErrorContains(t, validateUsageAlertRule(rule), "step_percent must be between 0 and 100")

	zero := 0.0
	rule = validUsageAlertRuleForTest()
	rule.StepPercent = &zero
	require.NoError(t, validateUsageAlertRule(rule))
}

func TestValidateUsageAlertRuleRejectsSonnetWindow(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.Window = "7d_sonnet"

	require.ErrorIs(t, validateUsageAlertRule(rule), ErrUsageAlertInvalidWindow)
}

func TestValidateUsageAlertRuleNormalizesUsageType(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.UsageType = ""
	require.NoError(t, validateUsageAlertRule(rule))
	require.Equal(t, UsageAlertTypeOverall, rule.UsageType)

	rule = validUsageAlertRuleForTest()
	rule.UsageType = UsageAlertTypeSpark
	require.NoError(t, validateUsageAlertRule(rule))

	rule.UsageType = "bad type!"
	require.ErrorIs(t, validateUsageAlertRule(rule), ErrUsageAlertInvalidUsageType)
}

func TestUsageAlertTypeSupportsPlatformAndWindow(t *testing.T) {
	require.True(t, usageAlertTypeSupportsRule(UsageAlertPlatformOpenAI, UsageAlertTypeSpark, UsageAlertWindow5h))
	require.True(t, usageAlertTypeSupportsRule(UsageAlertPlatformAnthropic, UsageAlertTypeFable, UsageAlertWindow7d))
	require.False(t, usageAlertTypeSupportsRule(UsageAlertPlatformAnthropic, UsageAlertTypeFable, UsageAlertWindow5h))
	require.False(t, usageAlertTypeSupportsRule(UsageAlertPlatformOpenAI, UsageAlertTypeFable, UsageAlertWindow7d))
}

func TestUsageAlertSnapshotsKeepFableAsOverallSubLimit(t *testing.T) {
	overallReset := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	fableReset := overallReset.Add(time.Hour)
	snapshots := usageAlertSnapshotsFromUsageInfo(42, UsageAlertPlatformAnthropic, UsageAlertSourceClaudeUsageAPI, &UsageInfo{
		FiveHour:      &UsageProgress{Utilization: 25},
		SevenDay:      &UsageProgress{Utilization: 40, ResetsAt: &overallReset},
		SevenDayFable: &UsageProgress{Utilization: 65, ResetsAt: &fableReset},
	}, time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC))

	require.Len(t, snapshots, 2)
	require.Equal(t, UsageAlertTypeOverall, snapshots[0].UsageType)
	require.Equal(t, UsageAlertRelationPrimary, snapshots[0].UsageRelation)
	require.Equal(t, 40.0, snapshots[0].Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, UsageAlertTypeFable, snapshots[1].UsageType)
	require.Equal(t, UsageAlertRelationSubLimit, snapshots[1].UsageRelation)
	require.Equal(t, UsageAlertTypeOverall, snapshots[1].ParentUsageType)
	require.Equal(t, 65.0, snapshots[1].Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, overallReset, *snapshots[1].Windows[UsageAlertWindow7d].ResetAt)
}

func TestEnsureUsageAlertRuleNameBuildsDefault(t *testing.T) {
	step := 5.0
	minReset := 24.0
	rule := validUsageAlertRuleForTest()
	rule.Name = " "
	rule.RealAccount = &RealAccount{Name: "OpenAI Main"}
	rule.StepPercent = &step
	rule.MinResetAfterHours = &minReset

	ensureUsageAlertRuleName(rule)

	require.Contains(t, rule.Name, "OpenAI Main")
	require.Contains(t, rule.Name, "OpenAI")
	require.Contains(t, rule.Name, "7d")
	require.Contains(t, rule.Name, "remaining")
	require.Contains(t, rule.Name, "<= 20%")
	require.Contains(t, rule.Name, "step 5%")
	require.Contains(t, rule.Name, "reset left 24h")
	require.Contains(t, rule.Name, "cooldown 60m")
}

func TestEnsureUsageAlertRuleNameTruncatesByRune(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.Name = strings.Repeat("测", usageAlertRuleNameMaxLength+5)

	ensureUsageAlertRuleName(rule)

	require.Len(t, []rune(rule.Name), usageAlertRuleNameMaxLength)
}

func TestUsageAlertStepAllowsTriggerRequiresCooldownAndStep(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	step := 5.0
	lastValue := 18.0
	lastTriggeredAt := now.Add(-30 * time.Minute)
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorLTE,
		StepPercent:     &step,
		CooldownMinutes: 60,
	}
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
	}

	require.True(t, usageAlertStepAllowsTrigger(nil, rule, 18, now))
	require.False(t, usageAlertStepAllowsTrigger(state, rule, 12, now))

	lastTriggeredAt = now.Add(-2 * time.Hour)
	state.LastTriggeredAt = &lastTriggeredAt
	require.False(t, usageAlertStepAllowsTrigger(state, rule, 14, now))
	require.True(t, usageAlertStepAllowsTrigger(state, rule, 13, now))
}

func TestUsageAlertStepAllowsTriggerSupportsIncreasingMetric(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	step := 5.0
	lastValue := 80.0
	lastTriggeredAt := now.Add(-2 * time.Hour)
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorGTE,
		StepPercent:     &step,
		CooldownMinutes: 60,
	}
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
	}

	require.False(t, usageAlertStepAllowsTrigger(state, rule, 84, now))
	require.True(t, usageAlertStepAllowsTrigger(state, rule, 85, now))
}

func TestDeliverJSONPostWebhook(t *testing.T) {
	var got UsageAlertWebhookEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Sub2API-UsageAlert/1.0", r.Header.Get("User-Agent"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	svc := &UsageAlertService{httpClient: server.Client()}
	event := UsageAlertWebhookEvent{
		Event:       "account.usage_alert",
		EventID:     "evt-test",
		TriggeredAt: time.Now().UTC(),
		RuleName:    "low remaining",
		Window:      UsageAlertWindow7d,
	}
	err := svc.deliverWebhookWithRetry(context.Background(), UsageAlertWebhook{
		Name:       "json",
		Type:       UsageAlertWebhookTypeJSONPost,
		URL:        server.URL,
		Enabled:    true,
		RetryCount: 0,
	}, event)
	require.NoError(t, err)
	require.Equal(t, "evt-test", got.EventID)
}

func TestRedactUsageAlertSecret(t *testing.T) {
	got := redactUsageAlertSecret(`Post "https://api.telegram.org/bot123456:abcDEF/sendMessage": timeout`, "123456:abcDEF")
	require.NotContains(t, got, "123456:abcDEF")
	require.Contains(t, got, "[redacted]")
}

func TestBuildUsageAlertWebhookEventUsesResolvedType(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	triggeredAt := time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC)
	snapshot := &UsageAlertSnapshot{
		AccountID:     11,
		RealAccountID: 22,
		UsageType:     UsageAlertTypeSpark,
		Platform:      UsageAlertPlatformOpenAI,
		Source:        UsageAlertSourceOpenAICodexHeaders,
	}

	event := buildUsageAlertWebhookEvent(snapshot, &RealAccount{Name: "OpenAI Main"}, UsageAlertTrigger{
		Rule:        rule,
		Window:      rule.Window,
		Value:       90,
		WindowState: UsageAlertWindowSnapshot{UsedPercent: 10, RemainingPercent: 90},
		TriggeredAt: triggeredAt,
		Resolved:    true,
	})

	require.Equal(t, UsageAlertEventResolved, event.Event)
	require.Equal(t, "OpenAI Main", event.RealAccountName)
	require.Equal(t, UsageAlertTypeSpark, event.UsageType)
	require.Equal(t, QuotaDimensionSpark, event.QuotaDimension)
	require.Contains(t, event.EventID, "-spark-")
	require.Equal(t, 90.0, event.Value)
}

func TestFormatUsageAlertTelegramMessageUsesLanguageAndTimezone(t *testing.T) {
	resetAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	event := UsageAlertWebhookEvent{
		Event:            "account.usage_alert",
		TriggeredAt:      time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC),
		RealAccountName:  "OpenAI Main",
		UsageType:        UsageAlertTypeSpark,
		Platform:         UsageAlertPlatformOpenAI,
		RuleName:         "weekly remaining low",
		Window:           UsageAlertWindow7d,
		Metric:           UsageAlertMetricRemaining,
		Operator:         UsageAlertOperatorLTE,
		Threshold:        20,
		UsedPercent:      81.5,
		RemainingPercent: 18.5,
		ResetAt:          &resetAt,
	}

	message := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "zh",
		Timezone: "Asia/Shanghai",
	})

	require.Contains(t, message, "[Sub2API] 用量告警")
	require.Contains(t, message, "账户：OpenAI Main")
	require.Contains(t, message, "用量类型：Spark")
	require.Contains(t, message, "阈值：剩余 <= 20.0%")
	require.Contains(t, message, "触发时间：2026-06-28 18:30:00")
	require.Contains(t, message, "重置时间：2026-06-28 20:00:00")
	require.NotContains(t, message, "UTC")
}

func TestFormatUsageAlertTelegramMessageUsesResetTitle(t *testing.T) {
	event := UsageAlertWebhookEvent{
		Event:            UsageAlertEventResolved,
		TriggeredAt:      time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC),
		RealAccountName:  "OpenAI Main",
		Platform:         UsageAlertPlatformOpenAI,
		RuleName:         "weekly remaining low",
		Window:           UsageAlertWindow7d,
		Metric:           UsageAlertMetricRemaining,
		Operator:         UsageAlertOperatorLTE,
		Threshold:        20,
		UsedPercent:      10,
		RemainingPercent: 90,
	}

	zh := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "zh",
		Timezone: "Asia/Shanghai",
	})
	require.Contains(t, zh, "[Sub2API] 用量告警已重置")
	require.Contains(t, zh, "重置通知时间：2026-06-28 18:30:00")

	en := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "en",
		Timezone: "UTC",
	})
	require.Contains(t, en, "[Sub2API] Usage alert reset")
	require.Contains(t, en, "Reset notified: 2026-06-28 10:30:00")
}

func TestUsageAlertTelegramConfigRejectsInvalidTimezone(t *testing.T) {
	_, err := usageAlertTelegramConfig(map[string]any{
		"bot_token": "123456:abcDEF",
		"chat_id":   "-1001234567890",
		"timezone":  "Mars/Olympus",
	})
	require.ErrorContains(t, err, "telegram timezone is invalid")
}

type usageAlertAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (s *usageAlertAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	return s.accounts[id], nil
}

type usageAlertRepoStub struct {
	UsageAlertRepository
	ensuredAccountID  int64
	attachedRealID    int64
	attachedAccountID int64
}

func (s *usageAlertRepoStub) EnsureRealAccountForAccount(_ context.Context, account *Account) (*RealAccount, error) {
	s.ensuredAccountID = account.ID
	return &RealAccount{ID: 99, Name: account.Name, Platform: account.Platform}, nil
}

func (s *usageAlertRepoStub) AttachAccount(_ context.Context, realAccountID, accountID int64) error {
	s.attachedRealID = realAccountID
	s.attachedAccountID = accountID
	return nil
}

func TestResolveUsageAlertScopeSharesRealAccountButSeparatesSparkQuota(t *testing.T) {
	parentID := int64(10)
	accountRepo := &usageAlertAccountRepoStub{accounts: map[int64]*Account{
		parentID: {
			ID:       parentID,
			Name:     "OpenAI Main",
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
		},
		20: {
			ID:              20,
			Name:            "OpenAI Main (Spark)",
			Platform:        PlatformOpenAI,
			Type:            AccountTypeOAuth,
			ParentAccountID: &parentID,
			QuotaDimension:  QuotaDimensionSpark,
		},
	}}
	alertRepo := &usageAlertRepoStub{}
	svc := NewUsageAlertService(alertRepo, accountRepo)

	realAccountID, usageType := svc.resolveUsageAlertScope(context.Background(), 20)

	require.Equal(t, int64(99), realAccountID)
	require.Equal(t, UsageAlertTypeSpark, usageType)
	require.Equal(t, parentID, alertRepo.ensuredAccountID)
	require.Equal(t, int64(99), alertRepo.attachedRealID)
	require.Equal(t, int64(20), alertRepo.attachedAccountID)
}

type rejectedUsageAlertSnapshotRepoStub struct {
	UsageAlertRepository
	previous      *UsageAlertSnapshot
	upsertCalls   int
	listRuleCalls int
}

func (s *rejectedUsageAlertSnapshotRepoStub) GetSnapshot(_ context.Context, _ int64, _ string) (*UsageAlertSnapshot, error) {
	return s.previous, nil
}

func (s *rejectedUsageAlertSnapshotRepoStub) UpsertSnapshot(_ context.Context, _ *UsageAlertSnapshot) (bool, error) {
	s.upsertCalls++
	return false, nil
}

func (s *rejectedUsageAlertSnapshotRepoStub) ListEnabledRules(_ context.Context, _ int64, _ string) ([]*UsageAlertRule, error) {
	s.listRuleCalls++
	return nil, nil
}

func TestObserveAsyncDoesNotEvaluateRejectedStaleSnapshot(t *testing.T) {
	now := time.Now().UTC()
	repo := &rejectedUsageAlertSnapshotRepoStub{
		previous: &UsageAlertSnapshot{SampledAt: now},
	}
	svc := NewUsageAlertService(repo, nil)

	svc.observeAsync(UsageAlertSnapshot{
		AccountID:     2,
		RealAccountID: 10,
		UsageType:     UsageAlertTypeOverall,
		Platform:      UsageAlertPlatformAnthropic,
		SampledAt:     now.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5},
		},
	})

	require.Equal(t, 1, repo.upsertCalls)
	require.Zero(t, repo.listRuleCalls)
}

func TestPrepareUsageAlertSnapshotRejectsLateSampleFromOlderResetWindow(t *testing.T) {
	oldReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 5, RemainingPercent: 95, ResetAt: &newReset},
		},
	}
	current := UsageAlertSnapshot{
		SampledAt: oldReset.Add(time.Hour),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldReset},
		},
	}

	_, _, accepted := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
}

func TestPrepareUsageAlertSnapshotKeepsFreshWindowAndPreservesNewerLinkedWindow(t *testing.T) {
	oldWeeklyReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newWeeklyReset := oldWeeklyReset.Add(7 * 24 * time.Hour)
	fiveHourReset := oldWeeklyReset.Add(5 * time.Hour)
	previousWeekly := UsageAlertWindowSnapshot{UsedPercent: 5, RemainingPercent: 95, ResetAt: &newWeeklyReset}
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{UsageAlertWindow7d: previousWeekly},
	}
	currentFiveHour := UsageAlertWindowSnapshot{UsedPercent: 25, RemainingPercent: 75, ResetAt: &fiveHourReset}
	current := UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow5h: currentFiveHour,
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldWeeklyReset},
		},
	}

	evaluation, persisted, accepted := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.Equal(t, map[string]UsageAlertWindowSnapshot{UsageAlertWindow5h: currentFiveHour}, evaluation.Windows)
	require.Equal(t, currentFiveHour, persisted.Windows[UsageAlertWindow5h])
	require.Equal(t, previousWeekly, persisted.Windows[UsageAlertWindow7d])
}

func TestPrepareUsageAlertSnapshotRejectsUnknownGenerationAfterKnownReset(t *testing.T) {
	resetAt := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 5, RemainingPercent: 95, ResetAt: &resetAt},
		},
	}
	current := UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5},
		},
	}

	_, _, accepted := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
}

type usageAlertGenerationStateRepoStub struct {
	UsageAlertRepository
	state            *UsageAlertState
	upsertStateCalls int
}

func (s *usageAlertGenerationStateRepoStub) GetState(_ context.Context, _, _ int64, _, _ string) (*UsageAlertState, error) {
	return s.state, nil
}

func (s *usageAlertGenerationStateRepoStub) UpsertState(_ context.Context, _ *UsageAlertState) error {
	s.upsertStateCalls++
	return nil
}

func TestEvaluateRulesRejectsWindowOlderThanPersistedStateReset(t *testing.T) {
	oldReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	repo := &usageAlertGenerationStateRepoStub{
		state: &UsageAlertState{LastStatus: UsageAlertStatusNormal, LastResetAt: &newReset},
	}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldReset},
		},
	}

	triggers := svc.evaluateRules(context.Background(), nil, current, []*UsageAlertRule{rule})

	require.Empty(t, triggers)
	require.Zero(t, repo.upsertStateCalls)
}

func validUsageAlertRuleForTest() *UsageAlertRule {
	realAccountID := int64(1)
	return &UsageAlertRule{
		Name:            "weekly remaining low",
		Platform:        UsageAlertPlatformOpenAI,
		UsageType:       UsageAlertTypeOverall,
		RealAccountID:   &realAccountID,
		Window:          UsageAlertWindow7d,
		Metric:          UsageAlertMetricRemaining,
		Operator:        UsageAlertOperatorLTE,
		Threshold:       20,
		CooldownMinutes: 60,
		Enabled:         true,
	}
}
