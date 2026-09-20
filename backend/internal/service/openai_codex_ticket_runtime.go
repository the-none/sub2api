package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

type CodexTicketProgress struct {
	Model               string     `json:"model"`
	State               string     `json:"state"`
	Attempts            int        `json:"attempts"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastAttempt         *time.Time `json:"last_attempt,omitempty"`
	LastSuccess         *time.Time `json:"last_success,omitempty"`
	NextAttempt         *time.Time `json:"next_attempt,omitempty"`
	Reason              string     `json:"reason,omitempty"`
	HTTPStatus          int        `json:"http_status,omitempty"`
	TicketLength        int        `json:"ticket_length,omitempty"`
}

type codexTicketJob struct {
	CodexTicketProgress
	accountID   int64
	fingerprint string
	cancel      context.CancelFunc
}

type ticketAttemptResult struct {
	reason         string
	status, length int
	success        bool
}

func (s *OpenAIGatewayService) ticketProxy(ctx context.Context, account *Account, cfg config.OpenAICodexTicketConfig) (string, error) {
	p := ticketPolicy(account)
	if p.ProxyID == nil {
		return cfg.HarvestProxyURL, nil
	}
	if s.settingService == nil || s.settingService.proxyRepo == nil {
		return "", errors.New("proxy_unavailable")
	}
	proxy, err := s.settingService.proxyRepo.GetByID(ctx, *p.ProxyID)
	if err != nil || proxy == nil || proxy.Status != StatusActive {
		return "", errors.New("proxy_unavailable")
	}
	return proxy.URL(), nil
}

func ticketFingerprint(account *Account, cfg config.OpenAICodexTicketConfig) string {
	// Used only in process memory; never sent to logs or the browser.
	b, _ := json.Marshal(struct {
		Config config.OpenAICodexTicketConfig
		Policy CodexTicketPolicy
	}{cfg, ticketPolicy(account)})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func ticketBackoff(cfg config.OpenAICodexTicketConfig, failures, status int) time.Duration {
	delay := time.Duration(cfg.HarvestProbeIntervalSeconds) * time.Second
	capDelay := time.Duration(cfg.MaxBackoffSeconds) * time.Second
	for i := 1; i < failures && delay < capDelay; i++ {
		delay = min(delay*2, capDelay)
	}
	if status == 401 || status == 403 || status == 429 {
		if minimum := min(time.Minute, capDelay); delay < minimum {
			delay = minimum
		}
	}
	// Jitter never shortens the configured minimum interval or exceeds the cap.
	return min(delay+time.Duration(rand.Float64()*float64(delay)/5), capDelay)
}

func (s *OpenAIGatewayService) attemptCodexTicket(ctx context.Context, account *Account, model string, cfg config.OpenAICodexTicketConfig, attempts int) (result ticketAttemptResult) {
	if s == nil || account == nil {
		return ticketAttemptResult{reason: "disabled"}
	}
	defer func() {
		fields := []zap.Field{zap.Int64("account_id", account.ID), zap.String("model", model), zap.Int("http", result.status), zap.Int("len", result.length), zap.Int("attempts", attempts)}
		if result.success {
			logger.L().Info("openai_codex_ticket harvested", fields...)
		} else {
			logger.L().Info("openai_codex_ticket probe miss", append(fields, zap.String("reason", result.reason))...)
		}
	}()

	if !ticketModelEnabled(cfg, model) || ctx.Err() != nil {
		return ticketAttemptResult{reason: "disabled"}
	}
	proxy, err := s.ticketProxy(ctx, account, cfg)
	if err != nil {
		return ticketAttemptResult{reason: "proxy_unavailable"}
	}
	if proxy == "" {
		return ticketAttemptResult{reason: "proxy_missing"}
	}
	if s.httpUpstream == nil {
		return ticketAttemptResult{reason: "transport_unavailable"}
	}
	value, _, _ := s.openaiCodexTicketFlight.Do(openAICodexTicketKey(account.ID, model), func() (any, error) {
		result := ticketAttemptResult{}
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			return ticketAttemptResult{reason: "token"}, nil
		}
		state, status, err := s.fireCodexTicketProbe(ctx, account, token, model, proxy, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second, cfg)
		result.status, result.length = status, len(state)
		switch {
		case ctx.Err() != nil:
			result.reason = "cancelled"
		case err != nil:
			result.reason = "transport_error"
		case status != http.StatusOK:
			result.reason = "http_error"
		case state == "":
			result.reason = "header_missing"
		case len(state) != cfg.TargetLength:
			result.reason = "length_mismatch"
		case !strings.HasPrefix(state, openAICodexTicketStatePrefix):
			result.reason = "prefix_mismatch"
		default:
			now := time.Now()
			persistErr := s.storeOpenAICodexTicket(ctx, account, &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state), CapturedAt: now, ExpiresAt: now.Add(time.Duration(cfg.TTLSeconds) * time.Second), Attempts: attempts})
			if persistErr != nil {
				result.reason = "persist_failed"
			}
			result.success = ctx.Err() == nil && !errors.Is(persistErr, errCodexTicketPolicyChanged)
			if !result.success {
				result.reason = "cancelled"
			}
		}
		return result, nil
	})
	result, ok := value.(ticketAttemptResult)
	if !ok {
		return ticketAttemptResult{reason: "cancelled"}
	}

	return result
}

// launchTicketJob reserves both a model key and a process-wide slot before
// starting work. Manual and automatic probes share the same limits and cooldowns.
func (s *OpenAIGatewayService) launchTicketJob(ctx context.Context, account *Account, model string, cfg config.OpenAICodexTicketConfig, manual bool) (<-chan struct{}, string) {
	release, err := guardTicketSnapshot(ctx)
	if err != nil {
		return nil, "stale"
	}
	defer release()
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || ctx.Err() != nil {
		return nil, "stopped"
	}
	s.ticketJobsMu.Lock()
	defer s.ticketJobsMu.Unlock()
	if s.ticketJobs == nil {
		s.ticketJobs = make(map[string]*codexTicketJob)
	}
	key := openAICodexTicketKey(account.ID, model)
	fingerprint := ticketFingerprint(account, cfg)
	job := s.ticketJobs[key]
	if job == nil {
		job = &codexTicketJob{accountID: account.ID, CodexTicketProgress: CodexTicketProgress{Model: model}}
		s.ticketJobs[key] = job
	}
	if job.cancel != nil {
		return nil, "running"
	}
	if job.fingerprint != fingerprint {
		job.NextAttempt = nil
		job.ConsecutiveFailures = 0
		job.fingerprint = fingerprint
	}
	now := time.Now()
	if job.NextAttempt != nil && now.Before(*job.NextAttempt) && (!manual || job.ConsecutiveFailures > 0) {
		return nil, "cooldown"
	}
	if s.ticketActive >= cfg.MaxConcurrency {
		return nil, "busy"
	}
	if !manual {
		t := s.lookupCodexTicketWithLength(account, model, cfg.TargetLength)
		if t.valid(now, cfg.TargetLength) && !t.needsRefresh(now, time.Duration(cfg.RefreshBeforeSeconds)*time.Second) {
			return nil, "ready"
		}
	}
	workCtx, cancel := context.WithCancel(ctx)
	job.cancel = cancel
	job.State = "harvesting"
	job.LastAttempt = &now
	job.Attempts++
	attempt := job.Attempts
	s.ticketActive++
	s.ticketWorkers.Add(1)
	done := make(chan struct{})
	acc := *account
	acc.Extra, acc.Credentials = maps.Clone(account.Extra), maps.Clone(account.Credentials)
	go func() {
		defer s.ticketWorkers.Done()
		defer close(done)
		defer cancel()
		result := s.attemptCodexTicket(workCtx, &acc, model, cfg, attempt)
		s.ticketJobsMu.Lock()
		defer s.ticketJobsMu.Unlock()
		s.ticketActive--
		job.cancel = nil
		if workCtx.Err() != nil {
			job.State = "waiting"
			job.Reason = "cancelled"
			job.NextAttempt = nil
			return
		}
		job.Reason, job.HTTPStatus, job.TicketLength = result.reason, result.status, result.length
		now := time.Now()
		var next time.Time
		if result.success {
			job.State = "ready"
			job.ConsecutiveFailures = 0
			job.LastSuccess = &now
			next = now.Add(time.Duration(cfg.TTLSeconds-cfg.RefreshBeforeSeconds) * time.Second)
		} else {
			job.State = "backoff"
			job.ConsecutiveFailures++
			next = now.Add(ticketBackoff(cfg, job.ConsecutiveFailures, result.status))
		}
		job.NextAttempt = &next
	}()
	return done, "started"
}

func (s *OpenAIGatewayService) cancelTicketJobs(accountID int64) {
	s.ticketJobsMu.Lock()
	defer s.ticketJobsMu.Unlock()
	for _, job := range s.ticketJobs {
		if accountID == 0 || job.accountID == accountID {
			if job.cancel != nil {
				job.cancel()
			}
			job.fingerprint = ""
			job.NextAttempt = nil
		}
	}
}

func (s *OpenAIGatewayService) scheduleCodexTickets(ctx context.Context, wait bool) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	ctx = s.stampTicketSnapshot(ctx)
	cfg := s.ticketConfigContext(ctx)
	if !cfg.Enabled {
		s.cancelTicketJobs(0)
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	allowed := make(map[string]string)
	for i := range accounts {
		account := &accounts[i]
		effective := resolveCodexTicketPolicy(account, cfg)
		if account.Status != StatusActive || !effective.Enabled {
			continue
		}
		for _, model := range effective.Models {
			allowed[openAICodexTicketKey(account.ID, model)] = ticketFingerprint(account, effective)
		}
	}
	release, err := guardTicketSnapshot(ctx)
	if err != nil {
		return
	}
	s.ticketJobsMu.Lock()
	for key, job := range s.ticketJobs {
		if fingerprint, ok := allowed[key]; !ok || fingerprint != job.fingerprint {
			if job.cancel != nil {
				job.cancel()
			} else if !ok {
				delete(s.ticketJobs, key)
			}
		}
	}
	s.ticketJobsMu.Unlock()
	release()
	var wg sync.WaitGroup
	for i := range accounts {
		account := &accounts[i]
		effective := resolveCodexTicketPolicy(account, cfg)
		if account.Status != StatusActive || !effective.Enabled {
			continue
		}
		for _, model := range effective.Models {
			done, _ := s.launchTicketJob(ctx, account, model, effective, false)
			if wait && done != nil {
				wg.Add(1)
				go func() { defer wg.Done(); <-done }()
			}
		}
	}
	wg.Wait()
}

type CodexTicketAccountView struct {
	Revision        string                    `json:"revision"`
	Policy          CodexTicketPolicy         `json:"policy"`
	GlobalEnabled   bool                      `json:"global_enabled"`
	Enabled         bool                      `json:"enabled"`
	Eligible        bool                      `json:"eligible"`
	Options         CodexTicketOptions        `json:"options"`
	ProxyConfigured bool                      `json:"proxy_configured"`
	Tickets         []OpenAICodexTicketStatus `json:"tickets"`
	Progress        []CodexTicketProgress     `json:"progress"`
}

func (s *OpenAIGatewayService) CodexTicketAccountView(ctx context.Context, account *Account) CodexTicketAccountView {
	global := s.ticketConfigContext(ctx)
	cfg := resolveCodexTicketPolicy(account, global)
	proxy, proxyErr := s.ticketProxy(ctx, account, cfg)
	// Status and injection must agree even when a ticket is only in memory after a DB failure.
	snapshot := *account
	snapshot.Extra = maps.Clone(account.Extra)
	if snapshot.Extra == nil {
		snapshot.Extra = map[string]any{}
	}
	for _, model := range cfg.Models {
		if ticket := s.lookupCodexTicketWithLength(account, model, cfg.TargetLength); ticket != nil {
			snapshot.Extra[openAICodexTicketExtraKey(model)] = ticket
		}
	}
	view := CodexTicketAccountView{Revision: ticketFingerprint(account, global), Policy: ticketPolicy(account), GlobalEnabled: global.Enabled, Enabled: cfg.Enabled, Eligible: isOpenAICodexTicketAccount(account), Options: CodexTicketOptionsFromConfig(cfg), ProxyConfigured: proxyErr == nil && proxy != "", Tickets: OpenAICodexTicketStatuses(&snapshot, global, time.Now()), Progress: []CodexTicketProgress{}}
	// 管理面板始终接收数组；关闭策略时也必须保持可编辑。
	if view.Tickets == nil {
		view.Tickets = []OpenAICodexTicketStatus{}
	}
	for _, model := range cfg.Models {
		p := CodexTicketProgress{Model: model, State: "waiting"}
		s.ticketJobsMu.Lock()
		if job := s.ticketJobs[openAICodexTicketKey(account.ID, model)]; job != nil {
			p = job.CodexTicketProgress
		}
		s.ticketJobsMu.Unlock()
		ticket := s.lookupCodexTicketWithLength(account, model, cfg.TargetLength)
		if p.LastSuccess == nil && ticket != nil {
			t := ticket.CapturedAt
			p.LastSuccess = &t
		}
		switch {
		case !view.Eligible:
			p.State = "unsupported"
		case !cfg.Enabled:
			p.State = "disabled"
		case account.Status != StatusActive:
			p.State = "inactive"
		case !view.ProxyConfigured:
			p.State = "configuration_missing"
			if proxyErr != nil {
				p.Reason = "proxy_unavailable"
			} else {
				p.Reason = "proxy_missing"
			}
		case p.State == "waiting" && ticket.valid(time.Now(), cfg.TargetLength):
			p.State = "ready"
		case p.State == "ready" && !ticket.valid(time.Now(), cfg.TargetLength):
			p.State = "waiting"
		}
		if p.State == "ready" && p.NextAttempt == nil && ticket != nil {
			next := ticket.ExpiresAt.Add(-time.Duration(cfg.RefreshBeforeSeconds) * time.Second)
			p.NextAttempt = &next
		}
		view.Progress = append(view.Progress, p)
	}
	return view
}

var ErrCodexTicketPolicyConflict = errors.New("ticket configuration changed; reload before saving")

func (s *OpenAIGatewayService) SaveCodexTicketPolicy(ctx context.Context, accountID int64, policy CodexTicketPolicy) error {
	return s.SaveCodexTicketPolicyIfCurrent(ctx, accountID, policy, "")
}
func (s *OpenAIGatewayService) SaveCodexTicketPolicyIfCurrent(ctx context.Context, accountID int64, policy CodexTicketPolicy, expectedRevision string) error {
	finish, err := s.ticketCoordinator().beginMutation(ctx, accountID)
	if err != nil {
		return err
	}
	defer finish()
	b, _ := json.Marshal(policy)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := ValidateCodexTicketPolicyExtra(map[string]any{CodexTicketPolicyExtraKey: raw}); err != nil {
		return err
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if expectedRevision != "" && ticketFingerprint(account, s.ticketConfigContext(ctx)) != expectedRevision {
		return ErrCodexTicketPolicyConflict
	}
	if !isOpenAICodexTicketAccount(account) {
		return errors.New("ticket policy requires a non-shadow OpenAI OAuth account")
	}
	if policy.ProxyID != nil {
		if s.settingService == nil || s.settingService.proxyRepo == nil {
			return errors.New("proxy unavailable")
		}
		proxy, err := s.settingService.proxyRepo.GetByID(ctx, *policy.ProxyID)
		if err != nil || proxy == nil || proxy.Status != StatusActive {
			return errors.New("selected proxy is unavailable")
		}
	}
	// Participation has its own key so bulk toggles never replace custom options.
	delete(raw, "enabled")
	var enabled any
	if policy.Enabled != nil {
		enabled = *policy.Enabled
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{CodexTicketPolicyExtraKey: raw, CodexTicketEnabledExtraKey: enabled}); err != nil {
		return err
	}
	s.cancelTicketJobs(accountID)
	return nil
}

func (s *OpenAIGatewayService) TriggerCodexTicket(ctx context.Context, accountID int64, model string) (string, error) {
	ctx = s.stampTicketSnapshot(ctx)
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cfg := resolveCodexTicketPolicy(account, s.ticketConfigContext(ctx))
	if !ticketModelEnabled(cfg, model) || account.Status != StatusActive {
		return "", errors.New("ticket harvesting is disabled for this account or model")
	}
	proxy, err := s.ticketProxy(ctx, account, cfg)
	if err != nil || proxy == "" {
		return "", errors.New("harvest proxy is not configured or unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	workCtx := s.ticketContext
	s.openaiCodexTicketLifecycleMu.Unlock()
	if workCtx == nil {
		return "", errors.New("ticket harvester is not running")
	}
	workCtx = context.WithValue(workCtx, codexTicketStampKey{}, ctx.Value(codexTicketStampKey{}))
	_, state := s.launchTicketJob(workCtx, account, model, cfg, true)
	if state == "stale" {
		return "", errors.New("ticket policy changed; refresh before harvesting")
	}
	if state == "stopped" {
		return "", fmt.Errorf("ticket harvester is stopped")
	}
	return state, nil
}
