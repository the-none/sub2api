package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ticketFailureSettings struct {
	SettingRepository
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *ticketFailureSettings) GetValue(ctx context.Context, _ string) (string, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.release:
		return "", errors.New("settings unavailable")
	}
}

func TestCodexTicketFailedCacheRefreshCoalescesAndBacksOff(t *testing.T) {
	repo := &ticketFailureSettings{started: make(chan struct{}), release: make(chan struct{})}
	svc := NewSettingService(repo, &config.Config{})
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: false})
	svc.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: cfg, expires: time.Now().Add(-time.Second)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = svc.GetCodexTicketConfig(context.Background(), cfg) }()
	}
	<-repo.started
	close(repo.release)
	wg.Wait()
	require.Equal(t, int64(1), repo.calls.Load(), "a failed refresh must not turn waiters into sequential storage reads")
	_ = svc.GetCodexTicketConfig(context.Background(), cfg)
	require.Equal(t, int64(1), repo.calls.Load(), "failures must have a retry deadline")
}

func TestCodexTicketCacheWaitHonorsCallerCancellation(t *testing.T) {
	repo := &ticketFailureSettings{started: make(chan struct{}), release: make(chan struct{})}
	svc := NewSettingService(repo, &config.Config{})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	done := make(chan struct{})
	go func() { svc.GetCodexTicketConfig(firstCtx, config.OpenAICodexTicketConfig{}); close(done) }()
	<-repo.started
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondCancel()
	returned := make(chan struct{})
	go func() { svc.GetCodexTicketConfig(secondCtx, config.OpenAICodexTicketConfig{}); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("cancelled caller waited for the storage read")
		return
	}
	firstCancel()
	<-done
}

type ticketPolicyReviewRepo struct {
	AccountRepository
	mu             sync.Mutex
	account        Account
	updates        atomic.Int64
	persistStarted chan struct{}
	persistRelease chan struct{}
	policyWritten  chan struct{}
	order          []string
}

func (r *ticketPolicyReviewRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.account
	a.Extra = maps.Clone(a.Extra)
	return &a, nil
}
func (r *ticketPolicyReviewRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	_, isPolicy := updates[CodexTicketEnabledExtraKey]
	if !isPolicy && r.persistStarted != nil {
		close(r.persistStarted)
		<-r.persistRelease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	r.updates.Add(1)
	if isPolicy {
		r.order = append(r.order, "policy")
		if r.policyWritten != nil {
			close(r.policyWritten)
		}
	} else {
		r.order = append(r.order, "ticket")
	}
	return nil
}

type blockingTicketProxyRepo struct {
	ProxyRepository
	started chan struct{}
	release chan struct{}
}

func (r *blockingTicketProxyRepo) GetByID(context.Context, int64) (*Proxy, error) {
	close(r.started)
	<-r.release
	return &Proxy{ID: 9, Status: StatusActive, Protocol: "http", Host: "proxy.example", Port: 8080}, nil
}

func TestCodexTicketCloseRejectsAlreadyValidatedManualStart(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Extra = map[string]any{CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 9}}
	repo := &ticketPolicyReviewRepo{account: *account}
	proxyRepo := &blockingTicketProxyRepo{started: make(chan struct{}), release: make(chan struct{})}
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true})
	calls := atomic.Int64{}
	svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls.Add(1); return codexTicketResponse(), nil }})
	svc.accountRepo = repo
	svc.settingService = NewSettingService(nil, svc.cfg)
	svc.settingService.proxyRepo = proxyRepo
	svc.ticketContext = context.Background()
	svc.ticketCoordinator().cancel = svc.cancelTicketJobs
	result := make(chan error, 1)
	go func() { _, err := svc.TriggerCodexTicket(context.Background(), 41, "gpt-6-astra"); result <- err }()
	<-proxyRepo.started
	disabled := false
	require.NoError(t, svc.SaveCodexTicketPolicy(context.Background(), 41, CodexTicketPolicy{Enabled: &disabled}))
	close(proxyRepo.release)
	require.ErrorContains(t, <-result, "policy changed")
	require.Zero(t, calls.Load())
}

func TestCodexTicketMutationOrdersPersistenceAndRejectsStaleWrites(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	repo := &ticketPolicyReviewRepo{account: *account, persistStarted: make(chan struct{}), persistRelease: make(chan struct{}), policyWritten: make(chan struct{})}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = repo
	ctx := svc.stampTicketSnapshot(context.Background())
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Hour)}
	stored := make(chan error, 1)
	go func() { stored <- svc.storeOpenAICodexTicket(ctx, account, ticket) }()
	<-repo.persistStarted
	closed := make(chan error, 1)
	disabled := false
	go func() {
		closed <- svc.SaveCodexTicketPolicy(context.Background(), 41, CodexTicketPolicy{Enabled: &disabled})
	}()
	close(repo.persistRelease)
	require.NoError(t, <-stored)
	require.NoError(t, <-closed)
	require.Equal(t, []string{"ticket", "policy"}, repo.order, "an acknowledged policy write must be after any already-committing ticket")
	before := repo.updates.Load()
	require.ErrorIs(t, svc.storeOpenAICodexTicket(ctx, account, ticket), errCodexTicketPolicyChanged)
	require.Equal(t, before, repo.updates.Load(), "obsolete snapshots must never reach persistence")
	_, state := svc.launchTicketJob(ctx, account, "gpt-6-astra", normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true}), true)
	require.Equal(t, "stale", state)
}

func TestCodexTicketShutdownSeedsCacheAndCancelsRunningProbe(t *testing.T) {
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080"})
	settingsRepo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(settingsRepo, &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: cfg}})
	started := make(chan struct{})
	svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}})
	svc.settingService = settings
	settings.ticketControl.cancel = svc.cancelTicketJobs
	ctx := svc.stampTicketSnapshot(context.Background())
	done, state := svc.launchTicketJob(ctx, ticketTestAccount(41), "gpt-6-astra", cfg, true)
	require.Equal(t, "started", state)
	<-started
	cfg.Enabled = false
	require.NoError(t, settings.SaveCodexTicketConfig(context.Background(), cfg, false))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the active request")
		return
	}
	settingsRepo.err = errors.New("database offline")
	require.False(t, settings.GetCodexTicketConfig(context.Background(), settings.cfg.Gateway.OpenAICodexTicket).Enabled, "storage failure must not revive the startup enabled=true value")
}

func TestCodexTicketPolicyRevisionRejectsStaleEditor(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = &ticketPolicyReviewRepo{account: *account}
	revision := svc.CodexTicketAccountView(context.Background(), account).Revision
	disabled := false
	require.NoError(t, svc.SaveCodexTicketPolicyIfCurrent(context.Background(), 41, CodexTicketPolicy{Enabled: &disabled}, revision))
	require.ErrorIs(t, svc.SaveCodexTicketPolicyIfCurrent(context.Background(), 41, CodexTicketPolicy{}, revision), ErrCodexTicketPolicyConflict)
}

func TestCodexTicketColdCachePreservesStoredOptionsAndProxy(t *testing.T) {
	cfg := normalizeCodexTicketConfig(config.OpenAICodexTicketConfig{Enabled: true})
	cfg.UserPrompt = "stored prompt"
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{codexTicketConfigSettingKey: string(raw), SettingKeyOpenAICodexTicketHarvestProxyURL: "http://user:secret@proxy.example:8080"}}}
	svc := NewSettingService(repo, &config.Config{})
	// A legacy settings write knows only the master/proxy, not the saved options.
	svc.seedCodexTicketConfig(map[string]string{SettingKeyOpenAICodexTicketEnabled: "true"})
	require.Equal(t, "stored prompt", svc.GetCodexTicketConfig(context.Background(), config.OpenAICodexTicketConfig{}).UserPrompt)
	cold := NewSettingService(repo, &config.Config{})
	cfg.HarvestProxyURL = "http://user:***@proxy.example:8080"
	require.NoError(t, cold.SaveCodexTicketConfig(context.Background(), cfg, false))
	require.Equal(t, "http://user:secret@proxy.example:8080", cold.GetCodexTicketConfig(context.Background(), config.OpenAICodexTicketConfig{}).HarvestProxyURL)
}

type staticTicketReviewProxyRepo struct {
	ProxyRepository
	proxy *Proxy
}

func (r *staticTicketReviewProxyRepo) GetByID(context.Context, int64) (*Proxy, error) {
	return r.proxy, nil
}

func TestCodexTicketCreateValidatesProxyAndGenericEditPreservesPolicy(t *testing.T) {
	for _, status := range []string{StatusActive, StatusDisabled} {
		t.Run(status, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{}}
			svc := &adminServiceImpl{accountRepo: repo, proxyRepo: &staticTicketReviewProxyRepo{proxy: &Proxy{ID: 9, Status: status}}}
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: map[string]any{CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 9}}, SkipDefaultGroupBind: true})
			if status == StatusActive {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "proxy is unavailable")
				require.Empty(t, repo.accounts)
			}
		})
	}
	account := ticketTestAccount(41)
	account.Extra = map[string]any{CodexTicketEnabledExtraKey: false, CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 9}}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: map[string]any{CodexTicketEnabledExtraKey: true, CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 11}, "note": "new"}})
	require.NoError(t, err)
	require.Equal(t, false, updated.Extra[CodexTicketEnabledExtraKey])
	require.Equal(t, int64(9), *CodexTicketProxyID(updated))
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), 41, map[string]any{CodexTicketEnabledExtraKey: true, CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 11}}))
	require.Equal(t, false, repo.accounts[41].Extra[CodexTicketEnabledExtraKey])
	require.Equal(t, int64(9), *CodexTicketProxyID(repo.accounts[41]))
}
