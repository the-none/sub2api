package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type passiveUsageAccountRepoStub struct {
	AccountRepository
	account *Account
}

func (s *passiveUsageAccountRepoStub) GetByID(_ context.Context, _ int64) (*Account, error) {
	return s.account, nil
}

type passiveUsageLogRepoStub struct {
	UsageLogRepository
}

func (s *passiveUsageLogRepoStub) GetAccountWindowStats(_ context.Context, _ int64, _ time.Time) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{}, nil
}

type passiveUsageAlertRepoStub struct {
	UsageAlertRepository
	upsertCalled chan struct{}
}

func (s *passiveUsageAlertRepoStub) UpsertSnapshot(_ context.Context, _ *UsageAlertSnapshot) (bool, error) {
	select {
	case s.upsertCalled <- struct{}{}:
	default:
	}
	return true, nil
}

func (s *passiveUsageAlertRepoStub) ListEnabledRules(_ context.Context, _ int64, _ string) ([]*UsageAlertRule, error) {
	return nil, nil
}

func TestGetPassiveUsageDoesNotReplayCachedSampleIntoUsageAlerts(t *testing.T) {
	realAccountID := int64(9)
	resetAt := time.Now().Add(6 * time.Hour)
	accountRepo := &passiveUsageAccountRepoStub{account: &Account{
		ID:            2,
		Name:          "Claude linked account",
		Platform:      PlatformAnthropic,
		Type:          AccountTypeOAuth,
		RealAccountID: &realAccountID,
		Extra: map[string]any{
			"passive_usage_7d_utilization": 0.95,
			"passive_usage_7d_reset":       resetAt.Unix(),
			"passive_usage_sampled_at":     time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	}}
	alertRepo := &passiveUsageAlertRepoStub{upsertCalled: make(chan struct{}, 1)}
	usageService := NewAccountUsageService(
		accountRepo,
		&passiveUsageLogRepoStub{},
		nil, nil, nil, nil, nil, nil,
		NewUsageCache(),
		nil, nil,
	)
	usageService.SetUsageAlertService(NewUsageAlertService(alertRepo, accountRepo))

	usage, err := usageService.GetPassiveUsage(context.Background(), 2)

	require.NoError(t, err)
	require.NotNil(t, usage.SevenDay)
	require.Equal(t, 95.0, usage.SevenDay.Utilization)
	select {
	case <-alertRepo.upsertCalled:
		t.Fatal("passive usage read must not be observed as a new usage alert sample")
	case <-time.After(100 * time.Millisecond):
	}
}
