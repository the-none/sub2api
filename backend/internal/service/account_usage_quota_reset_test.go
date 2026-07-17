package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type quotaResetUsageRepo struct {
	AccountRepository
	account      *Account
	updateErr    error
	updateCalls  int
	lastUpdates  map[string]any
	operationLog *[]string
}

func (r *quotaResetUsageRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, errors.New("account not found")
	}
	return r.account, nil
}

func (r *quotaResetUsageRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updateCalls++
	r.lastUpdates = make(map[string]any, len(updates))
	for key, value := range updates {
		r.lastUpdates[key] = value
	}
	if r.operationLog != nil {
		*r.operationLog = append(*r.operationLog, "persist_snapshot")
	}
	return r.updateErr
}

type quotaResetSchedulerRecorder struct {
	accountIDs   []int64
	err          error
	operationLog *[]string
}

func (r *quotaResetSchedulerRecorder) ReconcileOpenAIQuotaReset(_ context.Context, accountID int64) error {
	r.accountIDs = append(r.accountIDs, accountID)
	if r.operationLog != nil {
		*r.operationLog = append(*r.operationLog, "clear_scheduler")
	}
	return r.err
}

func newQuotaResetProbeAccount() *Account {
	return &Account{
		ID:       701,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Extra: map[string]any{
			"codex_5h_used_percent": 100.0,
			"codex_7d_used_percent": 100.0,
		},
	}
}

func TestAccountUsageService_ReconcileOpenAIQuotaResetPersistsProbeBeforeClearingScheduler(t *testing.T) {
	operationLog := []string{}
	account := newQuotaResetProbeAccount()
	repo := &quotaResetUsageRepo{account: account, operationLog: &operationLog}
	scheduler := &quotaResetSchedulerRecorder{operationLog: &operationLog}
	svc := &AccountUsageService{
		accountRepo:         repo,
		quotaResetScheduler: scheduler,
		openAICodexProbeFn: func(context.Context, *Account) (*openAICodexProbeOutcome, error) {
			return &openAICodexProbeOutcome{
				StatusCode: 200,
				Updates: map[string]any{
					"codex_primary_used_percent":   3.0,
					"codex_secondary_used_percent": 4.0,
					"codex_5h_used_percent":        4.0,
					"codex_7d_used_percent":        3.0,
					"codex_usage_updated_at":       "2026-07-17T10:00:00Z",
				},
			}, nil
		},
	}

	err := svc.ReconcileOpenAIQuotaReset(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"persist_snapshot", "clear_scheduler"}, operationLog)
	require.Equal(t, []int64{account.ID}, scheduler.accountIDs)
	require.Equal(t, 4.0, repo.lastUpdates["codex_5h_used_percent"])
	require.Equal(t, 3.0, repo.lastUpdates["codex_7d_used_percent"])
	require.NotEqual(t, float64(0), repo.lastUpdates["codex_5h_used_percent"])
}

func TestAccountUsageService_ReconcileOpenAIQuotaResetRejects429Snapshot(t *testing.T) {
	account := newQuotaResetProbeAccount()
	repo := &quotaResetUsageRepo{account: account}
	scheduler := &quotaResetSchedulerRecorder{}
	svc := &AccountUsageService{
		accountRepo:         repo,
		quotaResetScheduler: scheduler,
		openAICodexProbeFn: func(context.Context, *Account) (*openAICodexProbeOutcome, error) {
			return &openAICodexProbeOutcome{
				StatusCode: 429,
				Updates: map[string]any{
					"codex_5h_used_percent": 100.0,
					"codex_7d_used_percent": 100.0,
				},
			}, nil
		},
	}

	err := svc.ReconcileOpenAIQuotaReset(context.Background(), account.ID)
	require.ErrorContains(t, err, "not confirmed")
	require.Zero(t, repo.updateCalls)
	require.Empty(t, scheduler.accountIDs)
}

func TestAccountUsageService_ReconcileOpenAIQuotaResetDoesNotClearWhenSnapshotPersistenceFails(t *testing.T) {
	account := newQuotaResetProbeAccount()
	repo := &quotaResetUsageRepo{account: account, updateErr: errors.New("database unavailable")}
	scheduler := &quotaResetSchedulerRecorder{}
	svc := &AccountUsageService{
		accountRepo:         repo,
		quotaResetScheduler: scheduler,
		openAICodexProbeFn: func(context.Context, *Account) (*openAICodexProbeOutcome, error) {
			return &openAICodexProbeOutcome{
				StatusCode: 200,
				Updates: map[string]any{
					"codex_5h_used_percent": 2.0,
					"codex_7d_used_percent": 1.0,
				},
			}, nil
		},
	}

	err := svc.ReconcileOpenAIQuotaReset(context.Background(), account.ID)
	require.ErrorContains(t, err, "persist confirmed quota reset snapshot")
	require.Equal(t, 1, repo.updateCalls)
	require.Empty(t, scheduler.accountIDs)
}
