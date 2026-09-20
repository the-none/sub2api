package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const CodexTicketEnabledExtraKey = "codex_ticket_enabled"
const CodexTicketPolicyExtraKey = "codex_ticket_policy"
const codexTicketConfigSettingKey = "openai_codex_ticket_config"

// 通用账号编辑只能保留数据库中的最新策略；创建、专用端点和批量开关分别处理授权写入。
func PreserveCodexTicketPolicyExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for _, key := range []string{CodexTicketEnabledExtraKey, CodexTicketPolicyExtraKey} {
		delete(result, key)
		if value, ok := current[key]; ok {
			if result == nil {
				result = map[string]any{}
			}
			result[key] = value
		}
	}
	return result
}

func CodexTicketProxyID(account *Account) *int64 { return ticketPolicy(account).ProxyID }

func WithoutCodexTicketProxy(extra map[string]any) map[string]any {
	result := maps.Clone(extra)
	if raw, ok := result[CodexTicketPolicyExtraKey]; ok && raw != nil {
		b, err := json.Marshal(raw)
		var policy map[string]any
		if err == nil && json.Unmarshal(b, &policy) == nil {
			delete(policy, "proxy_id")
			result[CodexTicketPolicyExtraKey] = policy
		}
	}
	return result
}

func validateCodexTicketProxy(ctx context.Context, repo ProxyRepository, account *Account) error {
	id := CodexTicketProxyID(account)
	if id == nil {
		return nil
	}
	if !isOpenAICodexTicketAccount(account) || repo == nil {
		return errors.New("ticket proxy requires a non-shadow OpenAI OAuth account and an available proxy")
	}
	proxy, err := repo.GetByID(ctx, *id)
	if err != nil || proxy == nil || proxy.Status != StatusActive {
		return errors.New("selected ticket proxy is unavailable")
	}
	return nil
}

// CodexTicketOptions can be inherited as a group, independently of participation.
type CodexTicketOptions struct {
	Models               []string `json:"models"`
	Instructions         string   `json:"instructions"`
	UserPrompt           string   `json:"user_prompt"`
	RetrySeconds         int      `json:"retry_seconds"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	TTLSeconds           int      `json:"ttl_seconds"`
	RefreshBeforeSeconds int      `json:"refresh_before_seconds"`
	MaxBackoffSeconds    int      `json:"max_backoff_seconds"`
	FailClosed           bool     `json:"fail_closed"`
}

type CodexTicketPolicy struct {
	Enabled *bool               `json:"enabled"`
	Options *CodexTicketOptions `json:"options,omitempty"`
	// nil inherits the harvest proxy; a positive ID selects a managed proxy.
	ProxyID *int64 `json:"proxy_id,omitempty"`
}

func normalizeCodexTicketConfig(cfg config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 4
	}
	if cfg.MaxBackoffSeconds <= 0 {
		cfg.MaxBackoffSeconds = 300
	}
	if cfg.UserPrompt == "" {
		cfg.UserPrompt = "ping"
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	cfg.Models = slices.Clone(cfg.Models)
	return cfg
}

func CodexTicketOptionsFromConfig(cfg config.OpenAICodexTicketConfig) CodexTicketOptions {
	return CodexTicketOptions{Models: cfg.Models, Instructions: cfg.Instructions, UserPrompt: cfg.UserPrompt,
		RetrySeconds: cfg.HarvestProbeIntervalSeconds, TimeoutSeconds: cfg.HarvestAttemptTimeoutSeconds,
		TTLSeconds: cfg.TTLSeconds, RefreshBeforeSeconds: cfg.RefreshBeforeSeconds, MaxBackoffSeconds: cfg.MaxBackoffSeconds, FailClosed: cfg.FailClosed}
}

func (o CodexTicketOptions) apply(cfg config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	cfg.Models = slices.Clone(o.Models)
	cfg.Instructions, cfg.UserPrompt = o.Instructions, o.UserPrompt
	cfg.HarvestProbeIntervalSeconds, cfg.HarvestAttemptTimeoutSeconds = o.RetrySeconds, o.TimeoutSeconds
	cfg.TTLSeconds, cfg.RefreshBeforeSeconds, cfg.MaxBackoffSeconds = o.TTLSeconds, o.RefreshBeforeSeconds, o.MaxBackoffSeconds
	cfg.FailClosed = o.FailClosed
	return cfg
}

func (o CodexTicketOptions) Validate() error {
	return o.apply(config.OpenAICodexTicketConfig{}).ValidateOptions()
}

func ticketPolicy(account *Account) CodexTicketPolicy {
	var p CodexTicketPolicy
	if account != nil {
		b, err := json.Marshal(account.Extra[CodexTicketPolicyExtraKey])
		if err == nil {
			_ = json.Unmarshal(b, &p)
		}
	}
	if account != nil {
		if value, exists := account.Extra[CodexTicketEnabledExtraKey]; exists {
			p.Enabled = nil
			if enabled, ok := value.(bool); ok {
				p.Enabled = &enabled
			}
		}
	}
	return p
}

func ValidateCodexTicketPolicyExtra(extra map[string]any) error {
	if enabled, ok := extra[CodexTicketEnabledExtraKey]; ok && enabled != nil {
		if _, ok := enabled.(bool); !ok {
			return errors.New("codex_ticket_enabled must be a boolean or null")
		}
	}
	raw, ok := extra[CodexTicketPolicyExtraKey]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return errors.New("invalid codex ticket policy")
	}
	var p CodexTicketPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		return errors.New("invalid codex ticket policy")
	}
	if p.ProxyID != nil && *p.ProxyID <= 0 {
		return errors.New("ticket proxy_id must be positive or null to inherit")
	}
	if p.Options != nil {
		return p.Options.Validate()
	}
	return nil
}

func resolveCodexTicketPolicy(account *Account, cfg config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	p := ticketPolicy(account)
	if p.Options != nil && p.Options.Validate() == nil {
		cfg = p.Options.apply(cfg)
	}
	participating := cfg.DefaultAccountEnabled == nil || *cfg.DefaultAccountEnabled
	if p.Enabled != nil {
		participating = *p.Enabled
	}
	cfg.Enabled = cfg.Enabled && participating && isOpenAICodexTicketAccount(account)
	return cfg
}

type cachedCodexTicketConfig struct {
	cfg     config.OpenAICodexTicketConfig
	expires time.Time
}

// Read only the non-secret options here. Existing enabled/proxy settings remain
// authoritative so old admin clients and environment fallbacks keep working.
func (s *SettingService) codexTicketBaseConfig(ctx context.Context, fallback config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	fallback = normalizeCodexTicketConfig(fallback)
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	s.codexTicketConfigMu.Lock()
	cached := s.codexTicketConfigCache
	generation := s.codexTicketConfigGeneration
	s.codexTicketConfigMu.Unlock()
	if ctx.Err() != nil {
		if cached != nil {
			return cached.cfg
		}
		return fallback
	}
	if cached != nil && time.Now().Before(cached.expires) {
		return cached.cfg
	}
	// Coalesce misses without holding a mutex during storage I/O. Callers can
	// cancel independently; failed refreshes retain the last value for one second.
	result := s.codexTicketConfigSF.DoChan(fmt.Sprint(generation), func() (any, error) {
		s.codexTicketConfigMu.Lock()
		previous := s.codexTicketConfigCache
		s.codexTicketConfigMu.Unlock()
		if previous != nil && time.Now().Before(previous.expires) {
			return previous.cfg, nil
		}
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, err := s.settingRepo.GetValue(readCtx, codexTicketConfigSettingKey)
		cfg, ttl := fallback, 5*time.Second
		if err == nil && raw != "" {
			candidate := cfg
			if json.Unmarshal([]byte(raw), &candidate) == nil && candidate.Validate() == nil {
				cfg = candidate
			} else {
				err = errors.New("invalid ticket settings")
			}
		}
		if err == nil || errors.Is(err, ErrSettingNotFound) {
			err = nil
			for _, key := range []string{SettingKeyOpenAICodexTicketEnabled, SettingKeyOpenAICodexTicketHarvestProxyURL} {
				value, readErr := s.settingRepo.GetValue(readCtx, key)
				if readErr != nil && !errors.Is(readErr, ErrSettingNotFound) {
					err = readErr
					break
				}
				if key == SettingKeyOpenAICodexTicketEnabled && value != "" {
					cfg.Enabled = value == "true"
				}
				if key == SettingKeyOpenAICodexTicketHarvestProxyURL {
					cfg.HarvestProxyURL = fallback.HarvestProxyURL
					if strings.TrimSpace(value) != "" {
						cfg.HarvestProxyURL = strings.TrimSpace(value)
					}
				}
			}
		}
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			ttl = time.Second
			if previous != nil {
				cfg = previous.cfg
			}
		}
		s.codexTicketConfigMu.Lock()
		if s.codexTicketConfigGeneration == generation {
			s.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: cfg, expires: time.Now().Add(ttl)}
		}
		s.codexTicketConfigMu.Unlock()
		return cfg, nil
	})
	select {
	case <-ctx.Done():
		if cached != nil {
			return cached.cfg
		}
		return fallback
	case value := <-result:
		if cfg, ok := value.Val.(config.OpenAICodexTicketConfig); ok {
			return cfg
		}
		return fallback
	}
}

func (s *SettingService) GetCodexTicketConfig(ctx context.Context, fallback config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	return s.codexTicketBaseConfig(ctx, fallback)
}

func (s *SettingService) invalidateCodexTicketConfig() {
	s.codexTicketConfigMu.Lock()
	s.codexTicketConfigGeneration++
	if cached := s.codexTicketConfigCache; cached != nil {
		s.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: cached.cfg}
	}
	s.codexTicketConfigMu.Unlock()
}

// Seed acknowledged local writes, including the master switch, before allowing
// new task snapshots. Storage outages must not revive an acknowledged shutdown.
func (s *SettingService) seedCodexTicketConfig(updates map[string]string) {
	s.codexTicketConfigMu.Lock()
	defer s.codexTicketConfigMu.Unlock()
	_, complete := updates[codexTicketConfigSettingKey]
	known := complete || s.codexTicketConfigCache != nil
	cfg := config.OpenAICodexTicketConfig{}
	if s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	fallbackProxy := cfg.HarvestProxyURL
	cfg = normalizeCodexTicketConfig(cfg)
	if previous := s.codexTicketConfigCache; previous != nil {
		cfg = previous.cfg
	}
	oldProxy := cfg.HarvestProxyURL
	if raw, ok := updates[codexTicketConfigSettingKey]; ok {
		_ = json.Unmarshal([]byte(raw), &cfg)
		cfg.HarvestProxyURL = oldProxy
	}
	if enabled, ok := updates[SettingKeyOpenAICodexTicketEnabled]; ok {
		cfg.Enabled = enabled == "true"
	}
	if proxy, ok := updates[SettingKeyOpenAICodexTicketHarvestProxyURL]; ok {
		cfg.HarvestProxyURL = proxy
		if proxy == "" {
			cfg.HarvestProxyURL = fallbackProxy
		}
	}
	s.codexTicketConfigGeneration++
	expires := time.Time{}
	if known {
		expires = time.Now().Add(5 * time.Second)
	}
	s.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: cfg, expires: expires}
}

func (s *SettingService) SaveCodexTicketConfig(ctx context.Context, cfg config.OpenAICodexTicketConfig, clearProxy bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := ValidateOpenAICodexTicketHarvestProxyURL(cfg.HarvestProxyURL); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return errors.New("ticket settings unavailable")
	}
	updates := map[string]string{SettingKeyOpenAICodexTicketEnabled: fmt.Sprint(cfg.Enabled)}
	if clearProxy {
		updates[SettingKeyOpenAICodexTicketHarvestProxyURL] = ""
	} else if !IsMaskedProxyURL(cfg.HarvestProxyURL) {
		updates[SettingKeyOpenAICodexTicketHarvestProxyURL] = strings.TrimSpace(cfg.HarvestProxyURL)
	}
	preserveProxy := !clearProxy && IsMaskedProxyURL(cfg.HarvestProxyURL)
	cfg.HarvestProxyURL = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	updates[codexTicketConfigSettingKey] = string(data)
	finish, err := s.beginTicketMutation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	// A masked/omitted proxy preserves the actual database value, even when
	// this instance has not served the preceding GET and its cache is cold.
	if preserveProxy {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		current, readErr := s.settingRepo.GetValue(readCtx, SettingKeyOpenAICodexTicketHarvestProxyURL)
		cancel()
		if readErr != nil && !errors.Is(readErr, ErrSettingNotFound) {
			return readErr
		}
		updates[SettingKeyOpenAICodexTicketHarvestProxyURL] = current
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	s.InvalidateOpenAICodexTicketEnabledCache()
	s.InvalidateOpenAICodexTicketHarvestProxyCache()
	s.seedCodexTicketConfig(updates)
	return nil
}

func (s *OpenAIGatewayService) ticketConfigContext(ctx context.Context) config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if s != nil && s.settingService != nil {
		return s.settingService.GetCodexTicketConfig(ctx, cfg)
	}
	return normalizeCodexTicketConfig(cfg)
}

func ticketModelEnabled(cfg config.OpenAICodexTicketConfig, model string) bool {
	return cfg.Enabled && slices.Contains(cfg.Models, strings.TrimSpace(model))
}
