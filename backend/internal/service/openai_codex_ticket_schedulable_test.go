package service

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"sync"
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

type ticketSchedulingEditRepo struct {
	AccountRepository
	mu        sync.Mutex
	accounts  map[int64]*Account
	afterRead func()
}

func (r *ticketSchedulingEditRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	a := *r.accounts[id]
	a.Extra = maps.Clone(a.Extra)
	a.Credentials = maps.Clone(a.Credentials)
	r.mu.Unlock()
	if r.afterRead != nil {
		r.afterRead()
	}
	return &a, nil
}
func (r *ticketSchedulingEditRepo) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := *account
	r.accounts[account.ID] = &a
	return nil
}
func (r *ticketSchedulingEditRepo) UpdateExtra(_ context.Context, id int64, extra map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounts[id].Extra == nil {
		r.accounts[id].Extra = map[string]any{}
	}
	maps.Copy(r.accounts[id].Extra, extra)
	return nil
}
func (r *ticketSchedulingEditRepo) SetSchedulable(_ context.Context, id int64, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts[id].Schedulable = enabled
	return nil
}

func TestCodexTicketOrdinaryAccountEditDoesNotCancelHarvest(t *testing.T) {
	for _, editedID := range []int64{41, 99} {
		t.Run(fmt.Sprint(editedID), func(t *testing.T) {
			account := ticketTestAccount(41)
			repo := &ticketSchedulingEditRepo{accounts: map[int64]*Account{41: account, 99: {ID: 99, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}}}
			started, release := make(chan struct{}), make(chan struct{})
			cfg := ticketSchedulingConfig()
			svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				close(started)
				select {
				case <-release:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return codexTicketResponse(), nil
			}})
			svc.accountRepo = repo
			svc.settingService = NewSettingService(nil, svc.cfg)
			svc.ticketCoordinator().cancel = svc.cancelTicketJobs
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done, state := svc.launchTicketJob(svc.stampTicketSnapshot(ctx), account, "gpt-6-astra", cfg, false)
			require.Equal(t, "started", state)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("probe did not start")
			}
			admin := &adminServiceImpl{accountRepo: repo, settingService: svc.settingService}
			_, err := admin.UpdateAccount(context.Background(), editedID, &UpdateAccountInput{Name: "renamed"})
			require.NoError(t, err)
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("probe did not finish")
			}
			latest, err := repo.GetByID(context.Background(), 41)
			require.NoError(t, err)
			require.True(t, svc.CodexTicketAccountView(context.Background(), latest).Tickets[0].Ready, "ordinary editing must allow the in-flight probe to publish its ticket")
		})
	}
}

func TestCodexTicketOrdinaryEditSerializesWithSchedulingPause(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int64
	repo := &ticketSchedulingEditRepo{accounts: map[int64]*Account{41: ticketTestAccount(41)}, afterRead: func() {
		if reads.Add(1) == 1 {
			close(started)
			<-release
		}
	}}
	setting := NewSettingService(nil, &config.Config{})
	admin := &adminServiceImpl{accountRepo: repo, settingService: setting}
	edited := make(chan error, 1)
	go func() {
		_, err := admin.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Name: "renamed"})
		edited <- err
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("edit did not read the old scheduling state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := admin.SetAccountSchedulable(ctx, 41, false)
	require.ErrorIs(t, err, context.DeadlineExceeded, "pause must wait for the entire ordinary read/write operation")
	close(release)
	require.NoError(t, <-edited)
	_, err = admin.SetAccountSchedulable(context.Background(), 41, false)
	require.NoError(t, err)
	latest, err := repo.GetByID(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, "renamed", latest.Name)
	require.False(t, latest.Schedulable, "the ordinary edit must not restore the pre-pause snapshot")
}
