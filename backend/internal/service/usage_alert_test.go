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

func TestValidateUsageAlertRuleNormalizesQuotaDimension(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.QuotaDimension = ""
	require.NoError(t, validateUsageAlertRule(rule))
	require.Equal(t, QuotaDimensionGlobal, rule.QuotaDimension)

	rule = validUsageAlertRuleForTest()
	rule.QuotaDimension = QuotaDimensionSpark
	require.NoError(t, validateUsageAlertRule(rule))

	rule.QuotaDimension = "unknown"
	require.ErrorIs(t, validateUsageAlertRule(rule), ErrUsageAlertInvalidDimension)
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
		AccountID:      11,
		RealAccountID:  22,
		QuotaDimension: QuotaDimensionSpark,
		Platform:       UsageAlertPlatformOpenAI,
		Source:         UsageAlertSourceOpenAICodexHeaders,
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
		QuotaDimension:   QuotaDimensionSpark,
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
	require.Contains(t, message, "额度维度：Spark")
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

	realAccountID, quotaDimension := svc.resolveUsageAlertScope(context.Background(), 20)

	require.Equal(t, int64(99), realAccountID)
	require.Equal(t, QuotaDimensionSpark, quotaDimension)
	require.Equal(t, parentID, alertRepo.ensuredAccountID)
	require.Equal(t, int64(99), alertRepo.attachedRealID)
	require.Equal(t, int64(20), alertRepo.attachedAccountID)
}

func validUsageAlertRuleForTest() *UsageAlertRule {
	realAccountID := int64(1)
	return &UsageAlertRule{
		Name:            "weekly remaining low",
		Platform:        UsageAlertPlatformOpenAI,
		QuotaDimension:  QuotaDimensionGlobal,
		RealAccountID:   &realAccountID,
		Window:          UsageAlertWindow7d,
		Metric:          UsageAlertMetricRemaining,
		Operator:        UsageAlertOperatorLTE,
		Threshold:       20,
		CooldownMinutes: 60,
		Enabled:         true,
	}
}
