package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
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
	UsageAlertEventReset     = "account.usage_alert.account_reset"

	UsageAlertStatusNormal    = "normal"
	UsageAlertStatusTriggered = "triggered"

	usageAlertRuleNameMaxLength = 100
	usageAlertDeliveryLease     = 3 * time.Minute
	usageAlertDeliveryRetention = 90 * 24 * time.Hour
	usageAlertCleanupInterval   = 24 * time.Hour
	usageAlertRetryMinDelay     = 30 * time.Second
	usageAlertRetryMaxDelay     = 5 * time.Minute
	codex7dBoundaryRefreshDelay = 30 * time.Second
	codex7dResetDriftWindow     = time.Hour
	codex7dUsageRollbackPoints  = 1.0

	UsageAlertSourceOpenAICodexHeaders  = "openai_codex_headers"
	UsageAlertSourceOpenAICodexProbe    = "openai_codex_probe"
	UsageAlertSourceOpenAICodexUsageAPI = "openai_codex_usage_api"
	UsageAlertSourceOpenAIQuotaReset    = "openai_quota_reset"
	UsageAlertSourceClaudeHeaders       = "claude_headers"
	UsageAlertSourceClaudeUsageAPI      = "claude_usage_api"
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
	// HasOnlyDeletedAccounts distinguishes retired sources from new, unbound ones.
	HasOnlyDeletedAccounts bool `json:"has_only_deleted_accounts,omitempty"`
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
	LastGeneration  int64      `json:"last_generation"`
	// ResetOrderProtected is an in-process repository write policy. Codex 7d
	// uses generation ordering because its projected reset_at drifts.
	ResetOrderProtected bool      `json:"-"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type UsageAlertWindowSnapshot struct {
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	SampledAt        *time.Time `json:"sampled_at,omitempty"`
	Generation       int64      `json:"generation,omitempty"`
	BoundaryAt       *time.Time `json:"boundary_at,omitempty"`

	OfficialResetAnchorAt *time.Time `json:"official_reset_anchor_at,omitempty"`
	AwaitingOfficialReset bool       `json:"awaiting_official_reset,omitempty"`

	// In-memory provenance for pending merged windows before persistence/evaluation.
	Source    string `json:"-"`
	AccountID int64  `json:"-"`
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
	Rule         *UsageAlertRule
	Window       string
	Value        float64
	WindowState  UsageAlertWindowSnapshot
	TriggeredAt  time.Time
	Resolved     bool
	AccountReset bool
	StateAnchor  string
}

type UsageAlertDeliveryClaim int

const (
	UsageAlertDeliveryBusy UsageAlertDeliveryClaim = iota
	UsageAlertDeliveryClaimed
	UsageAlertDeliveryAlreadyDelivered
)

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
	AccountReset     bool       `json:"account_reset,omitempty"`
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
	// UpsertSnapshot returns false when a newer snapshot already exists.
	UpsertSnapshot(ctx context.Context, snapshot *UsageAlertSnapshot) (bool, error)
	GetState(ctx context.Context, realAccountID, ruleID int64, usageType, window string) (*UsageAlertState, error)
	UpsertState(ctx context.Context, state *UsageAlertState) error
	// Delivery is at-least-once across lease recovery. Consumers should use
	// event_id as their idempotency key.
	ClaimWebhookDelivery(ctx context.Context, eventID string, realAccountID, ruleID, webhookID int64, claimToken string, lease time.Duration) (UsageAlertDeliveryClaim, error)
	CompleteWebhookDelivery(ctx context.Context, eventID string, webhookID int64, claimToken string) error
	ReleaseWebhookDelivery(ctx context.Context, eventID string, webhookID int64, claimToken string) error
}

type UsageAlertService struct {
	repo                          UsageAlertRepository
	accountRepo                   AccountRepository
	httpClient                    *http.Client
	disallowPrivateWebhookTargets bool
	codexBoundaryRefresher        OpenAICodexUsageSnapshotRefresher
	evaluationLocks               sync.Map
	workers                       sync.Map
	stopMu                        sync.Mutex
	stopOnce                      sync.Once
	stopCh                        chan struct{}
	serviceCtx                    context.Context
	serviceCancel                 context.CancelFunc
	stopped                       atomic.Bool
	wg                            sync.WaitGroup
	cleanupMu                     sync.Mutex
	cleanupRunning                bool
	lastCleanupAt                 time.Time
	codexBoundaryRefreshMu        sync.Mutex
	codexBoundaryRefreshAt        map[int64]time.Time
}

type usageAlertObservationWorker struct {
	mu               sync.Mutex
	running          bool
	waiting          bool
	pending          *UsageAlertSnapshot
	manualPending    *UsageAlertSnapshot
	authorityPending *UsageAlertSnapshot
	retryPending     *UsageAlertSnapshot
	retryDelay       time.Duration
	wake             chan struct{}
}

type usageAlertDeliveryCleaner interface {
	CleanupWebhookDeliveries(ctx context.Context, deliveredBefore time.Time) error
}

func NewUsageAlertService(repo UsageAlertRepository, accountRepo AccountRepository) *UsageAlertService {
	ctx, cancel := context.WithCancel(context.Background())
	return &UsageAlertService{
		repo:                          repo,
		accountRepo:                   accountRepo,
		httpClient:                    newSafeUsageAlertHTTPClient(8 * time.Second),
		disallowPrivateWebhookTargets: true,
		stopCh:                        make(chan struct{}),
		serviceCtx:                    ctx,
		serviceCancel:                 cancel,
	}
}

func (s *UsageAlertService) stopSignal() <-chan struct{} {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopCh == nil {
		s.stopCh = make(chan struct{})
	}
	return s.stopCh
}

func (s *UsageAlertService) baseContext() context.Context {
	if s == nil {
		return context.Background()
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.serviceCtx == nil {
		s.serviceCtx, s.serviceCancel = context.WithCancel(context.Background())
	}
	return s.serviceCtx
}

func (s *UsageAlertService) tryAddWorker() bool {
	if s == nil {
		return false
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopped.Load() {
		return false
	}
	s.wg.Add(1)
	return true
}

// Stop signals active observation workers to exit wait loops, cancels in-flight
// background contexts, and waits for goroutines to drain.
func (s *UsageAlertService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.stopMu.Lock()
		s.stopped.Store(true)
		if s.stopCh == nil {
			s.stopCh = make(chan struct{})
		}
		close(s.stopCh)
		if s.serviceCancel != nil {
			s.serviceCancel()
		}
		s.stopMu.Unlock()
	})
	s.wg.Wait()
}

const telegramWebhookHost = "api.telegram.org"

type usageAlertProxyDecisionKey struct{}

type usageAlertProxyTunnel struct {
	proxyURL *url.URL
	ips      []net.IP
}

type usageAlertProxyDecision struct {
	transportProxyURL   *url.URL
	directProxyDialAddr string
	connectTunnel       *usageAlertProxyTunnel
	socksTunnel         *usageAlertProxyTunnel
}

type usageAlertBufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *usageAlertBufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

type safeUsageAlertTransport struct {
	base        *http.Transport
	proxyFunc   func(*http.Request) (*url.URL, error)
	resolveFunc func(context.Context, string) ([]net.IP, error)
}

func (t *safeUsageAlertTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("webhook request url is missing")
	}
	if err := validateUsageAlertOutboundURL(req.URL, false); err != nil {
		return nil, fmt.Errorf("webhook url is not allowed: %w", err)
	}
	proxyFn := http.ProxyFromEnvironment
	if t.proxyFunc != nil {
		proxyFn = t.proxyFunc
	}
	resolveFn := resolveAllowedWebhookIPs
	if t.resolveFunc != nil {
		resolveFn = t.resolveFunc
	}
	proxyURL, err := proxyFn(req)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		clonedReq := req.Clone(context.WithValue(req.Context(), usageAlertProxyDecisionKey{}, usageAlertProxyDecision{}))
		return t.base.RoundTrip(clonedReq)
	}

	host := strings.ToLower(strings.TrimSpace(req.URL.Hostname()))
	proxyScheme := strings.ToLower(strings.TrimSpace(proxyURL.Scheme))
	switch proxyScheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported webhook proxy scheme: %s", proxyURL.Scheme)
	}

	// Skip local DNS resolution only for Telegram when an operator-configured
	// environment proxy handles egress (e.g., split-horizon/GFW setups).
	if host == telegramWebhookHost {
		decision := usageAlertProxyDecision{
			transportProxyURL:   proxyURL,
			directProxyDialAddr: canonicalProxyDialAddr(proxyURL),
		}
		if proxyScheme == "socks5" || proxyScheme == "socks5h" {
			decision.transportProxyURL = nil
			decision.directProxyDialAddr = ""
			decision.socksTunnel = &usageAlertProxyTunnel{proxyURL: proxyURL}
		}
		clonedReq := req.Clone(context.WithValue(req.Context(), usageAlertProxyDecisionKey{}, decision))
		if decision.socksTunnel != nil {
			clonedReq.Close = true
		}
		return t.base.RoundTrip(clonedReq)
	}

	ips, err := resolveFn(req.Context(), host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no resolved addresses available")
	}

	if proxyScheme == "socks5" || proxyScheme == "socks5h" {
		clonedReq := req.Clone(context.WithValue(req.Context(), usageAlertProxyDecisionKey{}, usageAlertProxyDecision{
			socksTunnel: &usageAlertProxyTunnel{proxyURL: proxyURL, ips: ips},
		}))
		clonedReq.Close = true
		return t.base.RoundTrip(clonedReq)
	}

	if strings.EqualFold(req.URL.Scheme, "https") {
		clonedReq := req.Clone(context.WithValue(req.Context(), usageAlertProxyDecisionKey{}, usageAlertProxyDecision{
			connectTunnel: &usageAlertProxyTunnel{proxyURL: proxyURL, ips: ips},
		}))
		clonedReq.Close = true
		return t.base.RoundTrip(clonedReq)
	}

	// For plain HTTP over an HTTP/HTTPS forward proxy (without CONNECT),
	// RFC 9112 section 3.2.2 couples the request-target authority and Host
	// header, so rewriting the request-target to an IP breaks origin vhost
	// routing. Local URL, redirect, and DNS/IP checks have already validated the
	// target host against the trusted operator proxy boundary.
	clonedReq := req.Clone(context.WithValue(req.Context(), usageAlertProxyDecisionKey{}, usageAlertProxyDecision{
		transportProxyURL:   proxyURL,
		directProxyDialAddr: canonicalProxyDialAddr(proxyURL),
	}))
	return t.base.RoundTrip(clonedReq)
}

func dialUsageAlertResolvedIPs(ctx context.Context, network, addr string, dialFn func(context.Context, string, string) (net.Conn, error), ips []net.IP) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook dial target: %w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("no resolved addresses available")
	}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialFn(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no resolved addresses available")
}

func dialUsageAlertHTTPProxyTunnel(
	ctx context.Context,
	baseDialer *net.Dialer,
	baseTransport *http.Transport,
	network, addr string,
	tunnel *usageAlertProxyTunnel,
) (net.Conn, error) {
	if tunnel == nil || tunnel.proxyURL == nil {
		return nil, errors.New("webhook proxy configuration is missing")
	}
	proxyAddr := canonicalProxyDialAddr(tunnel.proxyURL)
	if proxyAddr == "" {
		return nil, errors.New("webhook proxy address is invalid")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook dial target: %w", err)
	}
	if len(tunnel.ips) == 0 {
		return nil, errors.New("no resolved addresses available")
	}

	var proxyAuth string
	if tunnel.proxyURL.User != nil {
		username := tunnel.proxyURL.User.Username()
		password, _ := tunnel.proxyURL.User.Password()
		proxyAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	}
	isHTTPSProxy := strings.EqualFold(tunnel.proxyURL.Scheme, "https")

	var lastErr error
	for _, ip := range tunnel.ips {
		conn, dialErr := baseDialer.DialContext(ctx, network, proxyAddr)
		if dialErr != nil {
			// Proxy TCP connection failure affects the proxy itself; do not loop across origin IPs.
			return nil, dialErr
		}
		if isHTTPSProxy {
			var tlsCfg *tls.Config
			if baseTransport != nil && baseTransport.TLSClientConfig != nil {
				tlsCfg = baseTransport.TLSClientConfig.Clone()
			} else {
				tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
			}
			tlsCfg.ServerName = tunnel.proxyURL.Hostname()
			tlsCfg.NextProtos = []string{"http/1.1"}
			tlsConn := tls.Client(conn, tlsCfg)
			if hsErr := tlsConn.HandshakeContext(ctx); hsErr != nil {
				_ = conn.Close()
				// Proxy TLS failure is on the proxy hop, independent of target IPs.
				return nil, hsErr
			}
			conn = tlsConn
		}

		connectTarget := net.JoinHostPort(ip.String(), port)
		connectReq := &http.Request{
			Method: http.MethodConnect,
			URL:    &url.URL{Opaque: connectTarget},
			Host:   connectTarget,
			Header: make(http.Header),
		}
		if proxyAuth != "" {
			connectReq.Header.Set("Proxy-Authorization", proxyAuth)
		}

		stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			_ = conn.SetDeadline(deadline)
		}
		if writeErr := connectReq.Write(conn); writeErr != nil {
			stopCancel()
			_ = conn.Close()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			lastErr = writeErr
			continue
		}
		br := bufio.NewReader(conn)
		resp, readErr := http.ReadResponse(br, connectReq)
		stopCancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = conn.Close()
			return nil, ctxErr
		}
		if readErr != nil {
			_ = conn.Close()
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			_ = conn.Close()
			lastErr = fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
			continue
		}
		_ = conn.SetDeadline(time.Time{})
		if br.Buffered() > 0 {
			return &usageAlertBufferedConn{Conn: conn, r: br}, nil
		}
		return conn, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no resolved addresses available")
}

func dialUsageAlertSOCKSProxyTunnel(
	ctx context.Context,
	baseDialer *net.Dialer,
	network, addr string,
	tunnel *usageAlertProxyTunnel,
) (net.Conn, error) {
	if tunnel == nil || tunnel.proxyURL == nil {
		return nil, errors.New("webhook proxy configuration is missing")
	}
	var auth *proxy.Auth
	if tunnel.proxyURL.User != nil {
		username := tunnel.proxyURL.User.Username()
		password, _ := tunnel.proxyURL.User.Password()
		auth = &proxy.Auth{User: username, Password: password}
	}
	proxyAddr := canonicalProxyDialAddr(tunnel.proxyURL)
	if proxyAddr == "" {
		return nil, errors.New("webhook proxy address is invalid")
	}
	socksDialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, baseDialer)
	if err != nil {
		return nil, fmt.Errorf("create socks5 proxy dialer: %w", err)
	}
	dialFn := socksDialer.Dial
	if ctxDialer, ok := socksDialer.(proxy.ContextDialer); ok {
		dialFn = func(n, a string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, n, a)
		}
	}
	if len(tunnel.ips) == 0 {
		return dialFn("tcp", addr)
	}
	return dialUsageAlertResolvedIPs(ctx, network, addr, func(_ context.Context, n, a string) (net.Conn, error) {
		return dialFn(n, a)
	}, tunnel.ips)
}

func canonicalProxyDialAddr(proxyURL *url.URL) string {
	if proxyURL == nil {
		return ""
	}
	host := proxyURL.Hostname()
	if host == "" {
		return ""
	}
	port := proxyURL.Port()
	if port == "" {
		switch strings.ToLower(proxyURL.Scheme) {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

func newSafeUsageAlertHTTPClient(timeout time.Duration) *http.Client {
	return newSafeUsageAlertHTTPClientWithHooks(timeout, http.ProxyFromEnvironment, resolveAllowedWebhookIPs)
}

func newSafeUsageAlertHTTPClientWithHooks(
	timeout time.Duration,
	proxyFunc func(*http.Request) (*url.URL, error),
	resolveFunc func(context.Context, string) ([]net.IP, error),
) *http.Client {
	if proxyFunc == nil {
		proxyFunc = http.ProxyFromEnvironment
	}
	if resolveFunc == nil {
		resolveFunc = resolveAllowedWebhookIPs
	}
	baseDialer := &net.Dialer{Timeout: 5 * time.Second}
	var baseTransport *http.Transport
	baseTransport = &http.Transport{
		DisableKeepAlives: true,
		Proxy: func(req *http.Request) (*url.URL, error) {
			if req != nil {
				if decision, ok := req.Context().Value(usageAlertProxyDecisionKey{}).(usageAlertProxyDecision); ok {
					return decision.transportProxyURL, nil
				}
			}
			return proxyFunc(req)
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if decision, ok := ctx.Value(usageAlertProxyDecisionKey{}).(usageAlertProxyDecision); ok {
				if decision.connectTunnel != nil {
					return dialUsageAlertHTTPProxyTunnel(ctx, baseDialer, baseTransport, network, addr, decision.connectTunnel)
				}
				if decision.socksTunnel != nil {
					return dialUsageAlertSOCKSProxyTunnel(ctx, baseDialer, network, addr, decision.socksTunnel)
				}
				if decision.directProxyDialAddr != "" && strings.EqualFold(addr, decision.directProxyDialAddr) {
					return baseDialer.DialContext(ctx, network, addr)
				}
			}
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid webhook dial target: %w", err)
			}
			ips, err := resolveFunc(ctx, host)
			if err != nil {
				return nil, err
			}
			return dialUsageAlertResolvedIPs(ctx, network, addr, baseDialer.DialContext, ips)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &safeUsageAlertTransport{
			base:        baseTransport,
			proxyFunc:   proxyFunc,
			resolveFunc: resolveFunc,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if req == nil || req.URL == nil {
				return errors.New("redirect target is missing")
			}
			if err := validateUsageAlertOutboundURL(req.URL, false); err != nil {
				return fmt.Errorf("redirect target is not allowed: %w", err)
			}
			return nil
		},
	}
}

func resolveAllowedWebhookIPs(ctx context.Context, host string) ([]net.IP, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, errors.New("webhook host is empty")
	}
	if parsed := net.ParseIP(trimmed); parsed != nil {
		if isDisallowedWebhookIP(parsed) {
			return nil, fmt.Errorf("webhook target ip %s is not allowed", parsed.String())
		}
		return []net.IP{parsed}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, trimmed)
	if err != nil {
		return nil, fmt.Errorf("dns resolution failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("dns resolution returned no addresses")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, item := range addrs {
		if isDisallowedWebhookIP(item.IP) {
			return nil, fmt.Errorf("resolved ip %s is not allowed", item.IP.String())
		}
		ips = append(ips, item.IP)
	}
	return ips, nil
}

var (
	disallowedWebhookPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
	}
	nat64WellKnownPrefix = netip.MustParsePrefix("64:ff9b::/96")
	ipv4CompatiblePrefix = netip.MustParsePrefix("::/96")
	sixToFourPrefix      = netip.MustParsePrefix("2002::/16")
)

func isDisallowedWebhookIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return isDisallowedWebhookAddr(addr)
}

func isDisallowedWebhookAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return true
	}
	for _, prefix := range disallowedWebhookPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	if addr.Is6() {
		b := addr.As16()
		if nat64WellKnownPrefix.Contains(addr) || ipv4CompatiblePrefix.Contains(addr) {
			return isDisallowedWebhookAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if sixToFourPrefix.Contains(addr) {
			return isDisallowedWebhookAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return false
}

func (s *UsageAlertService) SetOpenAICodexBoundaryRefresher(refresher OpenAICodexUsageSnapshotRefresher) {
	if s != nil {
		s.codexBoundaryRefresher = refresher
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
	keys := make([]string, 0, 2)
	if s != nil && s.repo != nil && rule.ID > 0 {
		existing, err := s.repo.GetRule(ctx, rule.ID)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.RealAccountID != nil && *existing.RealAccountID > 0 {
			keys = append(keys, usageAlertEvaluationLockKey(*existing.RealAccountID, existing.UsageType))
		}
	}
	if rule.RealAccountID != nil && *rule.RealAccountID > 0 {
		newKey := usageAlertEvaluationLockKey(*rule.RealAccountID, rule.UsageType)
		if len(keys) == 0 || keys[0] != newKey {
			keys = append(keys, newKey)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		lock := s.evaluationLockByKey(key)
		lock.Lock()
		defer lock.Unlock()
	}
	return s.repo.UpdateRule(ctx, rule)
}

func (s *UsageAlertService) DeleteRule(ctx context.Context, id int64) error {
	return s.repo.DeleteRule(ctx, id)
}

func (s *UsageAlertService) ListWebhooks(ctx context.Context) ([]*UsageAlertWebhook, error) {
	return s.repo.ListWebhooks(ctx)
}

func (s *UsageAlertService) CreateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error) {
	if err := s.validateWebhookTarget(webhook); err != nil {
		return nil, err
	}
	return s.repo.CreateWebhook(ctx, webhook)
}

func (s *UsageAlertService) UpdateWebhook(ctx context.Context, webhook *UsageAlertWebhook) (*UsageAlertWebhook, error) {
	if err := s.populatePreservedWebhookSecrets(ctx, webhook, true); err != nil {
		return nil, err
	}
	if err := s.validateWebhookTarget(webhook); err != nil {
		return nil, err
	}
	return s.repo.UpdateWebhook(ctx, webhook)
}

func (s *UsageAlertService) populatePreservedWebhookSecrets(ctx context.Context, webhook *UsageAlertWebhook, requireExisting bool) error {
	if s == nil || s.repo == nil || webhook == nil || webhook.ID <= 0 {
		return nil
	}
	if strings.TrimSpace(webhook.Type) != UsageAlertWebhookTypeTelegram {
		return nil
	}
	if strings.TrimSpace(usageAlertConfigString(webhook.Config, "bot_token")) != "" {
		return nil
	}
	existing, err := s.repo.GetWebhook(ctx, webhook.ID)
	if err != nil {
		if requireExisting {
			return err
		}
		return nil
	}
	if existing == nil || existing.Type != UsageAlertWebhookTypeTelegram {
		return nil
	}
	token := strings.TrimSpace(usageAlertConfigString(existing.Config, "bot_token"))
	if token == "" {
		return nil
	}
	cloned := make(map[string]any, len(webhook.Config)+1)
	for k, v := range webhook.Config {
		cloned[k] = v
	}
	cloned["bot_token"] = token
	webhook.Config = cloned
	return nil
}

// SanitizeUsageAlertWebhook returns a shallow copy with secret config fields
// removed so admin read APIs never echo raw Telegram bot tokens.
func SanitizeUsageAlertWebhook(webhook *UsageAlertWebhook) *UsageAlertWebhook {
	if webhook == nil {
		return nil
	}
	out := *webhook
	if len(webhook.Config) > 0 {
		cfg := make(map[string]any, len(webhook.Config))
		for k, v := range webhook.Config {
			if k == "bot_token" {
				cfg["has_bot_token"] = strings.TrimSpace(fmt.Sprint(v)) != "" && v != nil
				continue
			}
			cfg[k] = v
		}
		out.Config = cfg
	}
	return &out
}

// SanitizeUsageAlertBinding returns a binding copy with any nested webhook
// secret fields stripped.
func SanitizeUsageAlertBinding(binding *UsageAlertBinding) *UsageAlertBinding {
	if binding == nil {
		return nil
	}
	out := *binding
	out.Webhook = SanitizeUsageAlertWebhook(binding.Webhook)
	return &out
}

// UsageAlertRuleSemanticsChanged reports whether updating a rule alters its scope
// or evaluation semantics so persisted trigger states should be reset.
func UsageAlertRuleSemanticsChanged(before, after *UsageAlertRule) bool {
	if before == nil || after == nil {
		return true
	}
	if strings.TrimSpace(before.Platform) != strings.TrimSpace(after.Platform) ||
		!int64PtrEqual(before.RealAccountID, after.RealAccountID) ||
		normalizeUsageAlertUsageType(before.UsageType) != normalizeUsageAlertUsageType(after.UsageType) ||
		strings.TrimSpace(before.Window) != strings.TrimSpace(after.Window) ||
		strings.TrimSpace(before.Metric) != strings.TrimSpace(after.Metric) ||
		strings.TrimSpace(before.Operator) != strings.TrimSpace(after.Operator) ||
		before.Threshold != after.Threshold {
		return true
	}
	return !float64PtrEqual(before.StepPercent, after.StepPercent) ||
		!float64PtrEqual(before.MinResetAfterHours, after.MinResetAfterHours)
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func float64PtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
	if err := s.populatePreservedWebhookSecrets(ctx, webhook, false); err != nil {
		return err
	}
	if err := s.validateWebhookTarget(webhook); err != nil {
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
	if s == nil || s.repo == nil || s.stopped.Load() || snapshot == nil || snapshot.AccountID <= 0 || len(snapshot.Windows) == 0 {
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

	s.enqueueObservation(*snapshot)
}

func (s *UsageAlertService) enqueueObservation(snapshot UsageAlertSnapshot) {
	if s.stopped.Load() {
		return
	}
	key := fmt.Sprintf("%d:%s", snapshot.RealAccountID, normalizeUsageAlertUsageType(snapshot.UsageType))
	actual, _ := s.workers.LoadOrStore(key, &usageAlertObservationWorker{wake: make(chan struct{}, 1)})
	worker, ok := actual.(*usageAlertObservationWorker)
	if !ok {
		return
	}

	worker.mu.Lock()
	switch usageAlertObservationPriority(snapshot) {
	case 2:
		worker.manualPending = mergePendingUsageAlertSnapshot(worker.manualPending, snapshot)
	case 1:
		worker.authorityPending = mergePendingUsageAlertSnapshot(worker.authorityPending, snapshot)
	default:
		worker.pending = mergePendingUsageAlertSnapshot(worker.pending, snapshot)
	}
	if worker.running {
		select {
		case worker.wake <- struct{}{}:
		default:
		}
		worker.mu.Unlock()
		return
	}
	if !s.tryAddWorker() {
		worker.mu.Unlock()
		return
	}
	worker.running = true
	worker.mu.Unlock()

	go s.runObservationWorker(worker)
}

func cloneUsageAlertSnapshot(snapshot UsageAlertSnapshot) UsageAlertSnapshot {
	cloned := snapshot
	if len(snapshot.Windows) > 0 {
		cloned.Windows = make(map[string]UsageAlertWindowSnapshot, len(snapshot.Windows))
		for k, v := range snapshot.Windows {
			cloned.Windows[k] = v
		}
	}
	return cloned
}

func mergePendingUsageAlertSnapshot(existing *UsageAlertSnapshot, incoming UsageAlertSnapshot) *UsageAlertSnapshot {
	cloned := cloneUsageAlertSnapshot(incoming)
	if existing == nil {
		return &cloned
	}
	mergedWindows := make(map[string]UsageAlertWindowSnapshot, len(existing.Windows)+len(cloned.Windows))
	for window, value := range existing.Windows {
		if value.SampledAt == nil && !existing.SampledAt.IsZero() {
			sampledAt := existing.SampledAt.UTC()
			value.SampledAt = &sampledAt
		}
		if value.Source == "" {
			value.Source = existing.Source
		}
		if value.AccountID <= 0 {
			value.AccountID = existing.AccountID
		}
		mergedWindows[window] = value
	}
	for window, value := range cloned.Windows {
		if value.SampledAt == nil && !cloned.SampledAt.IsZero() {
			sampledAt := cloned.SampledAt.UTC()
			value.SampledAt = &sampledAt
		}
		if value.Source == "" {
			value.Source = cloned.Source
		}
		if value.AccountID <= 0 {
			value.AccountID = cloned.AccountID
		}
		prev, exists := mergedWindows[window]
		if !exists || prev.SampledAt == nil || value.SampledAt == nil || !value.SampledAt.Before(*prev.SampledAt) {
			mergedWindows[window] = value
		}
	}
	out := cloned
	if !existing.SampledAt.IsZero() && existing.SampledAt.After(out.SampledAt) {
		out.SampledAt = existing.SampledAt
	}
	out.Windows = mergedWindows
	return &out
}

func (s *UsageAlertService) runObservationWorker(worker *usageAlertObservationWorker) {
	defer s.wg.Done()
	stopSig := s.stopSignal()
	for {
		worker.mu.Lock()
		var pending *UsageAlertSnapshot
		if !s.stopped.Load() && worker.retryPending != nil {
			pending = worker.retryPending
			worker.retryPending = nil
		} else if worker.manualPending != nil {
			pending = worker.manualPending
			worker.manualPending = nil
		} else if worker.authorityPending != nil {
			pending = worker.authorityPending
			worker.authorityPending = nil
		} else {
			pending = worker.pending
			worker.pending = nil
		}
		if pending == nil {
			worker.running = false
			worker.waiting = false
			worker.mu.Unlock()
			return
		}
		worker.mu.Unlock()
		retry := s.observeAsync(*pending)
		worker.mu.Lock()
		if !retry || s.stopped.Load() {
			worker.retryDelay = 0
			select {
			case <-worker.wake:
			default:
			}
			worker.mu.Unlock()
			continue
		}
		retrySnapshot := cloneUsageAlertSnapshot(*pending)
		worker.retryPending = &retrySnapshot
		if worker.retryDelay <= 0 {
			worker.retryDelay = usageAlertRetryMinDelay
		} else {
			worker.retryDelay = min(worker.retryDelay*2, usageAlertRetryMaxDelay)
		}
		delay := worker.retryDelay
		worker.waiting = true
		worker.mu.Unlock()

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-worker.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-stopSig:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		worker.mu.Lock()
		worker.waiting = false
		worker.mu.Unlock()
	}
}

func usageAlertObservationPriority(snapshot UsageAlertSnapshot) int {
	if !isCodexOverall7dWindow(snapshot, UsageAlertWindow7d) {
		return 0
	}
	if _, ok := snapshot.Windows[UsageAlertWindow7d]; !ok {
		return 0
	}
	switch snapshot.Source {
	case UsageAlertSourceOpenAIQuotaReset:
		return 2
	case UsageAlertSourceOpenAICodexUsageAPI:
		return 1
	default:
		return 0
	}
}

type shadowRealAccountResolver interface {
	ResolveShadowRealAccount(ctx context.Context, shadowAccountID, parentAccountID int64) (*RealAccount, error)
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
		if resolver, ok := s.repo.(shadowRealAccountResolver); ok {
			realAccount, resolveErr := resolver.ResolveShadowRealAccount(ctx, account.ID, *account.ParentAccountID)
			if resolveErr != nil || realAccount == nil {
				slog.Warn("usage_alert_resolve_shadow_real_account_failed", "account_id", accountID, "parent_account_id", *account.ParentAccountID, "error", resolveErr)
				return 0, usageType
			}
			return realAccount.ID, usageType
		}
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

func (s *UsageAlertService) observeAsync(snapshot UsageAlertSnapshot) bool {
	evaluationLock := s.evaluationLock(snapshot.RealAccountID, snapshot.UsageType)
	evaluationLock.Lock()
	defer evaluationLock.Unlock()

	ctx, cancel := context.WithTimeout(s.baseContext(), 3*time.Minute)
	defer cancel()

	var previous *UsageAlertSnapshot
	var evaluationSnapshot UsageAlertSnapshot
	refreshCodex7d := false
	accepted := false
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		previous, err = s.repo.GetSnapshot(ctx, snapshot.RealAccountID, snapshot.UsageType)
		if err != nil {
			slog.Warn("usage_alert_get_snapshot_failed", "real_account_id", snapshot.RealAccountID, "error", err)
			return true
		}
		var persistedSnapshot UsageAlertSnapshot
		var hasFreshWindows, needsCodex7dRefresh bool
		evaluationSnapshot, persistedSnapshot, hasFreshWindows, needsCodex7dRefresh = prepareUsageAlertSnapshot(previous, snapshot)
		refreshCodex7d = refreshCodex7d || needsCodex7dRefresh
		if !hasFreshWindows {
			s.requestCodex7dBoundaryRefresh(ctx, snapshot, refreshCodex7d)
			return false
		}
		accepted, err = s.repo.UpsertSnapshot(ctx, &persistedSnapshot)
		if err != nil {
			slog.Warn("usage_alert_upsert_snapshot_failed", "real_account_id", snapshot.RealAccountID, "error", err)
			return true
		}
		if accepted {
			break
		}
	}
	if !accepted {
		s.requestCodex7dBoundaryRefresh(ctx, snapshot, refreshCodex7d)
		return false
	}
	s.requestCodex7dBoundaryRefresh(ctx, snapshot, refreshCodex7d)

	rules, err := s.repo.ListEnabledRules(ctx, snapshot.RealAccountID, snapshot.UsageType)
	if err != nil {
		slog.Warn("usage_alert_list_rules_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return true
	}
	if len(rules) == 0 {
		return false
	}

	triggers, err := s.evaluateRules(ctx, previous, &evaluationSnapshot, rules)
	if err != nil {
		slog.Warn("usage_alert_evaluate_rules_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return true
	}
	if len(triggers) == 0 {
		return false
	}
	webhooks, err := s.repo.ListEnabledWebhooksForRealAccount(ctx, snapshot.RealAccountID)
	if err != nil {
		slog.Warn("usage_alert_list_webhooks_failed", "real_account_id", snapshot.RealAccountID, "error", err)
		return true
	}
	if len(webhooks) == 0 {
		slog.Info("usage_alert_trigger_without_webhook", "real_account_id", snapshot.RealAccountID, "trigger_count", len(triggers))
		for _, trigger := range finalUsageAlertStateTriggers(triggers) {
			if err := s.commitUsageAlertTrigger(trigger, snapshot.RealAccountID); err != nil {
				slog.Warn("usage_alert_commit_state_failed", "real_account_id", snapshot.RealAccountID, "rule_id", trigger.Rule.ID, "error", err)
				return true
			}
		}
		return false
	}
	realAccount, _ := s.repo.GetRealAccount(ctx, snapshot.RealAccountID)
	deliveredEvents := make(map[string]struct{}, len(triggers))
	for _, trigger := range triggers {
		event := buildUsageAlertWebhookEvent(&evaluationSnapshot, realAccount, trigger)
		if _, delivered := deliveredEvents[event.EventID]; delivered {
			continue
		}
		deliveredEvents[event.EventID] = struct{}{}
		deliveryRuleID := trigger.Rule.ID
		if trigger.AccountReset {
			deliveryRuleID = 0
		}
		if !s.deliverUsageAlertEvent(ctx, snapshot.RealAccountID, deliveryRuleID, webhooks, event) {
			return true
		}
	}
	for _, trigger := range finalUsageAlertStateTriggers(triggers) {
		if err := s.commitUsageAlertTrigger(trigger, snapshot.RealAccountID); err != nil {
			slog.Warn("usage_alert_commit_state_failed", "real_account_id", snapshot.RealAccountID, "rule_id", trigger.Rule.ID, "error", err)
			return true
		}
	}
	return false
}

func finalUsageAlertStateTriggers(triggers []UsageAlertTrigger) []UsageAlertTrigger {
	finalByRule := make(map[int64]UsageAlertTrigger, len(triggers))
	order := make([]int64, 0, len(triggers))
	for _, trigger := range triggers {
		if trigger.Rule == nil {
			continue
		}
		if _, ok := finalByRule[trigger.Rule.ID]; !ok {
			order = append(order, trigger.Rule.ID)
		}
		finalByRule[trigger.Rule.ID] = trigger
	}
	final := make([]UsageAlertTrigger, 0, len(order))
	for _, ruleID := range order {
		final = append(final, finalByRule[ruleID])
	}
	return final
}

func (s *UsageAlertService) requestCodex7dBoundaryRefresh(ctx context.Context, snapshot UsageAlertSnapshot, required bool) {
	if !required || s.codexBoundaryRefresher == nil {
		return
	}
	accountIDs := []int64{snapshot.AccountID}
	if realAccount, err := s.repo.GetRealAccount(ctx, snapshot.RealAccountID); err == nil && realAccount != nil {
		for _, account := range realAccount.Accounts {
			if account == nil || account.ID <= 0 || account.ID == snapshot.AccountID || account.Platform != PlatformOpenAI || account.IsShadow() {
				continue
			}
			accountIDs = append(accountIDs, account.ID)
		}
	}
	for _, accountID := range accountIDs {
		if s.allowCodex7dBoundaryRefresh(accountID, time.Now().UTC()) {
			s.codexBoundaryRefresher.RefreshOpenAICodexUsageSnapshot(accountID, true)
		}
	}
}

func (s *UsageAlertService) allowCodex7dBoundaryRefresh(accountID int64, now time.Time) bool {
	if accountID <= 0 {
		return false
	}
	s.codexBoundaryRefreshMu.Lock()
	defer s.codexBoundaryRefreshMu.Unlock()
	if last := s.codexBoundaryRefreshAt[accountID]; !last.IsZero() && now.Sub(last) < codex7dBoundaryRefreshDelay {
		return false
	}
	if s.codexBoundaryRefreshAt == nil {
		s.codexBoundaryRefreshAt = make(map[int64]time.Time)
	}
	s.codexBoundaryRefreshAt[accountID] = now
	return true
}

func (s *UsageAlertService) deliverUsageAlertEvent(ctx context.Context, realAccountID, ruleID int64, webhooks []*UsageAlertWebhook, event UsageAlertWebhookEvent) bool {
	s.maybeCleanupWebhookDeliveries()
	for _, webhook := range webhooks {
		if webhook == nil {
			continue
		}
		claimToken, err := newUsageAlertClaimToken()
		if err != nil {
			slog.Warn("usage_alert_claim_token_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "error", err)
			return false
		}
		claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		claim, err := s.repo.ClaimWebhookDelivery(claimCtx, event.EventID, realAccountID, ruleID, webhook.ID, claimToken, usageAlertDeliveryLease)
		cancelClaim()
		if err != nil {
			slog.Warn("usage_alert_webhook_claim_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "error", err)
			return false
		}
		switch claim {
		case UsageAlertDeliveryAlreadyDelivered:
			continue
		case UsageAlertDeliveryBusy:
			// Stop before claiming later endpoints. This ensures one instance
			// owns the remaining delivery sequence instead of splitting it
			// across instances and leaving neither able to commit state.
			return false
		}

		if err := s.deliverWebhookWithRetry(ctx, *webhook, event); err != nil {
			releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			releaseErr := s.repo.ReleaseWebhookDelivery(releaseCtx, event.EventID, webhook.ID, claimToken)
			cancelRelease()
			if releaseErr != nil {
				slog.Warn("usage_alert_webhook_release_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "error", releaseErr)
			}
			slog.Warn("usage_alert_webhook_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "attempts", webhook.RetryCount+1, "error", err)
			return false
		}
		completeCtx, cancelComplete := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = s.repo.CompleteWebhookDelivery(completeCtx, event.EventID, webhook.ID, claimToken)
		cancelComplete()
		if err != nil {
			slog.Warn("usage_alert_webhook_complete_failed", "webhook_id", webhook.ID, "event_id", event.EventID, "error", err)
			return false
		}
	}
	return true
}

func (s *UsageAlertService) maybeCleanupWebhookDeliveries() {
	cleaner, ok := s.repo.(usageAlertDeliveryCleaner)
	if !ok || s.stopped.Load() {
		return
	}
	now := time.Now().UTC()
	s.cleanupMu.Lock()
	if s.cleanupRunning || (!s.lastCleanupAt.IsZero() && now.Sub(s.lastCleanupAt) < usageAlertCleanupInterval) {
		s.cleanupMu.Unlock()
		return
	}
	s.cleanupRunning = true
	s.cleanupMu.Unlock()

	if !s.tryAddWorker() {
		s.cleanupMu.Lock()
		s.cleanupRunning = false
		s.cleanupMu.Unlock()
		return
	}
	go func() {
		defer s.wg.Done()
		cleanupCtx, cancel := context.WithTimeout(s.baseContext(), 30*time.Second)
		err := cleaner.CleanupWebhookDeliveries(cleanupCtx, now.Add(-usageAlertDeliveryRetention))
		cancel()
		s.cleanupMu.Lock()
		s.cleanupRunning = false
		if err == nil {
			s.lastCleanupAt = now
		}
		s.cleanupMu.Unlock()
		if err != nil {
			slog.Warn("usage_alert_delivery_cleanup_failed", "error", err)
		}
	}()
}

func newUsageAlertClaimToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", token[:]), nil
}

func usageAlertEvaluationLockKey(realAccountID int64, usageType string) string {
	return fmt.Sprintf("%d:%s", realAccountID, normalizeUsageAlertUsageType(usageType))
}

func (s *UsageAlertService) evaluationLockByKey(key string) *sync.Mutex {
	actual, _ := s.evaluationLocks.LoadOrStore(key, &sync.Mutex{})
	lock, ok := actual.(*sync.Mutex)
	if !ok {
		lock = &sync.Mutex{}
		s.evaluationLocks.Store(key, lock)
	}
	return lock
}

func (s *UsageAlertService) evaluationLock(realAccountID int64, usageType string) *sync.Mutex {
	return s.evaluationLockByKey(usageAlertEvaluationLockKey(realAccountID, usageType))
}

// prepareUsageAlertSnapshot separates windows that are safe to evaluate from
// the merged snapshot persisted for later comparisons. A sample collected later
// must not move a linked real account back to an older quota window.
func prepareUsageAlertSnapshot(previous *UsageAlertSnapshot, current UsageAlertSnapshot) (UsageAlertSnapshot, UsageAlertSnapshot, bool, bool) {
	freshWindows := make(map[string]UsageAlertWindowSnapshot, len(current.Windows))
	persistedWindows := make(map[string]UsageAlertWindowSnapshot, len(current.Windows))
	if previous != nil {
		for window, value := range previous.Windows {
			persistedWindows[window] = normalizePersistedUsageAlertWindow(previous, window, value)
		}
	}

	refreshCodex7d := false
	for window, value := range current.Windows {
		if value.SampledAt == nil && !current.SampledAt.IsZero() {
			value.SampledAt = usageAlertTimePtr(current.SampledAt)
		} else if value.SampledAt != nil {
			value.SampledAt = usageAlertTimePtr(*value.SampledAt)
		}
		windowSampledAt := current.SampledAt
		if value.SampledAt != nil && !value.SampledAt.IsZero() {
			windowSampledAt = *value.SampledAt
		}
		windowSource := current.Source
		if value.Source != "" {
			windowSource = value.Source
		}
		initialized := false
		if previous != nil {
			if previousValue, ok := persistedWindows[window]; ok {
				var accepted bool
				if isCodexOverall7dWindow(current, window) {
					value, accepted, refreshCodex7d = prepareCodex7dWindow(previousValue, value, windowSource, windowSampledAt)
				} else {
					value, accepted = prepareResetOrderedWindow(previousValue, value)
				}
				if !accepted {
					continue
				}
			} else {
				value = initializeUsageAlertWindow(current, window, windowSource, value)
				initialized = true
			}
		}
		if previous == nil && !initialized {
			value = initializeUsageAlertWindow(current, window, windowSource, value)
		}
		freshWindows[window] = value
		persistedWindows[window] = value
	}
	if len(freshWindows) == 0 {
		return UsageAlertSnapshot{}, UsageAlertSnapshot{}, false, refreshCodex7d
	}

	evaluation := current
	evaluation.Windows = freshWindows
	persisted := current
	persisted.Windows = persistedWindows
	return evaluation, persisted, true, refreshCodex7d
}

func initializeUsageAlertWindow(snapshot UsageAlertSnapshot, window, source string, value UsageAlertWindowSnapshot) UsageAlertWindowSnapshot {
	value.Generation = 1
	if isCodexOverall7dWindow(snapshot, window) {
		value.BoundaryAt = value.ResetAt
		value.AwaitingOfficialReset = source == UsageAlertSourceOpenAIQuotaReset
	}
	return value
}

func normalizePersistedUsageAlertWindow(snapshot *UsageAlertSnapshot, window string, value UsageAlertWindowSnapshot) UsageAlertWindowSnapshot {
	if value.Generation <= 0 {
		value.Generation = 1
	}
	if value.SampledAt == nil && snapshot != nil && !snapshot.SampledAt.IsZero() {
		value.SampledAt = usageAlertTimePtr(snapshot.SampledAt)
	}
	if snapshot != nil && isCodexOverall7dWindow(*snapshot, window) && value.BoundaryAt == nil {
		value.BoundaryAt = value.ResetAt
	}
	return value
}

func prepareResetOrderedWindow(previous, current UsageAlertWindowSnapshot) (UsageAlertWindowSnapshot, bool) {
	// APIs can include fractional seconds; headers and Codex countdowns are
	// rounded to seconds. Keep the anchor across that one-second precision
	// difference so fresh usage is accepted without starting a new cycle.
	if previous.ResetAt != nil && current.ResetAt != nil {
		delta := current.ResetAt.Sub(*previous.ResetAt)
		if delta >= -time.Second && delta <= time.Second {
			current.ResetAt = previous.ResetAt
		}
	}
	if usageAlertWindowGenerationIsOlder(previous, current) {
		return UsageAlertWindowSnapshot{}, false
	}
	current.Generation = previous.Generation
	if current.Generation <= 0 {
		current.Generation = 1
	}
	if usageAlertWindowGenerationIsNewer(previous.ResetAt, current.ResetAt) {
		current.Generation++
	}
	if current.Generation == previous.Generation && usageAlertWindowSampleIsOlder(previous, current) {
		return UsageAlertWindowSnapshot{}, false
	}
	return current, true
}

func prepareCodex7dWindow(previous, current UsageAlertWindowSnapshot, source string, sampledAt time.Time) (UsageAlertWindowSnapshot, bool, bool) {
	current.Generation = previous.Generation
	if current.Generation <= 0 {
		current.Generation = 1
	}
	current.BoundaryAt = previous.BoundaryAt
	if current.BoundaryAt == nil {
		current.BoundaryAt = previous.ResetAt
	}

	if source == UsageAlertSourceOpenAIQuotaReset {
		if usageAlertWindowSampleIsOlder(previous, current) {
			return UsageAlertWindowSnapshot{}, false, false
		}
		if !codex7dSamePendingReset(previous, current) {
			current.Generation++
		}
		if current.ResetAt != nil {
			current.BoundaryAt = current.ResetAt
		}
		current.OfficialResetAnchorAt = current.ResetAt
		current.AwaitingOfficialReset = true
		// The boundary now sits a full cycle out and header samples are rejected
		// while awaiting the official reset, so nothing else would pull an
		// authoritative sample before then. Force the refresh that resolves the
		// awaiting state instead of going dark until the manual horizon expires.
		return current, true, true
	}
	if usageAlertWindowSampleIsOlder(previous, current) {
		return UsageAlertWindowSnapshot{}, false, false
	}
	if previous.AwaitingOfficialReset && source == UsageAlertSourceOpenAICodexUsageAPI {
		if current.ResetAt == nil || !current.ResetAt.After(sampledAt) {
			return UsageAlertWindowSnapshot{}, false, true
		}
		anchor := codex7dOfficialResetAnchor(previous)
		if anchor != nil && current.ResetAt.Before(anchor.Add(-codex7dResetDriftWindow)) {
			return UsageAlertWindowSnapshot{}, false, true
		}
		if anchor != nil && !codex7dResetTimesWithinWindow(anchor, current.ResetAt, codex7dResetDriftWindow) &&
			codex7dUsageRolledBack(previous, current) {
			return advanceCodex7dGeneration(previous, current), true, false
		}
		return reconcileCodex7dAuthority(current), true, false
	}

	if source == UsageAlertSourceOpenAICodexUsageAPI && current.ResetAt != nil && current.ResetAt.After(sampledAt) {
		if codex7dUsageRolledBack(previous, current) {
			return advanceCodex7dGeneration(previous, current), true, false
		}
		return reconcileCodex7dAuthority(current), true, false
	}

	boundaryDue := current.BoundaryAt != nil && !sampledAt.Before(*current.BoundaryAt)
	if boundaryDue {
		return UsageAlertWindowSnapshot{}, false, true
	}

	// Authoritative samples were all handled above, so anything still awaiting the
	// official reset here is a header or probe sample. Drop it, but keep asking
	// for an authoritative one: the manual branch only gets a single best-effort
	// refresh, and /wham/usage can lose it to a throttle, a transient upstream
	// failure, or a redemption the upstream has not applied yet. Without this the
	// account stays dark until the manual horizon expires a cycle later. Retries
	// are bounded by the 30s refresh throttle and stop at the first accepted
	// authoritative sample.
	if previous.AwaitingOfficialReset {
		return UsageAlertWindowSnapshot{}, false, true
	}
	if current.ResetAt != nil && !current.ResetAt.After(sampledAt) {
		return UsageAlertWindowSnapshot{}, false, false
	}
	current.AwaitingOfficialReset = false
	return current, true, false
}

func codex7dSamePendingReset(previous, current UsageAlertWindowSnapshot) bool {
	if !previous.AwaitingOfficialReset {
		return false
	}
	anchor := codex7dOfficialResetAnchor(previous)
	if anchor == nil || current.ResetAt == nil {
		return true
	}
	return codex7dResetTimesWithinWindow(anchor, current.ResetAt, codex7dResetDriftWindow)
}

func codex7dOfficialResetAnchor(window UsageAlertWindowSnapshot) *time.Time {
	if window.OfficialResetAnchorAt != nil {
		return window.OfficialResetAnchorAt
	}
	return window.ResetAt
}

// codex7dUsageRolledBack compares against the most recent accepted sample of any
// source rather than the last authoritative one. Accounts without websocket
// traffic only reach the Usage API once per cycle, at the boundary refresh, so
// an authority-only baseline would go a full week stale and end up comparing one
// post-reset reading against the next — a difference of roughly zero that never
// clears the threshold. Headers track usage as it accrues, and a header running
// slightly ahead of the authority at most costs one spurious generation bump,
// which re-arms alerting; a missed bump silently disables it.
func codex7dUsageRolledBack(previous, current UsageAlertWindowSnapshot) bool {
	return previous.UsedPercent-current.UsedPercent > codex7dUsageRollbackPoints
}

// reconcileCodex7dAuthority accepts an authoritative sample: the boundary tracks
// the reported horizon again and any pending official reset is settled.
func reconcileCodex7dAuthority(current UsageAlertWindowSnapshot) UsageAlertWindowSnapshot {
	current.BoundaryAt = current.ResetAt
	current.OfficialResetAnchorAt = nil
	current.AwaitingOfficialReset = false
	return current
}

// advanceCodex7dGeneration logs the evidence it acted on. The baseline is the
// last accepted sample, which may come from headers rather than the Usage API,
// so a small drop paired with a horizon that barely moved would mean the two
// sources disagree instead of a real reset. That case is not expected, but it is
// only distinguishable from the field.
func advanceCodex7dGeneration(previous, current UsageAlertWindowSnapshot) UsageAlertWindowSnapshot {
	current.Generation++
	attrs := []any{
		"generation", current.Generation,
		"used_percent_drop", previous.UsedPercent - current.UsedPercent,
	}
	if previous.BoundaryAt != nil && current.ResetAt != nil {
		attrs = append(attrs, "horizon_shift_seconds", int64(current.ResetAt.Sub(*previous.BoundaryAt).Seconds()))
	}
	slog.Info("codex_7d_generation_advanced", attrs...)
	return reconcileCodex7dAuthority(current)
}

func codex7dResetTimesWithinWindow(anchor, current *time.Time, window time.Duration) bool {
	if anchor == nil || current == nil {
		return false
	}
	delta := current.Sub(*anchor)
	return delta >= -window && delta <= window
}

func usageAlertWindowSampleIsOlder(previous, current UsageAlertWindowSnapshot) bool {
	return previous.SampledAt != nil && current.SampledAt != nil && current.SampledAt.Before(*previous.SampledAt)
}

func isCodexOverall7dWindow(snapshot UsageAlertSnapshot, window string) bool {
	return snapshot.Platform == UsageAlertPlatformOpenAI &&
		normalizeUsageAlertUsageType(snapshot.UsageType) == UsageAlertTypeOverall &&
		window == UsageAlertWindow7d
}

func usageAlertTimePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func usageAlertWindowGenerationIsOlder(previous, current UsageAlertWindowSnapshot) bool {
	if previous.ResetAt == nil {
		return false
	}
	if current.ResetAt == nil {
		return true
	}
	return current.ResetAt.Before(*previous.ResetAt)
}

func (s *UsageAlertService) evaluateRules(ctx context.Context, previous, current *UsageAlertSnapshot, rules []*UsageAlertRule) ([]UsageAlertTrigger, error) {
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
		state, err := s.repo.GetState(ctx, current.RealAccountID, rule.ID, current.UsageType, rule.Window)
		if err != nil {
			return nil, fmt.Errorf("get state for rule %d: %w", rule.ID, err)
		}
		if state != nil && state.LastGeneration > 0 && window.Generation > 0 && state.LastGeneration > window.Generation {
			continue
		}
		if state != nil && state.LastGeneration == 0 && !isCodexOverall7dWindow(*current, rule.Window) && usageAlertWindowGenerationIsOlder(
			UsageAlertWindowSnapshot{ResetAt: state.LastResetAt}, window,
		) {
			continue
		}
		stateAnchor := usageAlertStateAnchor(state)
		value := metricValue(window, rule.Metric)
		previousWindow, hasPreviousWindow := UsageAlertWindowSnapshot{}, false
		if previous != nil {
			previousWindow, hasPreviousWindow = previous.Windows[rule.Window]
		}
		newGeneration := state != nil && usageAlertWindowStateGenerationIsNewer(
			state,
			window,
			previousWindow,
			hasPreviousWindow,
			isCodexOverall7dWindow(*current, rule.Window),
		)
		accountReset := newGeneration && window.Generation > 0 && isCodexOverall7dRule(rule)
		if accountReset {
			triggers = append(triggers, usageAlertAccountResetTrigger(rule, window, value, now))
		}
		if !resetConstraintSatisfied(window, rule.MinResetAfterHours, now) {
			if accountReset {
				continue
			}
			if usageAlertStateWasTriggered(state) {
				triggers = append(triggers, usageAlertResolvedTrigger(rule, window, value, now, stateAnchor))
				continue
			}
			if err := s.updateRuleState(ctx, current.RealAccountID, rule, window, false, value); err != nil {
				return nil, err
			}
			continue
		}
		matched := compareUsageAlertValue(value, rule.Operator, rule.Threshold)
		if !matched {
			if accountReset {
				continue
			}
			if usageAlertStateWasTriggered(state) {
				triggers = append(triggers, usageAlertResolvedTrigger(rule, window, value, now, stateAnchor))
				continue
			}
			if err := s.updateRuleState(ctx, current.RealAccountID, rule, window, false, value); err != nil {
				return nil, err
			}
			continue
		}
		stateForTrigger := state
		if newGeneration {
			stateForTrigger = nil
			stateAnchor = "new-generation"
		}
		if !usageAlertRuleAllowsTrigger(previous, current, rule, stateForTrigger, value, now) {
			continue
		}
		triggers = append(triggers, UsageAlertTrigger{
			Rule:        rule,
			Window:      rule.Window,
			Value:       value,
			WindowState: window,
			TriggeredAt: now,
			StateAnchor: stateAnchor,
		})
	}
	return triggers, nil
}

func usageAlertWindowStateGenerationIsNewer(
	state *UsageAlertState,
	window UsageAlertWindowSnapshot,
	previousWindow UsageAlertWindowSnapshot,
	hasPreviousWindow bool,
	codex7d bool,
) bool {
	if state == nil {
		return false
	}
	if state.LastGeneration > 0 && window.Generation > 0 {
		return window.Generation > state.LastGeneration
	}
	if codex7d && state.LastGeneration == 0 && window.Generation > 0 {
		if state.LastResetAt != nil && window.ResetAt != nil {
			return window.ResetAt.After(state.LastResetAt.Add(codex7dResetDriftWindow))
		}
		return window.Generation > 1
	}
	if codex7d && hasPreviousWindow && window.Generation > 0 {
		previousGeneration := previousWindow.Generation
		if previousGeneration <= 0 {
			previousGeneration = 1
		}
		return window.Generation > previousGeneration
	}
	return usageAlertWindowGenerationIsNewer(state.LastResetAt, window.ResetAt)
}

func (s *UsageAlertService) commitUsageAlertTrigger(trigger UsageAlertTrigger, realAccountID int64) error {
	if trigger.Rule == nil || realAccountID <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.updateRuleState(ctx, realAccountID, trigger.Rule, trigger.WindowState, !trigger.Resolved, trigger.Value)
}

func usageAlertRuleAllowsTrigger(previous, current *UsageAlertSnapshot, rule *UsageAlertRule, state *UsageAlertState, value float64, now time.Time) bool {
	if usageAlertRuleStepPercent(rule) > 0 {
		return usageAlertStepAllowsTrigger(state, rule, value, now)
	}
	// Snapshots are persisted before delivery. After a failed send they no
	// longer show the crossing, but the rule remains normal until delivery
	// succeeds. Keep that transition eligible for retry.
	if state == nil || state.LastStatus != UsageAlertStatusTriggered {
		return true
	}
	if crossedThreshold(previous, current, rule, value) {
		return true
	}
	if rule.CooldownMinutes <= 0 {
		return false
	}
	return stateAllowsRepeat(state, rule, now)
}

func usageAlertStateWasTriggered(state *UsageAlertState) bool {
	return state != nil && state.LastStatus == UsageAlertStatusTriggered
}

func usageAlertResolvedTrigger(rule *UsageAlertRule, window UsageAlertWindowSnapshot, value float64, now time.Time, stateAnchor string) UsageAlertTrigger {
	return UsageAlertTrigger{
		Rule:        rule,
		Window:      rule.Window,
		Value:       value,
		WindowState: window,
		TriggeredAt: now,
		Resolved:    true,
		StateAnchor: stateAnchor,
	}
}

func usageAlertAccountResetTrigger(rule *UsageAlertRule, window UsageAlertWindowSnapshot, value float64, now time.Time) UsageAlertTrigger {
	return UsageAlertTrigger{
		Rule:         rule,
		Window:       rule.Window,
		Value:        value,
		WindowState:  window,
		TriggeredAt:  now,
		Resolved:     true,
		AccountReset: true,
		StateAnchor:  "new-generation",
	}
}

func usageAlertWindowGenerationIsNewer(previous, current *time.Time) bool {
	return previous != nil && current != nil && current.After(*previous)
}

func usageAlertStateAnchor(state *UsageAlertState) string {
	if state == nil {
		return "none"
	}
	triggeredAt := int64(0)
	if state.LastTriggeredAt != nil {
		triggeredAt = state.LastTriggeredAt.UTC().UnixNano()
	}
	lastValue := "nil"
	if state.LastValue != nil {
		lastValue = strconv.FormatFloat(*state.LastValue, 'g', -1, 64)
	}
	resetAt := int64(0)
	if state.LastResetAt != nil {
		resetAt = state.LastResetAt.UTC().UnixNano()
	}
	return fmt.Sprintf("%s:%d:%s:%d:%d", state.LastStatus, triggeredAt, lastValue, resetAt, state.LastGeneration)
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
	if rule.Platform != "" && rule.Platform != UsageAlertPlatformAll && rule.Platform != realAccount.Platform {
		return fmt.Errorf("rule platform %q does not match real account platform %q", rule.Platform, realAccount.Platform)
	}
	rule.Platform = realAccount.Platform
	rule.UsageType = normalizeUsageAlertUsageType(rule.UsageType)
	if !usageAlertTypeSupportsRule(realAccount.Platform, rule.UsageType, rule.Window) {
		return fmt.Errorf("usage type %q does not support %s %s alerts", rule.UsageType, realAccount.Platform, rule.Window)
	}
	rule.RealAccount = realAccount
	return nil
}

func (s *UsageAlertService) updateRuleState(ctx context.Context, realAccountID int64, rule *UsageAlertRule, window UsageAlertWindowSnapshot, triggered bool, value float64) error {
	status := UsageAlertStatusNormal
	var triggeredAt *time.Time
	if triggered {
		status = UsageAlertStatusTriggered
		now := time.Now().UTC()
		triggeredAt = &now
		// Step rules persist the fixed threshold level that was reached, not
		// the incidental sampled value. Otherwise a first sample at 21.3%
		// shifts all later 20% steps to 41.3/61.3/81.3 and can make 100%
		// permanently unreachable.
		if level, ok := usageAlertStepLevel(rule, value); ok {
			value = level
		}
	}
	state := &UsageAlertState{
		RealAccountID:       realAccountID,
		RuleID:              rule.ID,
		UsageType:           rule.UsageType,
		Window:              rule.Window,
		LastStatus:          status,
		LastTriggeredAt:     triggeredAt,
		LastValue:           &value,
		LastResetAt:         window.ResetAt,
		LastGeneration:      window.Generation,
		ResetOrderProtected: !isCodexOverall7dRule(rule),
	}
	return s.repo.UpsertState(ctx, state)
}

func isCodexOverall7dRule(rule *UsageAlertRule) bool {
	return rule != nil && rule.Platform == UsageAlertPlatformOpenAI &&
		normalizeUsageAlertUsageType(rule.UsageType) == UsageAlertTypeOverall &&
		rule.Window == UsageAlertWindow7d
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
			timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("webhook returned status %d", resp.StatusCode)
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
	if account.Identifier != nil && strings.TrimSpace(*account.Identifier) == "__auto__" {
		return fmt.Errorf("real account identifier %q is reserved", "__auto__")
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

func validateUsageAlertWebhookURL(raw string, allowPrivate bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if err := validateUsageAlertOutboundURL(parsed, allowPrivate); err != nil {
		return "", err
	}
	// Preserve trailing "/" and raw path encoding because webhook endpoints
	// can treat /hook and /hook/ distinctly or 301-redirect POST to GET.
	return trimmed, nil
}

func validateUsageAlertOutboundURL(parsed *url.URL, allowPrivate bool) error {
	if parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid url")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("invalid url scheme: %s", parsed.Scheme)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return errors.New("invalid host")
	}
	if port := parsed.Port(); port != "" {
		num, err := strconv.Atoi(port)
		if err != nil || num <= 0 || num > 65535 {
			return fmt.Errorf("invalid port: %s", port)
		}
	}
	if !allowPrivate {
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return fmt.Errorf("host is not allowed: %s", host)
		}
		if ip := net.ParseIP(host); ip != nil && isDisallowedWebhookIP(ip) {
			return fmt.Errorf("host is not allowed: %s", host)
		}
	}
	return nil
}

func (s *UsageAlertService) validateWebhookTarget(webhook *UsageAlertWebhook) error {
	if err := validateUsageAlertWebhook(webhook); err != nil {
		return err
	}
	if s != nil && s.disallowPrivateWebhookTargets && webhook != nil && webhook.Type == UsageAlertWebhookTypeJSONPost {
		normalized, err := validateUsageAlertWebhookURL(webhook.URL, false)
		if err != nil {
			return fmt.Errorf("webhook url is not allowed: %w", err)
		}
		webhook.URL = normalized
	}
	return nil
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
		normalized, err := validateUsageAlertWebhookURL(webhook.URL, true)
		if err != nil {
			return fmt.Errorf("webhook url is invalid")
		}
		webhook.URL = normalized
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
	if event.AccountReset {
		if cfg.Language == "zh" {
			return fmt.Sprintf(
				"[Sub2API] 账号用量已重置\n账户：%s\n平台：%s\n用量类型：%s\n窗口：%s\n已用：%.1f%%\n剩余：%.1f%%\n下次重置时间：%s\n重置通知时间：%s",
				accountName,
				usageAlertPlatformDisplayName(event.Platform, "zh"),
				usageAlertUsageTypeDisplayName(event.UsageType, "zh"),
				usageAlertWindowDisplayName(event.Window, "zh"),
				event.UsedPercent,
				event.RemainingPercent,
				resetAt,
				triggeredAt,
			)
		}
		return fmt.Sprintf(
			"[Sub2API] Account usage reset\nAccount: %s\nPlatform: %s\nUsage type: %s\nWindow: %s\nUsed: %.1f%%\nRemaining: %.1f%%\nNext reset: %s\nReset notified: %s",
			accountName,
			usageAlertPlatformDisplayName(event.Platform, "en"),
			usageAlertUsageTypeDisplayName(event.UsageType, "en"),
			usageAlertWindowDisplayName(event.Window, "en"),
			event.UsedPercent,
			event.RemainingPercent,
			resetAt,
			triggeredAt,
		)
	}
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
		return true
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
	currentLevel, currentLevelOK := usageAlertStepLevel(rule, currentValue)
	if !currentLevelOK {
		return false
	}
	if state == nil || state.LastStatus != UsageAlertStatusTriggered {
		return true
	}
	if state.LastValue == nil {
		return stateAllowsRepeat(state, rule, now)
	}
	lastLevel, lastLevelOK := usageAlertStepLevel(rule, *state.LastValue)
	if !lastLevelOK {
		return stateAllowsRepeat(state, rule, now)
	}
	var advanced bool
	if rule.Operator == UsageAlertOperatorLTE {
		advanced = currentLevel < lastLevel
	} else {
		advanced = currentLevel > lastLevel
	}
	return advanced && stateAllowsRepeat(state, rule, now)
}

// usageAlertStepLevel maps a sample to a fixed level anchored at the rule's
// threshold. For used >= 20 with step 20 the levels are exactly
// 20/40/60/80/100; for remaining <= 80 they are 80/60/40/20/0.
func usageAlertStepLevel(rule *UsageAlertRule, value float64) (float64, bool) {
	step := usageAlertRuleStepPercent(rule)
	if rule == nil || step <= 0 {
		return 0, false
	}
	const epsilon = 1e-9
	if rule.Operator == UsageAlertOperatorLTE {
		if value > rule.Threshold+epsilon {
			return 0, false
		}
		steps := math.Floor((rule.Threshold-value)/step + epsilon)
		level := rule.Threshold - steps*step
		if level < 0 {
			level = 0
		}
		return level, true
	}
	if value < rule.Threshold-epsilon {
		return 0, false
	}
	steps := math.Floor((value-rule.Threshold)/step + epsilon)
	level := rule.Threshold + steps*step
	if level > 100 {
		level = 100
	}
	return level, true
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
	if trigger.AccountReset {
		eventName = UsageAlertEventReset
	}
	eventIdentity := ""
	if trigger.AccountReset {
		eventIdentity = fmt.Sprintf(
			"%d|%s|%s|%s|%s",
			snapshot.RealAccountID,
			snapshot.UsageType,
			trigger.Window,
			eventName,
			usageAlertWindowGenerationIdentity(trigger.WindowState),
		)
	} else {
		eventIdentity = fmt.Sprintf(
			"%d|%s|%d|%s|%s|%s|%s|%.12g|%s|%s|%s",
			snapshot.RealAccountID,
			snapshot.UsageType,
			trigger.Rule.ID,
			trigger.Window,
			eventName,
			trigger.Rule.Metric,
			trigger.Rule.Operator,
			trigger.Rule.Threshold,
			usageAlertOptionalFloatIdentity(trigger.Rule.StepPercent),
			trigger.StateAnchor,
			usageAlertWindowGenerationIdentity(trigger.WindowState),
		)
	}
	accountID := snapshot.AccountID
	if trigger.WindowState.AccountID > 0 {
		accountID = trigger.WindowState.AccountID
	}
	source := snapshot.Source
	if trigger.WindowState.Source != "" {
		source = trigger.WindowState.Source
	}
	eventHash := sha256.Sum256([]byte(eventIdentity))
	event := UsageAlertWebhookEvent{
		Event:            eventName,
		EventID:          fmt.Sprintf("ua-%x", eventHash[:]),
		TriggeredAt:      trigger.TriggeredAt,
		AccountID:        accountID,
		RealAccountID:    snapshot.RealAccountID,
		RealAccountName:  realAccountName,
		UsageType:        snapshot.UsageType,
		UsageRelation:    snapshot.UsageRelation,
		ParentUsageType:  snapshot.ParentUsageType,
		QuotaDimension:   usageAlertLegacyQuotaDimension(snapshot.UsageType),
		Platform:         snapshot.Platform,
		Source:           source,
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
		AccountReset:     trigger.AccountReset,
		StepPercent:      trigger.Rule.StepPercent,
	}
	if trigger.AccountReset {
		event.RuleID = 0
		event.RuleName = ""
		event.Metric = ""
		event.Operator = ""
		event.Threshold = 0
		event.StepPercent = nil
	}
	return event
}

func usageAlertWindowGenerationIdentity(window UsageAlertWindowSnapshot) string {
	if window.Generation > 0 {
		return fmt.Sprintf("generation:%d", window.Generation)
	}
	return fmt.Sprintf("reset:%d", usageAlertResetUnixNano(window.ResetAt))
}

func usageAlertOptionalFloatIdentity(value *float64) string {
	if value == nil {
		return "nil"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func usageAlertResetUnixNano(resetAt *time.Time) int64 {
	if resetAt == nil {
		return 0
	}
	return resetAt.UTC().UnixNano()
}

func usageAlertSnapshotsFromUsageInfo(accountID int64, platform, source string, usage *UsageInfo, sampledAt time.Time) []*UsageAlertSnapshot {
	if accountID <= 0 || usage == nil {
		return nil
	}
	snapshots := make([]*UsageAlertSnapshot, 0, 2)
	windows := make(map[string]UsageAlertWindowSnapshot, 2)
	if usage.FiveHour != nil && !usage.FiveHour.UtilizationUnknown {
		windows[UsageAlertWindow5h] = usageAlertWindowFromProgress(usage.FiveHour)
	}
	if usage.SevenDay != nil && !usage.SevenDay.UtilizationUnknown {
		windows[UsageAlertWindow7d] = usageAlertWindowFromProgress(usage.SevenDay)
	}
	overallType := []string(nil)
	if platform == UsageAlertPlatformAnthropic {
		overallType = []string{UsageAlertTypeOverall}
	}
	if snapshot := usageAlertSnapshotFromWindows(accountID, platform, source, windows, sampledAt, overallType...); snapshot != nil {
		snapshots = append(snapshots, snapshot)
	}
	if platform == UsageAlertPlatformAnthropic && usage.SevenDayFable != nil && !usage.SevenDayFable.UtilizationUnknown {
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
