package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const CodexTicketEnabledExtraKey = "codex_ticket_enabled"
const CodexTicketPolicyExtraKey = "codex_ticket_policy"
const codexTicketConfigSettingKey = "openai_codex_ticket_config"

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
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
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
	if cfg.Instructions == "" {
		cfg.Instructions = "Reply with exactly: pong"
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
	if len(o.Models) == 0 || len(o.Models) > 20 {
		return errors.New("models must contain 1–20 model names")
	}
	seen := map[string]bool{}
	for _, model := range o.Models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 128 || strings.ContainsAny(model, "\r\n\x00") || seen[model] {
			return errors.New("model names must be non-empty, unique, trimmed and at most 128 bytes")
		}
		seen[model] = true
	}
	if len(o.Instructions) > 8000 || strings.TrimSpace(o.UserPrompt) == "" || len(o.UserPrompt) > 8000 {
		return errors.New("user_prompt is required; prompts must not exceed 8000 bytes")
	}
	if o.RetrySeconds < 1 || o.RetrySeconds > 3600 {
		return errors.New("retry_seconds must be between 1 and 3600")
	}
	if o.TimeoutSeconds < 1 || o.TimeoutSeconds > 120 {
		return errors.New("timeout_seconds must be between 1 and 120")
	}
	if o.TTLSeconds < 60 || o.TTLSeconds > 86400 {
		return errors.New("ttl_seconds must be between 60 and 86400")
	}
	if o.RefreshBeforeSeconds < 0 || o.RefreshBeforeSeconds >= o.TTLSeconds {
		return errors.New("refresh_before_seconds must be non-negative and smaller than ttl_seconds")
	}
	if o.MaxBackoffSeconds < o.RetrySeconds || o.MaxBackoffSeconds > 86400 {
		return errors.New("max_backoff_seconds must be between retry_seconds and 86400")
	}
	return nil
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
	if s == nil || s.settingRepo == nil || ctx.Err() != nil {
		return fallback
	}
	s.codexTicketConfigMu.Lock()
	defer s.codexTicketConfigMu.Unlock()
	if c := s.codexTicketConfigCache; c != nil && time.Now().Before(c.expires) {
		return c.cfg
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(readCtx, codexTicketConfigSettingKey)
	cfg := fallback
	if err == nil && raw != "" {
		candidate := cfg
		if json.Unmarshal([]byte(raw), &candidate) == nil && CodexTicketOptionsFromConfig(candidate).Validate() == nil {
			cfg = candidate
		}
	} else if err != nil && !errors.Is(err, ErrSettingNotFound) && s.codexTicketConfigCache != nil {
		return s.codexTicketConfigCache.cfg
	}
	s.codexTicketConfigCache = &cachedCodexTicketConfig{cfg: cfg, expires: time.Now().Add(5 * time.Second)}
	return cfg
}

func (s *SettingService) GetCodexTicketConfig(ctx context.Context, fallback config.OpenAICodexTicketConfig) config.OpenAICodexTicketConfig {
	cfg := s.codexTicketBaseConfig(ctx, fallback)
	cfg.Enabled = s.GetOpenAICodexTicketEnabled(ctx, fallback.Enabled)
	if proxy := s.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
		cfg.HarvestProxyURL = proxy
	} else {
		cfg.HarvestProxyURL = fallback.HarvestProxyURL
	}
	return cfg
}

func (s *SettingService) SaveCodexTicketConfig(ctx context.Context, cfg config.OpenAICodexTicketConfig, clearProxy bool) error {
	if err := (CodexTicketOptions{Models: cfg.Models, Instructions: cfg.Instructions, UserPrompt: cfg.UserPrompt, RetrySeconds: cfg.HarvestProbeIntervalSeconds, TimeoutSeconds: cfg.HarvestAttemptTimeoutSeconds, TTLSeconds: cfg.TTLSeconds, RefreshBeforeSeconds: cfg.RefreshBeforeSeconds, MaxBackoffSeconds: cfg.MaxBackoffSeconds, FailClosed: cfg.FailClosed}).Validate(); err != nil {
		return err
	}
	if cfg.TargetLength < 16 || cfg.TargetLength > 8192 || cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > 32 {
		return errors.New("target_length must be 16–8192 and max_concurrency must be 1–32")
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
	cfg.HarvestProxyURL = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	updates[codexTicketConfigSettingKey] = string(data)
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	s.codexTicketConfigMu.Lock()
	s.codexTicketConfigCache = nil
	s.codexTicketConfigMu.Unlock()
	s.InvalidateOpenAICodexTicketEnabledCache()
	s.InvalidateOpenAICodexTicketHarvestProxyCache()
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
