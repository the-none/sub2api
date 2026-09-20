package service

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ticketSchedulingRepo struct{ ticketPolicyReviewRepo }

func (r *ticketSchedulingRepo) SetSchedulable(_ context.Context, _ int64, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Schedulable = enabled
	return nil
}
func (r *ticketSchedulingRepo) BulkUpdate(_ context.Context, _ []int64, updates AccountBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if updates.Schedulable != nil {
		r.account.Schedulable = *updates.Schedulable
	}
	return 1, nil
}
func (r *ticketSchedulingRepo) ListByPlatform(ctx context.Context, _ string) ([]Account, error) {
	account, err := r.GetByID(ctx, 41)
	if err != nil {
		return nil, err
	}
	return []Account{*account}, nil
}
func ticketSchedulingConfig() config.OpenAICodexTicketConfig {
	return normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"}})
}

func TestCodexTicketPausedSchedulingSkipsAutomaticAndManualHarvest(t *testing.T) {
	account := ticketTestAccount(41)
	account.Schedulable = false
	calls := atomic.Int64{}
	svc := ticketTestService(t, ticketSchedulingConfig(), &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }})
	repo := &ticketSchedulingRepo{ticketPolicyReviewRepo: ticketPolicyReviewRepo{account: *account}}
	svc.accountRepo = repo
	svc.ticketContext = context.Background()
	svc.refreshOpenAICodexTickets(context.Background())
	_, err := svc.TriggerCodexTicket(context.Background(), 41, "gpt-6-astra")
	require.ErrorContains(t, err, "scheduling is disabled")
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Zero(t, calls.Load())
	view := svc.CodexTicketAccountView(context.Background(), account)
	require.True(t, view.Enabled, "pausing scheduling must not rewrite the ticket policy")
	require.True(t, view.Eligible)
	require.Equal(t, "scheduling_paused", view.Progress[0].State)
	require.Nil(t, view.Progress[0].NextAttempt)
}

func TestCodexTicketSchedulingToggleCancelsAndResumesHarvest(t *testing.T) {
	for _, mode := range []string{"single", "bulk", "background"} {
		t.Run(mode, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Extra = map[string]any{CodexTicketEnabledExtraKey: true}
			repo := &ticketSchedulingRepo{ticketPolicyReviewRepo: ticketPolicyReviewRepo{account: *account}}
			started := make(chan struct{})
			calls := atomic.Int64{}
			cfg := ticketSchedulingConfig()
			svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return codexTicketResponse(), nil
			}})
			svc.accountRepo = repo
			svc.ticketContext = context.Background()
			svc.settingService = NewSettingService(nil, svc.cfg)
			svc.ticketCoordinator().cancel = svc.cancelTicketJobs
			admin := &adminServiceImpl{accountRepo: repo, settingService: svc.settingService}
			oldContext := svc.stampTicketSnapshot(context.Background())
			done, state := svc.launchTicketJob(oldContext, account, "gpt-6-astra", cfg, false)
			require.Equal(t, "started", state)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("probe did not start")
				return
			}
			paused := false
			switch mode {
			case "single":
				_, err := admin.SetAccountSchedulable(context.Background(), 41, false)
				require.NoError(t, err)
			case "bulk":
				_, err := admin.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{41}, Schedulable: &paused})
				require.NoError(t, err)
			case "background":
				require.NoError(t, repo.SetSchedulable(context.Background(), 41, false))
				svc.refreshOpenAICodexTickets(context.Background())
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("pausing scheduling did not cancel the probe")
				return
			}
			svc.refreshOpenAICodexTickets(context.Background())
			require.Equal(t, int64(1), calls.Load())
			latest, err := repo.GetByID(context.Background(), 41)
			require.NoError(t, err)
			require.Equal(t, true, latest.Extra[CodexTicketEnabledExtraKey])
			require.Equal(t, "scheduling_paused", svc.CodexTicketAccountView(context.Background(), latest).Progress[0].State)
			if mode != "background" {
				_, state = svc.launchTicketJob(oldContext, account, "gpt-6-astra", cfg, true)
				require.Equal(t, "stale", state, "an old validated snapshot cannot restart after the pause is acknowledged")
			}
			_, err = admin.SetAccountSchedulable(context.Background(), 41, true)
			require.NoError(t, err)
			svc.refreshOpenAICodexTickets(context.Background())
			require.Equal(t, int64(2), calls.Load(), "resuming scheduling must resume the existing enabled policy")
			latest, err = repo.GetByID(context.Background(), 41)
			require.NoError(t, err)
			require.True(t, svc.CodexTicketAccountView(context.Background(), latest).Tickets[0].Ready)
		})
	}
}

func TestCodexTicketTemporaryCooldownDoesNotPreventTicketRecovery(t *testing.T) {
	account := ticketTestAccount(41)
	until := time.Now().Add(time.Hour)
	account.RateLimitResetAt = &until
	account.TempUnschedulableUntil = &until
	calls := atomic.Int64{}
	svc := ticketTestService(t, ticketSchedulingConfig(), &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }})
	svc.accountRepo = &ticketSchedulingRepo{ticketPolicyReviewRepo: ticketPolicyReviewRepo{account: *account}}
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, int64(1), calls.Load(), "persistent scheduling remains on; temporary cooldown must not deadlock ticket recovery")
}

func TestCodexTicketResumingSchedulingDoesNotEnableDisabledPolicy(t *testing.T) {
	account := ticketTestAccount(41)
	account.Schedulable = false
	account.Extra = map[string]any{CodexTicketEnabledExtraKey: false}
	calls := atomic.Int64{}
	svc := ticketTestService(t, ticketSchedulingConfig(), &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }})
	repo := &ticketSchedulingRepo{ticketPolicyReviewRepo: ticketPolicyReviewRepo{account: *account}}
	svc.accountRepo = repo
	admin := &adminServiceImpl{accountRepo: repo}
	_, err := admin.SetAccountSchedulable(context.Background(), 41, true)
	require.NoError(t, err)
	svc.refreshOpenAICodexTickets(context.Background())
	require.Zero(t, calls.Load())
	latest, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, false, latest.Extra[CodexTicketEnabledExtraKey])
}
