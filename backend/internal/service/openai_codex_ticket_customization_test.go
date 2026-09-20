package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketAccountPolicyIsConsistentAcrossGateInjectionAndHarvest(t *testing.T) {
	for _, mode := range []string{"inherit", "on", "off", "master-off", "default-off", "override-default-off", "bulk-off"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"}}
			account := ticketTestAccount(1)
			account.Status = StatusActive
			var policy CodexTicketPolicy
			expected := mode != "off" && mode != "master-off" && mode != "default-off" && mode != "bulk-off"
			on, off := true, false
			switch mode {
			case "on":
				policy.Enabled = &on
			case "off":
				policy.Enabled = &off
			case "master-off":
				cfg.Enabled = false
				policy.Enabled = &on
			case "default-off":
				cfg.DefaultAccountEnabled = &off
			case "override-default-off":
				cfg.DefaultAccountEnabled = &off
				policy.Enabled = &on
			case "bulk-off":
				policy.Enabled = &on
			}
			account.Extra = map[string]any{CodexTicketPolicyExtraKey: policy}
			if mode == "bulk-off" {
				account.Extra[CodexTicketEnabledExtraKey] = false
			}
			calls := atomic.Int64{}
			upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }}
			svc := ticketTestService(t, cfg, upstream)
			svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
			require.Equal(t, expected, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			h := http.Header{"X-Codex-Turn-State": []string{"client-value"}}
			err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
			if expected {
				require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
			} else {
				require.NoError(t, err)
				require.Equal(t, "client-value", h.Get(openAICodexTurnStateHeader))
			}
			svc.refreshOpenAICodexTickets(context.Background())
			if expected {
				require.Equal(t, int64(1), calls.Load())
				require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			} else {
				require.Zero(t, calls.Load())
				require.Empty(t, OpenAICodexTicketStatuses(account, cfg, time.Now()))
			}
		})
	}
}

func TestCodexTicketCustomPromptsAndModelScope(t *testing.T) {
	account := ticketTestAccount(41)
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://proxy.example:8080"})
	opts := CodexTicketOptionsFromConfig(cfg)
	opts.Models = []string{"custom-model"}
	opts.Instructions = ""
	opts.UserPrompt = "中文 \"quote\"\nsecond line"
	opts.RefreshBeforeSeconds = 0
	account.Extra = map[string]any{CodexTicketPolicyExtraKey: CodexTicketPolicy{Options: &opts}}
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, "", body["instructions"])
		require.Equal(t, "custom-model", body["model"])
		var decoded struct {
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Len(t, decoded.Input, 1)
		require.Len(t, decoded.Input[0].Content, 1)
		require.Equal(t, opts.UserPrompt, decoded.Input[0].Content[0].Text)
		return codexTicketResponse(), nil
	}}
	svc := ticketTestService(t, cfg, upstream)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "custom-model"))
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "custom-model")
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "custom-model"))
	view := svc.CodexTicketAccountView(context.Background(), account)
	require.Equal(t, 0, view.Options.RefreshBeforeSeconds)
	require.Empty(t, view.Options.Instructions)
}

func TestCodexTicketSharedConcurrencyAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080", MaxConcurrency: 1})
	svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}})
	account := ticketTestAccount(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, state := svc.launchTicketJob(ctx, account, "gpt-6-astra", cfg, false)
	require.Equal(t, "started", state)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
		return
	}
	_, state = svc.launchTicketJob(ctx, account, "gpt-6-astra", cfg, true)
	require.Equal(t, "running", state)
	_, state = svc.launchTicketJob(ctx, account, "gpt-5.6-sol", cfg, true)
	require.Equal(t, "busy", state)
	svc.cancelTicketJobs(account.ID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe did not cancel")
		return
	}
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.Zero(t, svc.ticketActive)
}

func TestCodexTicketFailureBackoffPreservesOldTicket(t *testing.T) {
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"}})
	calls := atomic.Int64{}
	svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}})
	account := ticketTestAccount(1)
	account.Status = StatusActive
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{AccountID: 1, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}))
	done, state := svc.launchTicketJob(context.Background(), account, "gpt-6-astra", cfg, true)
	require.Equal(t, "started", state)
	<-done
	view := svc.CodexTicketAccountView(context.Background(), account)
	require.Len(t, view.Tickets, 1)
	require.True(t, view.Tickets[0].Ready)
	require.False(t, view.Tickets[0].Blocked)
	require.Equal(t, "backoff", view.Progress[0].State)
	require.Equal(t, 429, view.Progress[0].HTTPStatus)
	require.GreaterOrEqual(t, view.Progress[0].NextAttempt.Sub(*view.Progress[0].LastAttempt), time.Minute)
	_, state = svc.launchTicketJob(context.Background(), account, "gpt-6-astra", cfg, true)
	require.Equal(t, "cooldown", state)
	require.Equal(t, int64(1), calls.Load())
	require.True(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra").valid(time.Now(), 292))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
}

func TestCodexTicketSettingsRoundTripAndValidation(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://user:secret@proxy.example:8080"})
	cfg.Instructions = ""
	cfg.UserPrompt = "custom"
	cfg.RefreshBeforeSeconds = 0
	require.NoError(t, settings.SaveCodexTicketConfig(context.Background(), cfg, false))
	got := settings.GetCodexTicketConfig(context.Background(), config.OpenAICodexTicketConfig{})
	require.Equal(t, "", got.Instructions)
	require.Zero(t, got.RefreshBeforeSeconds)
	require.Equal(t, "custom", got.UserPrompt)
	require.Equal(t, cfg.HarvestProxyURL, got.HarvestProxyURL)
	got.HarvestProxyURL = MaskProxyURL(got.HarvestProxyURL)
	require.NoError(t, settings.SaveCodexTicketConfig(context.Background(), got, false))
	require.Equal(t, cfg.HarvestProxyURL, settings.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	require.NoError(t, settings.SaveCodexTicketConfig(context.Background(), got, true))
	require.Empty(t, settings.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	invalid := got
	invalid.RefreshBeforeSeconds = invalid.TTLSeconds
	require.Error(t, settings.SaveCodexTicketConfig(context.Background(), invalid, false))
	require.Equal(t, 0, settings.GetCodexTicketConfig(context.Background(), config.OpenAICodexTicketConfig{}).RefreshBeforeSeconds)
	opts := CodexTicketOptionsFromConfig(cfg)
	opts.Models = []string{"duplicate", "duplicate"}
	require.Error(t, opts.Validate())
	require.Error(t, ValidateCodexTicketPolicyExtra(map[string]any{CodexTicketEnabledExtraKey: "false"}))
	require.NoError(t, ValidateCodexTicketPolicyExtra(map[string]any{CodexTicketEnabledExtraKey: nil}))
}

func (r *codexTicketSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}
