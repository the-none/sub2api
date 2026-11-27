package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageAlertRealAccountResponseUsesLinkedAccountJSONShape(t *testing.T) {
	realAccountID := int64(10)
	item := &service.RealAccount{
		ID:        realAccountID,
		Name:      "Claude Main",
		Platform:  service.PlatformAnthropic,
		CreatedAt: time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 28, 10, 5, 0, 0, time.UTC),
		Accounts: []*service.Account{
			{
				ID:            42,
				Name:          "claude-a",
				Platform:      service.PlatformAnthropic,
				Type:          service.AccountTypeOAuth,
				Status:        service.StatusActive,
				RealAccountID: &realAccountID,
				Credentials:   map[string]any{"access_token": "secret"},
			},
		},
	}

	raw, err := json.Marshal(usageAlertRealAccountResponseFromService(item))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	accounts, ok := decoded["accounts"].([]any)
	require.True(t, ok)
	require.Len(t, accounts, 1)
	account, ok := accounts[0].(map[string]any)
	require.True(t, ok)

	require.Equal(t, float64(42), account["id"])
	require.Equal(t, "claude-a", account["name"])
	require.Equal(t, service.PlatformAnthropic, account["platform"])
	require.Equal(t, service.AccountTypeOAuth, account["type"])
	require.Equal(t, service.StatusActive, account["status"])
	require.Equal(t, service.QuotaDimensionGlobal, account["quota_dimension"])
	require.NotContains(t, account, "ID")
	require.NotContains(t, account, "Name")
	require.NotContains(t, account, "credentials")
	require.NotContains(t, account, "Credentials")
}

func TestUsageAlertRuleRequestAcceptsLegacyQuotaDimension(t *testing.T) {
	require.Equal(t, "global", (usageAlertRuleRequest{QuotaDimension: "global"}).resolvedUsageType())
	require.Equal(t, "fable", (usageAlertRuleRequest{UsageType: "fable", QuotaDimension: "global"}).resolvedUsageType())
}

func TestUsageAlertRealAccountResponsePreservesDeletedSourceWarning(t *testing.T) {
	raw, err := json.Marshal(usageAlertRealAccountResponseFromService(&service.RealAccount{
		ID: 3, Name: "Retired source", HasOnlyDeletedAccounts: true,
	}))
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, true, decoded["has_only_deleted_accounts"])
}

func TestUsageAlertNestedRealAccountOmitsUncomputedDeletionStatus(t *testing.T) {
	raw, err := json.Marshal(&service.UsageAlertRule{
		RealAccount: &service.RealAccount{ID: 3, Name: "Retired source"},
	})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "has_only_deleted_accounts")
}

type usageAlertHandlerRepoStub struct {
	service.UsageAlertRepository
	webhooks []*service.UsageAlertWebhook
	bindings []*service.UsageAlertBinding
}

func (s *usageAlertHandlerRepoStub) ListWebhooks(context.Context) ([]*service.UsageAlertWebhook, error) {
	return s.webhooks, nil
}

func (s *usageAlertHandlerRepoStub) GetWebhook(_ context.Context, id int64) (*service.UsageAlertWebhook, error) {
	for _, w := range s.webhooks {
		if w.ID == id {
			return w, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (s *usageAlertHandlerRepoStub) UpdateWebhook(_ context.Context, w *service.UsageAlertWebhook) (*service.UsageAlertWebhook, error) {
	return w, nil
}

func (s *usageAlertHandlerRepoStub) ListBindings(context.Context) ([]*service.UsageAlertBinding, error) {
	return s.bindings, nil
}

func TestUsageAlertHandlerRedactsBotTokenOnReadAndPreservesOnUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stored := &service.UsageAlertWebhook{
		ID: 4, Name: "Telegram", Type: service.UsageAlertWebhookTypeTelegram,
		Config: map[string]any{"bot_token": "999:TOPSECRET", "chat_id": "-1002"},
	}
	repo := &usageAlertHandlerRepoStub{
		webhooks: []*service.UsageAlertWebhook{stored},
		bindings: []*service.UsageAlertBinding{{ID: 1, RealAccountID: 2, WebhookID: 4, Webhook: stored}},
	}
	h := NewUsageAlertHandler(service.NewUsageAlertService(repo, nil))
	router := gin.New()
	router.GET("/webhooks", h.ListWebhooks)
	router.PUT("/webhooks/:id", h.UpdateWebhook)
	router.GET("/bindings", h.ListBindings)

	for _, path := range []string{"/webhooks", "/bindings"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "TOPSECRET")
		require.Contains(t, rec.Body.String(), `"has_bot_token":true`)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/webhooks/4", bytes.NewBufferString(`{"name":"Telegram 2","type":"telegram","config":{"bot_token":"","chat_id":"-1002"}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "TOPSECRET")
	require.Contains(t, rec.Body.String(), `"has_bot_token":true`)
}
