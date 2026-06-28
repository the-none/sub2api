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
