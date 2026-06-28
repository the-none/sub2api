package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFormatUsageAlertTelegramMessageUsesLanguageAndTimezone(t *testing.T) {
	resetAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	event := UsageAlertWebhookEvent{
		Event:            "account.usage_alert",
		TriggeredAt:      time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC),
		RealAccountName:  "OpenAI Main",
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
	require.Contains(t, message, "阈值：剩余 <= 20.0%")
	require.Contains(t, message, "触发时间：2026-06-28 18:30:00")
	require.Contains(t, message, "重置时间：2026-06-28 20:00:00")
	require.NotContains(t, message, "UTC")
}

func TestUsageAlertTelegramConfigRejectsInvalidTimezone(t *testing.T) {
	_, err := usageAlertTelegramConfig(map[string]any{
		"bot_token": "123456:abcDEF",
		"chat_id":   "-1001234567890",
		"timezone":  "Mars/Olympus",
	})
	require.ErrorContains(t, err, "telegram timezone is invalid")
}

func validUsageAlertRuleForTest() *UsageAlertRule {
	realAccountID := int64(1)
	return &UsageAlertRule{
		Name:            "weekly remaining low",
		Platform:        UsageAlertPlatformOpenAI,
		RealAccountID:   &realAccountID,
		Window:          UsageAlertWindow7d,
		Metric:          UsageAlertMetricRemaining,
		Operator:        UsageAlertOperatorLTE,
		Threshold:       20,
		CooldownMinutes: 60,
		Enabled:         true,
	}
}
