package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
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
