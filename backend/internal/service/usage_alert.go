package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	UsageAlertPlatformAll       = "all"
	UsageAlertPlatformOpenAI    = PlatformOpenAI
	UsageAlertPlatformAnthropic = PlatformAnthropic

	UsageAlertWindow5h         = "5h"
	UsageAlertWindow7d         = "7d"
	UsageAlertTypeOverall      = "overall"
	UsageAlertTypeSpark        = "spark"
	UsageAlertTypeFable        = "fable"
	UsageAlertRelationPrimary  = "primary"
	UsageAlertRelationShadow   = "shadow"
	UsageAlertRelationSubLimit = "sub_limit"
	UsageAlertMetricUsed       = "used_percent"
	UsageAlertMetricRemaining  = "remaining_percent"
	UsageAlertOperatorGTE      = ">="
	UsageAlertOperatorLTE      = "<="

	UsageAlertWebhookTypeJSONPost = "json_post"
	UsageAlertWebhookTypeTelegram = "telegram"

	UsageAlertEventTriggered = "account.usage_alert"
	UsageAlertEventResolved  = "account.usage_alert.resolved"

	UsageAlertStatusNormal    = "normal"
	UsageAlertStatusTriggered = "triggered"

	usageAlertRuleNameMaxLength = 100

	UsageAlertSourceOpenAICodexHeaders = "openai_codex_headers"
	UsageAlertSourceOpenAICodexProbe   = "openai_codex_probe"
	UsageAlertSourceClaudeHeaders      = "claude_headers"
	UsageAlertSourceClaudeUsageAPI     = "claude_usage_api"
)

var (
	ErrUsageAlertInvalidPlatform  = errors.New("usage alert platform must be openai, anthropic, or all")
	ErrUsageAlertInvalidWindow    = errors.New("usage alert window must be 5h or 7d")
	ErrUsageAlertInvalidMetric    = errors.New("usage alert metric must be used_percent or remaining_percent")
	ErrUsageAlertInvalidOperator  = errors.New("usage alert operator must be >= or <=")
	ErrUsageAlertInvalidUsageType = errors.New("usage alert usage type is invalid")
	ErrUsageAlertInvalidWebhook   = errors.New("usage alert webhook type must be json_post or telegram")
)

type RealAccount struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	Identifier *string    `json:"identifier,omitempty"`
	Notes      *string    `json:"notes,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Accounts   []*Account `json:"accounts,omitempty"`
}

type UsageAlertRule struct {
	ID                 int64        `json:"id"`
	Name               string       `json:"name"`
	Platform           string       `json:"platform"`
	RealAccountID      *int64       `json:"real_account_id,omitempty"`
	UsageType          string       `json:"usage_type"`
	Window             string       `json:"window"`
	Metric             string       `json:"metric"`
	Operator           string       `json:"operator"`
	Threshold          float64      `json:"threshold"`
	MinResetAfterHours *float64     `json:"min_reset_after_hours,omitempty"`
	StepPercent        *float64     `json:"step_percent,omitempty"`
	CooldownMinutes    int          `json:"cooldown_minutes"`
	Enabled            bool         `json:"enabled"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	RealAccount        *RealAccount `json:"real_account,omitempty"`
}

type UsageAlertWebhook struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	URL        string         `json:"url,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Enabled    bool           `json:"enabled"`
	RetryCount int            `json:"retry_count"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type UsageAlertTelegramConfig struct {
	BotToken            string `json:"bot_token"`
	ChatID              string `json:"chat_id"`
	MessageThreadID     *int64 `json:"message_thread_id,omitempty"`
	Language            string `json:"language,omitempty"`
	Timezone            string `json:"timezone,omitempty"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
}

type UsageAlertBinding struct {
	ID            int64              `json:"id"`
	RealAccountID int64              `json:"real_account_id"`
	WebhookID     int64              `json:"webhook_id"`
	Enabled       bool               `json:"enabled"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	RealAccount   *RealAccount       `json:"real_account,omitempty"`
	Webhook       *UsageAlertWebhook `json:"webhook,omitempty"`
}

type UsageAlertState struct {
	ID              int64      `json:"id"`
	RealAccountID   int64      `json:"real_account_id"`
	RuleID          int64      `json:"rule_id"`
	UsageType       string     `json:"usage_type"`
	Window          string     `json:"window"`
	LastStatus      string     `json:"last_status"`
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	LastValue       *float64   `json:"last_value,omitempty"`
	LastResetAt     *time.Time `json:"last_reset_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type UsageAlertWindowSnapshot struct {
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

type UsageAlertSnapshot struct {
	AccountID       int64                               `json:"account_id"`
	RealAccountID   int64                               `json:"real_account_id"`
	UsageType       string                              `json:"usage_type"`
	UsageRelation   string                              `json:"usage_relation"`
	ParentUsageType string                              `json:"parent_usage_type,omitempty"`
	Platform        string                              `json:"platform"`
	Source          string                              `json:"source"`
	Windows         map[string]UsageAlertWindowSnapshot `json:"windows"`
	SampledAt       time.Time                           `json:"sampled_at"`
}

type UsageAlertTrigger struct {
	Rule        *UsageAlertRule
	Window      string
	Value       float64
	WindowState UsageAlertWindowSnapshot
	TriggeredAt time.Time
	Resolved    bool
}

type UsageAlertWebhookEvent struct {
	Event            string     `json:"event"`
	EventID          string     `json:"event_id"`
	TriggeredAt      time.Time  `json:"triggered_at"`
	AccountID        int64      `json:"account_id"`
	RealAccountID    int64      `json:"real_account_id"`
	RealAccountName  string     `json:"real_account_name,omitempty"`
	UsageType        string     `json:"usage_type"`
	UsageRelation    string     `json:"usage_relation"`
	ParentUsageType  string     `json:"parent_usage_type,omitempty"`
	QuotaDimension   string     `json:"quota_dimension,omitempty"`
	Platform         string     `json:"platform"`
	Source           string     `json:"source"`
	RuleID           int64      `json:"rule_id"`
	RuleName         string     `json:"rule_name"`
	Window           string     `json:"window"`
	Metric           string     `json:"metric"`
	Operator         string     `json:"operator"`
	Threshold        float64    `json:"threshold"`
	StepPercent      *float64   `json:"step_percent,omitempty"`
	Value            float64    `json:"value"`
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

type UsageAlertRepository interface {
	ListRealAccounts(ctx context.Context) ([]*RealAccount, error)
	GetRealAccount(ctx context.Context, id int64) (*RealAccount, error)
	CreateRealAccount(ctx context.Context, account *RealAccount) (*RealAccount, error)
	UpdateRealAccount(ctx context.Context, account *RealAccount) (*RealAccount, error)
	DeleteRealAccount(ctx context.Context, id int64) error
	AttachAccount(ctx context.Context, realAccountID, accountID int64) error
	DetachAccount(ctx context.Context, accountID int64) error

	ListRules(ctx context.Context) ([]*UsageAlertRule, error)
	ListEnabledRules(ctx context.Context, realAccountID int64, usageType string) ([]*UsageAlertRule, error)
	GetRule(ctx context.Context, id int64) (*UsageAlertRule, error)
	CreateRule(ctx context.Context, rule *UsageAlertRule) (*UsageAlertRule, error)
	UpdateRule(ctx context.Context, rule *UsageAlertRule) (*UsageAlertRule, error)
	DeleteRule(ctx context.Context, id int64) error

	ListWebhooks(ctx context.Context) ([]*UsageAlertWebhook, error)
	GetWebhook(ctx context.Context, id int64) (*UsageAlertWebhook, error)
	CreateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error)
	UpdateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error)
	DeleteWebhook(ctx context.Context, id int64) error

	ListBindings(ctx context.Context) ([]*UsageAlertBinding, error)
	ListEnabledWebhooksForRealAccount(ctx context.Context, realAccountID int64) ([]*UsageAlertWebhook, error)
	CreateBinding(ctx context.Context, binding *UsageAlertBinding) (*UsageAlertBinding, error)
	UpdateBinding(ctx context.Context, binding *UsageAlertBinding) (*UsageAlertBinding, error)
	DeleteBinding(ctx context.Context, id int64) error
	EnsureRealAccountForAccount(ctx context.Context, account *Account) (*RealAccount, error)

	GetSnapshot(ctx context.Context, realAccountID int64, usageType string) (*UsageAlertSnapshot, error)
	UpsertSnapshot(ctx context.Context, snapshot *UsageAlertSnapshot) error
	GetState(ctx context.Context, realAccountID, ruleID int64, usageType, window string) (*UsageAlertState, error)
	UpsertState(ctx context.Context, state *UsageAlertState) error
}

type UsageAlertService struct {
	repo        UsageAlertRepository
	accountRepo AccountRepository
	httpClient  *http.Client
}

func NewUsageAlertService(repo UsageAlertRepository, accountRepo AccountRepository) *UsageAlertService {
	return &UsageAlertService{
		repo:        repo,
		accountRepo: accountRepo,
		httpClient:  &http.Client{Timeout: 8 * time.Second},
	}
}

func (s *UsageAlertService) ListRealAccounts(ctx context.Context) ([]*RealAccount, error) {
	return s.repo.ListRealAccounts(ctx)
}

func (s *UsageAlertService) GetRealAccount(ctx context.Context, id int64) (*RealAccount, error) {
	return s.repo.GetRealAccount(ctx, id)
}

func (s *UsageAlertService) CreateRealAccount(ctx context.Context, account *RealAccount) (*RealAccount, error) {
	if err := validateRealAccount(account); err != nil {
		return nil, err
	}
	return s.repo.CreateRealAccount(ctx, account)
}

func (s *UsageAlertService) UpdateRealAccount(ctx context.Context, account *RealAccount) (*RealAccount, error) {
	if err := validateRealAccount(account); err != nil {
		return nil, err
	}
	return s.repo.UpdateRealAccount(ctx, account)
}

func (s *UsageAlertService) DeleteRealAccount(ctx context.Context, id int64) error {
	return s.repo.DeleteRealAccount(ctx, id)
}

func (s *UsageAlertService) AttachAccount(ctx context.Context, realAccountID, accountID int64) error {
	if realAccountID <= 0 || accountID <= 0 {
		return fmt.Errorf("real_account_id and account_id must be positive")
	}
	return s.repo.AttachAccount(ctx, realAccountID, accountID)
}

func (s *UsageAlertService) DetachAccount(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return fmt.Errorf("account_id must be positive")
	}
	return s.repo.DetachAccount(ctx, accountID)
}

func (s *UsageAlertService) ListRules(ctx context.Context) ([]*UsageAlertRule, error) {
	return s.repo.ListRules(ctx)
}

func (s *UsageAlertService) CreateRule(ctx context.Context, rule *UsageAlertRule) (*UsageAlertRule, error) {
	if err := validateUsageAlertRule(rule); err != nil {
		return nil, err
	}
	if err := s.populateUsageAlertRuleScope(ctx, rule); err != nil {
		return nil, err
	}
	ensureUsageAlertRuleName(rule)
	return s.repo.CreateRule(ctx, rule)
}

func (s *UsageAlertService) UpdateRule(ctx context.Context, rule *UsageAlertRule) (*UsageAlertRule, error) {
	if err := validateUsageAlertRule(rule); err != nil {
		return nil, err
	}
	if err := s.populateUsageAlertRuleScope(ctx, rule); err != nil {
		return nil, err
	}
	ensureUsageAlertRuleName(rule)
	return s.repo.UpdateRule(ctx, rule)
}

func (s *UsageAlertService) DeleteRule(ctx context.Context, id int64) error {
	return s.repo.DeleteRule(ctx, id)
}

func (s *UsageAlertService) ListWebhooks(ctx context.Context) ([]*UsageAlertWebhook, error) {
	return s.repo.ListWebhooks(ctx)
}

func (s *UsageAlertService) CreateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error) {
	if err := validateUsageAlertWebhook(webhook); err != nil {
		return nil, err
	}
	return s.repo.CreateWebhook(ctx, webhook)
}

func (s *UsageAlertService) UpdateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error) {
	if err := validateUsageAlertWebhook(webhook); err != nil {
		return nil, err
	}
	return s.repo.UpdateWebhook(ctx, webhook)
}

func (s *UsageAlertService) DeleteWebhook(ctx context.Context, id int64) error {
	return s.repo.DeleteWebhook(ctx, id)
}

func (s *UsageAlertService) ListBindings(ctx context.Context) ([]*UsageAlertBinding, error) {
	return s.repo.ListBindings(ctx)
}

func (s *UsageAlertService) GetSnapshot(ctx context.Context, realAccountID int64, usageType string) (*UsageAlertSnapshot, error) {
	if realAccountID <= 0 {
		return nil, fmt.Errorf("real_account_id must be positive")
	}
	usageType = normalizeUsageAlertUsageType(usageType)
	if !isValidUsageAlertUsageType(usageType) {
		return nil, ErrUsageAlertInvalidUsageType
	}
	snapshot, err := s.repo.GetSnapshot(ctx, realAccountID, usageType)
	if err != nil || snapshot == nil {
		return snapshot, err
	}
	snapshot.UsageType = normalizeUsageAlertUsageType(snapshot.UsageType)
	snapshot.UsageRelation, snapshot.ParentUsageType = usageAlertTypeRelationship(snapshot.UsageType)
	return snapshot, nil
}

func (s *UsageAlertService) CreateBinding(ctx context.Context, binding *UsageAlertBinding) (*UsageAlertBinding, error) {
	if binding == nil || binding.RealAccountID <= 0 || binding.WebhookID <= 0 {
		return nil, fmt.Errorf("real_account_id and webhook_id must be positive")
	}
	return s.repo.CreateBinding(ctx, binding)
}

func (s *UsageAlertService) UpdateBinding(ctx context.Context, binding *UsageAlertBinding) (*UsageAlertBinding, error) {
	if binding == nil || binding.ID <= 0 || binding.RealAccountID <= 0 || binding.WebhookID <= 0 {
		return nil, fmt.Errorf("id, real_account_id and webhook_id must be positive")
	}
	return s.repo.UpdateBinding(ctx, binding)
}

func (s *UsageAlertService) DeleteBinding(ctx context.Context, id int64) error {
	return s.repo.DeleteBinding(ctx, id)
}

func (s *UsageAlertService) TestWebhook(ctx context.Context, webhook *UsageAlertWebhook) error {
	if s == nil {
		return fmt.Errorf("usage alert service is not configured")
	}
	if webhook == nil {
		return fmt.Errorf("usage alert webhook is required")
	}
	if strings.TrimSpace(webhook.Name) == "" {
		webhook.Name = "Manual test"
	}
	webhook.Enabled = true
	if err := validateUsageAlertWebhook(webhook); err != nil {
		return err
	}
	now := time.Now().UTC()
	resetAt := now.Add(6 * time.Hour)
	event := UsageAlertWebhookEvent{
		Event:            "account.usage_alert.test",
		EventID:          fmt.Sprintf("test-%d", now.UnixNano()),
		TriggeredAt:      now,
		AccountID:        0,
		RealAccountID:    0,
		RealAccountName:  "Test account",
		UsageType:        UsageAlertTypeOverall,
		UsageRelation:    UsageAlertRelationPrimary,
		QuotaDimension:   QuotaDimensionGlobal,
		Platform:         UsageAlertPlatformOpenAI,
		Source:           "manual_test",
		RuleID:           0,
		RuleName:         "Manual test notification",
		Window:           UsageAlertWindow7d,
		Metric:           UsageAlertMetricRemaining,
		Operator:         UsageAlertOperatorLTE,
		Threshold:        20,
		Value:            18.5,
		UsedPercent:      81.5,
		RemainingPercent: 18.5,
		ResetAt:          &resetAt,
	}
	return s.deliverWebhookWithRetry(ctx, *webhook, event)
}

func (s *UsageAlertService) Observe(ctx context.Context, snapshot *UsageAlertSnapshot) {
	if s == nil || s.repo == nil || snapshot == nil || snapshot.AccountID <= 0 || len(snapshot.Windows) == 0 {
		return
	}
	if snapshot.SampledAt.IsZero() {
		snapshot.SampledAt = time.Now().UTC()
	}
	if strings.TrimSpace(snapshot.Platform) == "" {
		return
	}
	if snapshot.RealAccountID <= 0 || strings.TrimSpace(snapshot.UsageType) == "" {
		realAccountID, accountUsageType := s.resolveUsageAlertScope(ctx, snapshot.AccountID)
		if snapshot.RealAccountID <= 0 {
			snapshot.RealAccountID = realAccountID
		}
		if strings.TrimSpace(snapshot.UsageType) == "" {
			snapshot.UsageType = accountUsageType
		}
	}
	snapshot.UsageType = normalizeUsageAlertUsageType(snapshot.UsageType)
	if snapshot.RealAccountID <= 0 || !isValidUsageAlertUsageType(snapshot.UsageType) {
		return
	}
	snapshot.UsageRelation, snapshot.ParentUsageType = usageAlertTypeRelationship(snapshot.UsageType)

	go s.observeAsync(*snapshot)
}

func (s *UsageAlertService) resolveUsageAlertScope(ctx context.Context, accountID int64) (int64, string) {
	if s.accountRepo == nil || accountID <= 0 {
		return 0, UsageAlertTypeOverall
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return 0, UsageAlertTypeOverall
	}
	usageType := UsageAlertTypeOverall
	if account.QuotaDimensionOrDefault() == QuotaDimensionSpark {
		usageType = UsageAlertTypeSpark
	}
	if account.RealAccountID != nil && *account.RealAccountID > 0 {
		return *account.RealAccountID, usageType
	}
	if account.IsShadow() && account.ParentAccountID != nil && *account.ParentAccountID > 0 {
		parent, parentErr := s.accountRepo.GetByID(ctx, *account.ParentAccountID)
		if parentErr != nil || parent == nil {
			slog.Warn("usage_alert_get_shadow_parent_failed", "account_id", accountID, "parent_account_id", *account.ParentAccountID, "error", parentErr)
			return 0, usageType
		}
		realAccount, ensureErr := s.repo.EnsureRealAccountForAccount(ctx, parent)
		if ensureErr != nil || realAccount == nil {
			slog.Warn("usage_alert_ensure_parent_real_account_failed", "account_id", accountID, "parent_account_id", parent.ID, "error", ensureErr)
			return 0, usageType
		}
		if attachErr := s.repo.AttachAccount(ctx, realAccount.ID, account.ID); attachErr != nil {
			slog.Warn("usage_alert_attach_shadow_real_account_failed", "account_id", accountID, "real_account_id", realAccount.ID, "error", attachErr)
			return 0, usageType
		}
		return realAccount.ID, usageType
	}
	realAccount, err := s.repo.EnsureRealAccountForAccount(ctx, account)
	if err != nil || realAccount == nil {
		slog.Warn("usage_alert_ensure_real_account_failed", "account_id", accountID, "error", err)
		return 0, usageType
	}
	return realAccount.ID, usageType
}

func (s *UsageAlertService) observeAsync(snapshot UsageAlertSnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	previous, err := s.repo.GetSnapshot(ctx, snapshot.RealAccountID, snapshot.UsageType)
	if err != nil {
		slog.Warn("usage_alert_get_snapshot_failed", "real_account_id", snapshot.RealAccountID, "error", err)
	}
	if err := s.repo.UpsertSnapshot(ctx, &snapshot); err != nil {
		slog.Warn("usage_alert_upsert_snapshot_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return
	}

	rules, err := s.repo.ListEnabledRules(ctx, snapshot.RealAccountID, snapshot.UsageType)
	if err != nil {
		slog.Warn("usage_alert_list_rules_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return
	}
	if len(rules) == 0 {
		return
	}

	triggers := s.evaluateRules(ctx, previous, &snapshot, rules)
	if len(triggers) == 0 {
		return
	}
	webhooks, err := s.repo.ListEnabledWebhooksForRealAccount(ctx, snapshot.RealAccountID)
	if err != nil {
		slog.Warn("usage_alert_list_webhooks_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return
	}
	if len(webhooks) == 0 {
		slog.Info("usage_alert_trigger_without_webhook", "real_account_id", snapshot.RealAccountID, "trigger_count", len(triggers))
		return
	}
	realAccount, _ := s.repo.GetRealAccount(ctx, snapshot.RealAccountID)
	for _, trigger := range triggers {
		event := buildUsageAlertWebhookEvent(&snapshot, realAccount, trigger)
		for _, webhook := range webhooks {
			go s.deliverWebhook(*webhook, event)
		}
	}
}

func (s *UsageAlertService) evaluateRules(ctx context.Context, previous, current *UsageAlertSnapshot, rules []*UsageAlertRule) []UsageAlertTrigger {
	now := time.Now().UTC()
	triggers := make([]UsageAlertTrigger, 0)
	for _, rule := range rules {
		if rule == nil || !rule.Enabled || !ruleAppliesToRealAccount(rule, current.RealAccountID) {
			continue
		}
		window, ok := current.Windows[rule.Window]
		if !ok {
			continue
		}
		state, _ := s.repo.GetState(ctx, current.RealAccountID, rule.ID, current.UsageType, rule.Window)
		value := metricValue(window, rule.Metric)
		if !resetConstraintSatisfied(window, rule.MinResetAfterHours, now) {
			if usageAlertStateWasTriggered(state) {
				triggers = append(triggers, usageAlertResolvedTrigger(rule, window, value, now))
			}
			s.updateRuleState(ctx, current.RealAccountID, rule, window, false, value)
			continue
		}
		matched := compareUsageAlertValue(value, rule.Operator, rule.Threshold)
		if !matched {
			if usageAlertStateWasTriggered(state) {
				triggers = append(triggers, usageAlertResolvedTrigger(rule, window, value, now))
			}
			s.updateRuleState(ctx, current.RealAccountID, rule, window, false, value)
			continue
		}
		if !usageAlertRuleAllowsTrigger(previous, current, rule, state, value, now) {
			continue
		}
		s.updateRuleState(ctx, current.RealAccountID, rule, window, true, value)
		triggers = append(triggers, UsageAlertTrigger{
			Rule:        rule,
			Window:      rule.Window,
			Value:       value,
			WindowState: window,
			TriggeredAt: now,
		})
	}
	return triggers
}

func usageAlertRuleAllowsTrigger(previous, current *UsageAlertSnapshot, rule *UsageAlertRule, state *UsageAlertState, value float64, now time.Time) bool {
	if usageAlertRuleStepPercent(rule) > 0 {
		return usageAlertStepAllowsTrigger(state, rule, value, now)
	}
	return crossedThreshold(previous, current, rule, value) || stateAllowsRepeat(state, rule, now)
}

func usageAlertStateWasTriggered(state *UsageAlertState) bool {
	return state != nil && state.LastStatus == UsageAlertStatusTriggered
}

func usageAlertResolvedTrigger(rule *UsageAlertRule, window UsageAlertWindowSnapshot, value float64, now time.Time) UsageAlertTrigger {
	return UsageAlertTrigger{
		Rule:        rule,
		Window:      rule.Window,
		Value:       value,
		WindowState: window,
		TriggeredAt: now,
		Resolved:    true,
	}
}

func (s *UsageAlertService) populateUsageAlertRuleScope(ctx context.Context, rule *UsageAlertRule) error {
	if s == nil || s.repo == nil || rule == nil || rule.RealAccountID == nil || *rule.RealAccountID <= 0 {
		return fmt.Errorf("real_account_id must be positive")
	}
	realAccount, err := s.repo.GetRealAccount(ctx, *rule.RealAccountID)
	if err != nil {
		return err
	}
	if realAccount == nil {
		return fmt.Errorf("real account not found")
	}
	rule.Platform = realAccount.Platform
	rule.UsageType = normalizeUsageAlertUsageType(rule.UsageType)
	if !usageAlertTypeSupportsRule(realAccount.Platform, rule.UsageType, rule.Window) {
		return fmt.Errorf("usage type %q does not support %s %s alerts", rule.UsageType, realAccount.Platform, rule.Window)
	}
	rule.RealAccount = realAccount
	return nil
}

func (s *UsageAlertService) updateRuleState(ctx context.Context, realAccountID int64, rule *UsageAlertRule, window UsageAlertWindowSnapshot, triggered bool, value float64) {
	status := UsageAlertStatusNormal
	var triggeredAt *time.Time
	if triggered {
		status = UsageAlertStatusTriggered
		now := time.Now().UTC()
		triggeredAt = &now
	}
	state := &UsageAlertState{
		RealAccountID:   realAccountID,
		RuleID:          rule.ID,
		UsageType:       rule.UsageType,
		Window:          rule.Window,
		LastStatus:      status,
		LastTriggeredAt: triggeredAt,
		LastValue:       &value,
		LastResetAt:     window.ResetAt,
	}
	if err := s.repo.UpsertState(ctx, state); err != nil {
		slog.Warn("usage_alert_upsert_state_failed", "real_account_id", realAccountID, "usage_type", rule.UsageType, "rule_id", rule.ID, "error", err)
	}
}

func (s *UsageAlertService) deliverWebhook(webhook UsageAlertWebhook, event UsageAlertWebhookEvent) {
	if s == nil || s.httpClient == nil || !webhook.Enabled {
		return
	}
	if err := s.deliverWebhookWithRetry(context.Background(), webhook, event); err != nil {
		slog.Warn("usage_alert_webhook_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "attempts", webhook.RetryCount+1, "error", err)
	}
}

func (s *UsageAlertService) deliverWebhookWithRetry(ctx context.Context, webhook UsageAlertWebhook, event UsageAlertWebhookEvent) error {
	if s == nil || s.httpClient == nil {
		return fmt.Errorf("usage alert webhook sender is not configured")
	}
	if !webhook.Enabled {
		return nil
	}
	if err := validateUsageAlertWebhook(&webhook); err != nil {
		return err
	}
	attempts := webhook.RetryCount + 1
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = s.deliverWebhookOnce(attemptCtx, webhook, event)
		cancel()
		if err == nil {
			slog.Info("usage_alert_webhook_delivered", "webhook_id", webhook.ID, "event_id", event.EventID, "attempt", attempt, "type", webhook.Type)
			return nil
		}
		if attempt < attempts {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
	return err
}

func (s *UsageAlertService) deliverWebhookOnce(ctx context.Context, webhook UsageAlertWebhook, event UsageAlertWebhookEvent) error {
	switch webhook.Type {
	case "", UsageAlertWebhookTypeJSONPost:
		return s.deliverJSONPostWebhook(ctx, webhook, event)
	case UsageAlertWebhookTypeTelegram:
		return s.deliverTelegramWebhook(ctx, webhook, event)
	default:
		return ErrUsageAlertInvalidWebhook
	}
}

func (s *UsageAlertService) deliverJSONPostWebhook(ctx context.Context, webhook UsageAlertWebhook, event UsageAlertWebhookEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sub2API-UsageAlert/1.0")
	return s.doWebhookRequest(req)
}

func (s *UsageAlertService) deliverTelegramWebhook(ctx context.Context, webhook UsageAlertWebhook, event UsageAlertWebhookEvent) error {
	cfg, err := usageAlertTelegramConfig(webhook.Config)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":              cfg.ChatID,
		"text":                 formatUsageAlertTelegramMessage(event, cfg),
		"disable_notification": cfg.DisableNotification,
	}
	if cfg.MessageThreadID != nil && *cfg.MessageThreadID > 0 {
		payload["message_thread_id"] = *cfg.MessageThreadID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.BotToken), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("telegram sendMessage request failed: %s", redactUsageAlertSecret(err.Error(), cfg.BotToken))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sub2API-UsageAlert/1.0")
	if err := s.doWebhookRequest(req); err != nil {
		return fmt.Errorf("telegram sendMessage failed: %s", redactUsageAlertSecret(err.Error(), cfg.BotToken))
	}
	return nil
}

func (s *UsageAlertService) doWebhookRequest(req *http.Request) error {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, msg)
}

func validateRealAccount(account *RealAccount) error {
	if account == nil {
		return fmt.Errorf("real account is required")
	}
	account.Name = strings.TrimSpace(account.Name)
	account.Platform = strings.TrimSpace(account.Platform)
	if account.Name == "" {
		return fmt.Errorf("real account name is required")
	}
	if account.Platform != UsageAlertPlatformOpenAI && account.Platform != UsageAlertPlatformAnthropic {
		return ErrUsageAlertInvalidPlatform
	}
	return nil
}

func validateUsageAlertRule(rule *UsageAlertRule) error {
	if rule == nil {
		return fmt.Errorf("usage alert rule is required")
	}
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Platform = strings.TrimSpace(rule.Platform)
	rule.UsageType = normalizeUsageAlertUsageType(rule.UsageType)
	rule.Window = strings.TrimSpace(rule.Window)
	rule.Metric = strings.TrimSpace(rule.Metric)
	rule.Operator = strings.TrimSpace(rule.Operator)
	if rule.RealAccountID == nil || *rule.RealAccountID <= 0 {
		return fmt.Errorf("real_account_id must be positive")
	}
	if rule.Platform != "" && rule.Platform != UsageAlertPlatformAll && rule.Platform != UsageAlertPlatformOpenAI && rule.Platform != UsageAlertPlatformAnthropic {
		return ErrUsageAlertInvalidPlatform
	}
	if !isValidUsageAlertUsageType(rule.UsageType) {
		return ErrUsageAlertInvalidUsageType
	}
	if rule.Window != UsageAlertWindow5h && rule.Window != UsageAlertWindow7d {
		return ErrUsageAlertInvalidWindow
	}
	if rule.Metric != UsageAlertMetricUsed && rule.Metric != UsageAlertMetricRemaining {
		return ErrUsageAlertInvalidMetric
	}
	if rule.Operator != UsageAlertOperatorGTE && rule.Operator != UsageAlertOperatorLTE {
		return ErrUsageAlertInvalidOperator
	}
	if math.IsNaN(rule.Threshold) || math.IsInf(rule.Threshold, 0) || rule.Threshold < 0 || rule.Threshold > 100 {
		return fmt.Errorf("threshold must be between 0 and 100")
	}
	if rule.MinResetAfterHours != nil && (*rule.MinResetAfterHours < 0 || math.IsNaN(*rule.MinResetAfterHours) || math.IsInf(*rule.MinResetAfterHours, 0)) {
		return fmt.Errorf("min_reset_after_hours must be non-negative")
	}
	if rule.StepPercent != nil && (*rule.StepPercent < 0 || *rule.StepPercent > 100 || math.IsNaN(*rule.StepPercent) || math.IsInf(*rule.StepPercent, 0)) {
		return fmt.Errorf("step_percent must be between 0 and 100")
	}
	if rule.CooldownMinutes < 0 {
		return fmt.Errorf("cooldown_minutes must be non-negative")
	}
	return nil
}

func ensureUsageAlertRuleName(rule *UsageAlertRule) {
	if rule == nil {
		return
	}
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		rule.Name = buildUsageAlertRuleDefaultName(rule)
	}
	rule.Name = truncateUsageAlertRuleName(rule.Name)
}

func buildUsageAlertRuleDefaultName(rule *UsageAlertRule) string {
	accountName := ""
	if rule.RealAccount != nil {
		accountName = strings.TrimSpace(rule.RealAccount.Name)
	}
	if accountName == "" && rule.RealAccountID != nil && *rule.RealAccountID > 0 {
		accountName = fmt.Sprintf("real-account-%d", *rule.RealAccountID)
	}
	if accountName == "" {
		accountName = "real-account"
	}

	parts := []string{
		accountName,
		usageAlertPlatformDisplayName(rule.Platform, "en"),
		usageAlertUsageTypeDisplayName(rule.UsageType, "en"),
		usageAlertWindowDisplayName(rule.Window, "en"),
		usageAlertMetricDisplayName(rule.Metric, "en"),
		fmt.Sprintf("%s %s%%", rule.Operator, formatUsageAlertRuleNumber(rule.Threshold)),
	}
	if rule.StepPercent != nil {
		parts = append(parts, fmt.Sprintf("step %s%%", formatUsageAlertRuleNumber(*rule.StepPercent)))
	}
	if rule.MinResetAfterHours != nil {
		parts = append(parts, fmt.Sprintf("reset left %sh", formatUsageAlertRuleNumber(*rule.MinResetAfterHours)))
	}
	if rule.CooldownMinutes > 0 {
		parts = append(parts, fmt.Sprintf("cooldown %dm", rule.CooldownMinutes))
	}
	return strings.Join(parts, " ")
}

func truncateUsageAlertRuleName(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) <= usageAlertRuleNameMaxLength {
		return string(runes)
	}
	return string(runes[:usageAlertRuleNameMaxLength])
}

func formatUsageAlertRuleNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func validateUsageAlertWebhook(webhook *UsageAlertWebhook) error {
	if webhook == nil {
		return fmt.Errorf("usage alert webhook is required")
	}
	webhook.Name = strings.TrimSpace(webhook.Name)
	webhook.Type = strings.TrimSpace(webhook.Type)
	webhook.URL = strings.TrimSpace(webhook.URL)
	if webhook.Name == "" {
		return fmt.Errorf("webhook name is required")
	}
	if webhook.Type == "" {
		webhook.Type = UsageAlertWebhookTypeJSONPost
	}
	if webhook.Config == nil {
		webhook.Config = map[string]any{}
	}
	switch webhook.Type {
	case UsageAlertWebhookTypeJSONPost:
		if webhook.URL == "" {
			return fmt.Errorf("webhook url is required")
		}
		parsed, err := url.ParseRequestURI(webhook.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("webhook url is invalid")
		}
	case UsageAlertWebhookTypeTelegram:
		if _, err := usageAlertTelegramConfig(webhook.Config); err != nil {
			return err
		}
	default:
		return ErrUsageAlertInvalidWebhook
	}
	if webhook.RetryCount < 0 || webhook.RetryCount > 10 {
		return fmt.Errorf("retry_count must be between 0 and 10")
	}
	return nil
}

func usageAlertTelegramConfig(config map[string]any) (UsageAlertTelegramConfig, error) {
	cfg := UsageAlertTelegramConfig{
		BotToken:            strings.TrimSpace(usageAlertConfigString(config, "bot_token")),
		ChatID:              strings.TrimSpace(usageAlertConfigString(config, "chat_id")),
		Language:            normalizeUsageAlertLanguage(usageAlertConfigString(config, "language")),
		Timezone:            strings.TrimSpace(usageAlertConfigString(config, "timezone")),
		DisableNotification: usageAlertConfigBool(config, "disable_notification"),
	}
	if cfg.BotToken == "" {
		return cfg, fmt.Errorf("telegram bot_token is required")
	}
	if strings.ContainsAny(cfg.BotToken, " \t\r\n/") {
		return cfg, fmt.Errorf("telegram bot_token is invalid")
	}
	if cfg.ChatID == "" {
		return cfg, fmt.Errorf("telegram chat_id is required")
	}
	threadID, hasThreadID, err := usageAlertConfigInt64(config, "message_thread_id")
	if err != nil {
		return cfg, fmt.Errorf("telegram message_thread_id is invalid")
	}
	if hasThreadID {
		if threadID < 0 {
			return cfg, fmt.Errorf("telegram message_thread_id must be non-negative")
		}
		if threadID > 0 {
			cfg.MessageThreadID = &threadID
		}
	}
	if cfg.Timezone != "" {
		if _, err := time.LoadLocation(cfg.Timezone); err != nil {
			return cfg, fmt.Errorf("telegram timezone is invalid")
		}
	}
	return cfg, nil
}

func normalizeUsageAlertLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "zh", "zh-cn", "zh_hans", "zh-hans", "cn":
		return "zh"
	default:
		return "en"
	}
}

func usageAlertConfigString(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func usageAlertConfigBool(config map[string]any, key string) bool {
	if config == nil {
		return false
	}
	value, ok := config[key]
	if !ok || value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(v))
		return parsed
	default:
		return false
	}
}

func usageAlertConfigInt64(config map[string]any, key string) (int64, bool, error) {
	if config == nil {
		return 0, false, nil
	}
	value, ok := config[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	switch v := value.(type) {
	case int:
		return int64(v), true, nil
	case int64:
		return v, true, nil
	case float64:
		if math.Trunc(v) != v {
			return 0, true, fmt.Errorf("must be an integer")
		}
		return int64(v), true, nil
	case json.Number:
		parsed, err := v.Int64()
		return parsed, true, err
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		return parsed, true, err
	default:
		return 0, true, fmt.Errorf("unsupported value type")
	}
}

func formatUsageAlertTelegramMessage(event UsageAlertWebhookEvent, cfg UsageAlertTelegramConfig) string {
	location := usageAlertTelegramLocation(cfg.Timezone)
	accountName := strings.TrimSpace(event.RealAccountName)
	if accountName == "" {
		accountName = fmt.Sprintf("#%d", event.RealAccountID)
	}
	resetAt := usageAlertFormatTime(nil, location)
	if event.ResetAt != nil {
		resetAt = usageAlertFormatTime(event.ResetAt, location)
	}
	triggeredAt := usageAlertFormatTime(&event.TriggeredAt, location)
	if cfg.Language == "zh" {
		title := "[Sub2API] 用量告警"
		timeLabel := "触发时间"
		switch event.Event {
		case "account.usage_alert.test":
			title = "[Sub2API] 测试通知"
		case UsageAlertEventResolved:
			title = "[Sub2API] 用量告警已重置"
			timeLabel = "重置通知时间"
		}
		return fmt.Sprintf(
			"%s\n账户：%s\n平台：%s\n用量类型：%s\n规则：%s\n窗口：%s\n已用：%.1f%%\n剩余：%.1f%%\n阈值：%s %s %.1f%%\n重置时间：%s\n%s：%s",
			title,
			accountName,
			usageAlertPlatformDisplayName(event.Platform, "zh"),
			usageAlertUsageTypeDisplayName(event.UsageType, "zh"),
			event.RuleName,
			usageAlertWindowDisplayName(event.Window, "zh"),
			event.UsedPercent,
			event.RemainingPercent,
			usageAlertMetricDisplayName(event.Metric, "zh"),
			event.Operator,
			event.Threshold,
			resetAt,
			timeLabel,
			triggeredAt,
		)
	}
	title := "[Sub2API] Usage alert"
	timeLabel := "Triggered"
	switch event.Event {
	case "account.usage_alert.test":
		title = "[Sub2API] Test notification"
	case UsageAlertEventResolved:
		title = "[Sub2API] Usage alert reset"
		timeLabel = "Reset notified"
	}
	return fmt.Sprintf(
		"%s\nAccount: %s\nPlatform: %s\nUsage type: %s\nRule: %s\nWindow: %s\nUsed: %.1f%%\nRemaining: %.1f%%\nThreshold: %s %s %.1f%%\nReset: %s\n%s: %s",
		title,
		accountName,
		usageAlertPlatformDisplayName(event.Platform, "en"),
		usageAlertUsageTypeDisplayName(event.UsageType, "en"),
		event.RuleName,
		usageAlertWindowDisplayName(event.Window, "en"),
		event.UsedPercent,
		event.RemainingPercent,
		usageAlertMetricDisplayName(event.Metric, "en"),
		event.Operator,
		event.Threshold,
		resetAt,
		timeLabel,
		triggeredAt,
	)
}

func usageAlertTelegramLocation(timezone string) *time.Location {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return time.Local
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Local
	}
	return location
}

func usageAlertFormatTime(value *time.Time, location *time.Location) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	if location == nil {
		location = time.Local
	}
	return value.In(location).Format("2006-01-02 15:04:05 MST")
}

func usageAlertPlatformDisplayName(platform, language string) string {
	switch platform {
	case UsageAlertPlatformOpenAI:
		return "OpenAI"
	case UsageAlertPlatformAnthropic:
		return "Claude"
	default:
		if language == "zh" {
			return "全部"
		}
		return platform
	}
}

func normalizeUsageAlertUsageType(usageType string) string {
	usageType = strings.ToLower(strings.TrimSpace(usageType))
	if usageType == "" || usageType == QuotaDimensionGlobal {
		return UsageAlertTypeOverall
	}
	return usageType
}

func isValidUsageAlertUsageType(usageType string) bool {
	if usageType == "" || len(usageType) > 32 {
		return false
	}
	for _, char := range usageType {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func usageAlertTypeSupportsRule(platform, usageType, window string) bool {
	switch platform {
	case UsageAlertPlatformOpenAI:
		return usageType == UsageAlertTypeOverall || usageType == UsageAlertTypeSpark
	case UsageAlertPlatformAnthropic:
		if usageType == UsageAlertTypeOverall {
			return true
		}
		return usageType == UsageAlertTypeFable && window == UsageAlertWindow7d
	default:
		return false
	}
}

func usageAlertLegacyQuotaDimension(usageType string) string {
	if normalizeUsageAlertUsageType(usageType) == UsageAlertTypeSpark {
		return QuotaDimensionSpark
	}
	return QuotaDimensionGlobal
}

func usageAlertTypeRelationship(usageType string) (relation, parent string) {
	switch normalizeUsageAlertUsageType(usageType) {
	case UsageAlertTypeSpark:
		return UsageAlertRelationShadow, UsageAlertTypeOverall
	case UsageAlertTypeFable:
		return UsageAlertRelationSubLimit, UsageAlertTypeOverall
	default:
		return UsageAlertRelationPrimary, ""
	}
}

func usageAlertUsageTypeDisplayName(usageType, language string) string {
	switch normalizeUsageAlertUsageType(usageType) {
	case UsageAlertTypeSpark:
		return "Spark"
	case UsageAlertTypeFable:
		return "Fable"
	default:
		if language == "zh" {
			return "总额度"
		}
		return "Overall"
	}
}

func usageAlertWindowDisplayName(window, language string) string {
	switch window {
	case UsageAlertWindow5h:
		if language == "zh" {
			return "5 小时"
		}
		return "5h"
	case UsageAlertWindow7d:
		if language == "zh" {
			return "7 天"
		}
		return "7d"
	default:
		return window
	}
}

func usageAlertMetricDisplayName(metric, language string) string {
	switch metric {
	case UsageAlertMetricUsed:
		if language == "zh" {
			return "已用"
		}
		return "used"
	case UsageAlertMetricRemaining:
		if language == "zh" {
			return "剩余"
		}
		return "remaining"
	default:
		return metric
	}
}

func redactUsageAlertSecret(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}

func ruleAppliesToRealAccount(rule *UsageAlertRule, realAccountID int64) bool {
	return rule != nil && rule.RealAccountID != nil && *rule.RealAccountID == realAccountID
}

func resetConstraintSatisfied(window UsageAlertWindowSnapshot, minHours *float64, now time.Time) bool {
	if minHours == nil {
		return true
	}
	if window.ResetAt == nil {
		return false
	}
	return window.ResetAt.Sub(now).Hours() >= *minHours
}

func metricValue(window UsageAlertWindowSnapshot, metric string) float64 {
	if metric == UsageAlertMetricRemaining {
		return window.RemainingPercent
	}
	return window.UsedPercent
}

func compareUsageAlertValue(value float64, operator string, threshold float64) bool {
	if operator == UsageAlertOperatorLTE {
		return value <= threshold
	}
	return value >= threshold
}

func crossedThreshold(previous, current *UsageAlertSnapshot, rule *UsageAlertRule, currentValue float64) bool {
	if previous == nil {
		return true
	}
	prevWindow, ok := previous.Windows[rule.Window]
	if !ok {
		return true
	}
	prevValue := metricValue(prevWindow, rule.Metric)
	if rule.Operator == UsageAlertOperatorLTE {
		return prevValue > rule.Threshold && currentValue <= rule.Threshold
	}
	return prevValue < rule.Threshold && currentValue >= rule.Threshold
}

func stateAllowsRepeat(state *UsageAlertState, rule *UsageAlertRule, now time.Time) bool {
	if state == nil || state.LastStatus != UsageAlertStatusTriggered || state.LastTriggeredAt == nil {
		return true
	}
	if rule.CooldownMinutes <= 0 {
		return false
	}
	return now.Sub(*state.LastTriggeredAt) >= time.Duration(rule.CooldownMinutes)*time.Minute
}

func usageAlertRuleStepPercent(rule *UsageAlertRule) float64 {
	if rule == nil || rule.StepPercent == nil {
		return 0
	}
	return *rule.StepPercent
}

func usageAlertStepAllowsTrigger(state *UsageAlertState, rule *UsageAlertRule, currentValue float64, now time.Time) bool {
	step := usageAlertRuleStepPercent(rule)
	if step <= 0 {
		return false
	}
	if state == nil || state.LastStatus != UsageAlertStatusTriggered {
		return true
	}
	if !stateAllowsRepeat(state, rule, now) {
		return false
	}
	if state.LastValue == nil {
		return true
	}
	if rule.Operator == UsageAlertOperatorLTE {
		return currentValue <= *state.LastValue-step
	}
	return currentValue >= *state.LastValue+step
}

func buildUsageAlertWebhookEvent(snapshot *UsageAlertSnapshot, realAccount *RealAccount, trigger UsageAlertTrigger) UsageAlertWebhookEvent {
	realAccountName := ""
	if realAccount != nil {
		realAccountName = realAccount.Name
	}
	eventName := UsageAlertEventTriggered
	if trigger.Resolved {
		eventName = UsageAlertEventResolved
	}
	return UsageAlertWebhookEvent{
		Event:            eventName,
		EventID:          fmt.Sprintf("%d-%s-%d-%s-%d", snapshot.RealAccountID, snapshot.UsageType, trigger.Rule.ID, trigger.Window, trigger.TriggeredAt.UnixNano()),
		TriggeredAt:      trigger.TriggeredAt,
		AccountID:        snapshot.AccountID,
		RealAccountID:    snapshot.RealAccountID,
		RealAccountName:  realAccountName,
		UsageType:        snapshot.UsageType,
		UsageRelation:    snapshot.UsageRelation,
		ParentUsageType:  snapshot.ParentUsageType,
		QuotaDimension:   usageAlertLegacyQuotaDimension(snapshot.UsageType),
		Platform:         snapshot.Platform,
		Source:           snapshot.Source,
		RuleID:           trigger.Rule.ID,
		RuleName:         trigger.Rule.Name,
		Window:           trigger.Window,
		Metric:           trigger.Rule.Metric,
		Operator:         trigger.Rule.Operator,
		Threshold:        trigger.Rule.Threshold,
		Value:            trigger.Value,
		UsedPercent:      trigger.WindowState.UsedPercent,
		RemainingPercent: trigger.WindowState.RemainingPercent,
		ResetAt:          trigger.WindowState.ResetAt,
		StepPercent:      trigger.Rule.StepPercent,
	}
}

func usageAlertSnapshotsFromUsageInfo(accountID int64, platform, source string, usage *UsageInfo, sampledAt time.Time) []*UsageAlertSnapshot {
	if accountID <= 0 || usage == nil {
		return nil
	}
	snapshots := make([]*UsageAlertSnapshot, 0, 2)
	windows := make(map[string]UsageAlertWindowSnapshot, 2)
	if usage.FiveHour != nil {
		windows[UsageAlertWindow5h] = usageAlertWindowFromProgress(usage.FiveHour)
	}
	if usage.SevenDay != nil {
		windows[UsageAlertWindow7d] = usageAlertWindowFromProgress(usage.SevenDay)
	}
	overallType := []string(nil)
	if platform == UsageAlertPlatformAnthropic {
		overallType = []string{UsageAlertTypeOverall}
	}
	if snapshot := usageAlertSnapshotFromWindows(accountID, platform, source, windows, sampledAt, overallType...); snapshot != nil {
		snapshots = append(snapshots, snapshot)
	}
	if platform == UsageAlertPlatformAnthropic && usage.SevenDayFable != nil {
		fable := usageAlertWindowFromProgress(usage.SevenDayFable)
		// Fable is a 7d sub-limit under Overall and shares the parent reset.
		if usage.SevenDay != nil && usage.SevenDay.ResetsAt != nil {
			fable.ResetAt = usage.SevenDay.ResetsAt
		}
		if snapshot := usageAlertSnapshotFromWindows(accountID, platform, source, map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: fable,
		}, sampledAt, UsageAlertTypeFable); snapshot != nil {
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots
}

func usageAlertWindowFromProgress(progress *UsageProgress) UsageAlertWindowSnapshot {
	if progress == nil {
		return UsageAlertWindowSnapshot{}
	}
	used := progress.Utilization
	return UsageAlertWindowSnapshot{
		UsedPercent:      used,
		RemainingPercent: usageAlertRemainingPercent(used),
		ResetAt:          progress.ResetsAt,
	}
}

func usageAlertSnapshotFromCodexExtra(accountID int64, source string, extra map[string]any, sampledAt time.Time) *UsageAlertSnapshot {
	if accountID <= 0 || len(extra) == 0 {
		return nil
	}
	now := sampledAt
	if now.IsZero() {
		now = time.Now()
	}
	windows := make(map[string]UsageAlertWindowSnapshot, 2)
	if progress := buildCodexUsageProgressFromExtra(extra, UsageAlertWindow5h, now); progress != nil {
		windows[UsageAlertWindow5h] = usageAlertWindowFromProgress(progress)
	}
	if progress := buildCodexUsageProgressFromExtra(extra, UsageAlertWindow7d, now); progress != nil {
		windows[UsageAlertWindow7d] = usageAlertWindowFromProgress(progress)
	}
	return usageAlertSnapshotFromWindows(accountID, UsageAlertPlatformOpenAI, source, windows, now)
}

func usageAlertSnapshotFromWindows(accountID int64, platform, source string, windows map[string]UsageAlertWindowSnapshot, sampledAt time.Time, usageTypes ...string) *UsageAlertSnapshot {
	if accountID <= 0 || len(windows) == 0 {
		return nil
	}
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	usageType := ""
	if len(usageTypes) > 0 {
		usageType = normalizeUsageAlertUsageType(usageTypes[0])
	}
	relation, parent := usageAlertTypeRelationship(usageType)
	return &UsageAlertSnapshot{
		AccountID:       accountID,
		Platform:        platform,
		UsageType:       usageType,
		UsageRelation:   relation,
		ParentUsageType: parent,
		Source:          source,
		Windows:         windows,
		SampledAt:       sampledAt.UTC(),
	}
}

func usageAlertRemainingPercent(usedPercent float64) float64 {
	remaining := 100 - usedPercent
	if remaining < 0 {
		return 0
	}
	return remaining
}
