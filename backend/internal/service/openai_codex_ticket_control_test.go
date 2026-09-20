package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexTicketControlWaitsHonorCancellation(t *testing.T) {
	for _, mode := range []string{"snapshot", "guard", "mutation"} {
		t.Run(mode, func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = svc.stampTicketSnapshot(ctx)
			control := svc.ticketCoordinator()
			weight := ticketWriteWeight
			if mode == "mutation" {
				weight = 1
			}
			release, err := control.acquire(context.Background(), weight)
			require.NoError(t, err)
			defer release()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				switch mode {
				case "snapshot":
					result := svc.stampTicketSnapshot(ctx)
					done <- result.Err()
				case "guard":
					unlock, e := guardTicketSnapshot(ctx)
					if unlock != nil {
						unlock()
					}
					done <- e
				case "mutation":
					unlock, e := control.beginMutation(ctx, 41)
					if unlock != nil {
						unlock()
					}
					done <- e
				}
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				require.True(t, errors.Is(err, context.Canceled))
			case <-time.After(time.Second):
				t.Fatal("cancelled lock waiter did not return while the blocker was still held")
				return
			}
		})
	}
}
func TestCodexTicketStopDuringPolicyWrite(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.accountRepo = &codexTicketRefreshRepo{}
	svc.StartOpenAICodexTicketHarvester()
	release, err := svc.ticketCoordinator().acquire(context.Background(), ticketWriteWeight)
	require.NoError(t, err)
	defer release()
	done := make(chan struct{})
	go func() { svc.StopOpenAICodexTicketHarvester(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop waited for the unrelated policy writer")
		return
	}
}

type cancelledTicketLookup struct {
	ticketPolicyReviewRepo
	cancel context.CancelFunc
}

func (r *cancelledTicketLookup) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.cancel()
	return r.ticketPolicyReviewRepo.GetByID(ctx, id)
}
func TestCodexTicketCancelledManualValidationCannotReviveStartupPolicy(t *testing.T) {
	startup := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080"})
	calls := atomic.Int64{}
	svc := ticketTestService(t, startup, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }})
	settingsRepo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	svc.settingService = NewSettingService(settingsRepo, svc.cfg)
	disabled := startup
	disabled.Enabled = false
	svc.settingService.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: disabled, expires: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	account := ticketTestAccount(41)
	account.Status = StatusActive
	svc.accountRepo = &cancelledTicketLookup{ticketPolicyReviewRepo: ticketPolicyReviewRepo{account: *account}, cancel: cancel}
	svc.ticketContext = context.Background()
	state, err := svc.TriggerCodexTicket(ctx, 41, "gpt-6-astra")
	svc.ticketWorkers.Wait()
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, state)
	require.Zero(t, calls.Load())
	require.False(t, svc.settingService.GetCodexTicketConfig(ctx, startup).Enabled, "cancelled reads must retain the last effective disabled policy")
}
