package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateUsageAlertWebhookByType(t *testing.T) {
	jsonPost := &UsageAlertWebhook{
		Name:       "json",
		Type:       UsageAlertWebhookTypeJSONPost,
		RetryCount: 1,
	}
	require.ErrorContains(t, validateUsageAlertWebhook(jsonPost), "webhook url is required")

	telegram := &UsageAlertWebhook{
		Name: "telegram",
		Type: UsageAlertWebhookTypeTelegram,
		Config: map[string]any{
			"bot_token": "123456:abcDEF",
			"chat_id":   "-1001234567890",
		},
		RetryCount: 1,
	}
	require.NoError(t, validateUsageAlertWebhook(telegram))
}

func TestValidateUsageAlertRuleRejectsInvalidStepPercent(t *testing.T) {
	negative := -0.1
	rule := validUsageAlertRuleForTest()
	rule.StepPercent = &negative
	require.ErrorContains(t, validateUsageAlertRule(rule), "step_percent must be between 0 and 100")

	tooLarge := 100.1
	rule = validUsageAlertRuleForTest()
	rule.StepPercent = &tooLarge
	require.ErrorContains(t, validateUsageAlertRule(rule), "step_percent must be between 0 and 100")

	zero := 0.0
	rule = validUsageAlertRuleForTest()
	rule.StepPercent = &zero
	require.NoError(t, validateUsageAlertRule(rule))
}

func TestValidateUsageAlertRuleRejectsSonnetWindow(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.Window = "7d_sonnet"

	require.ErrorIs(t, validateUsageAlertRule(rule), ErrUsageAlertInvalidWindow)
}

func TestValidateUsageAlertRuleNormalizesUsageType(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.UsageType = ""
	require.NoError(t, validateUsageAlertRule(rule))
	require.Equal(t, UsageAlertTypeOverall, rule.UsageType)

	rule = validUsageAlertRuleForTest()
	rule.UsageType = UsageAlertTypeSpark
	require.NoError(t, validateUsageAlertRule(rule))

	rule.UsageType = "bad type!"
	require.ErrorIs(t, validateUsageAlertRule(rule), ErrUsageAlertInvalidUsageType)
}

func TestUsageAlertTypeSupportsPlatformAndWindow(t *testing.T) {
	require.True(t, usageAlertTypeSupportsRule(UsageAlertPlatformOpenAI, UsageAlertTypeSpark, UsageAlertWindow5h))
	require.True(t, usageAlertTypeSupportsRule(UsageAlertPlatformAnthropic, UsageAlertTypeFable, UsageAlertWindow7d))
	require.False(t, usageAlertTypeSupportsRule(UsageAlertPlatformAnthropic, UsageAlertTypeFable, UsageAlertWindow5h))
	require.False(t, usageAlertTypeSupportsRule(UsageAlertPlatformOpenAI, UsageAlertTypeFable, UsageAlertWindow7d))
}

func TestUsageAlertSnapshotsKeepFableAsOverallSubLimit(t *testing.T) {
	overallReset := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	fableReset := overallReset.Add(time.Hour)
	snapshots := usageAlertSnapshotsFromUsageInfo(42, UsageAlertPlatformAnthropic, UsageAlertSourceClaudeUsageAPI, &UsageInfo{
		FiveHour:      &UsageProgress{Utilization: 25},
		SevenDay:      &UsageProgress{Utilization: 40, ResetsAt: &overallReset},
		SevenDayFable: &UsageProgress{Utilization: 65, ResetsAt: &fableReset},
	}, time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC))

	require.Len(t, snapshots, 2)
	require.Equal(t, UsageAlertTypeOverall, snapshots[0].UsageType)
	require.Equal(t, UsageAlertRelationPrimary, snapshots[0].UsageRelation)
	require.Equal(t, 40.0, snapshots[0].Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, UsageAlertTypeFable, snapshots[1].UsageType)
	require.Equal(t, UsageAlertRelationSubLimit, snapshots[1].UsageRelation)
	require.Equal(t, UsageAlertTypeOverall, snapshots[1].ParentUsageType)
	require.Equal(t, 65.0, snapshots[1].Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, overallReset, *snapshots[1].Windows[UsageAlertWindow7d].ResetAt)
}

func TestEnsureUsageAlertRuleNameBuildsDefault(t *testing.T) {
	step := 5.0
	minReset := 24.0
	rule := validUsageAlertRuleForTest()
	rule.Name = " "
	rule.RealAccount = &RealAccount{Name: "OpenAI Main"}
	rule.StepPercent = &step
	rule.MinResetAfterHours = &minReset

	ensureUsageAlertRuleName(rule)

	require.Contains(t, rule.Name, "OpenAI Main")
	require.Contains(t, rule.Name, "OpenAI")
	require.Contains(t, rule.Name, "7d")
	require.Contains(t, rule.Name, "remaining")
	require.Contains(t, rule.Name, "<= 20%")
	require.Contains(t, rule.Name, "step 5%")
	require.Contains(t, rule.Name, "reset left 24h")
	require.Contains(t, rule.Name, "cooldown 60m")
}

func TestEnsureUsageAlertRuleNameTruncatesByRune(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.Name = strings.Repeat("测", usageAlertRuleNameMaxLength+5)

	ensureUsageAlertRuleName(rule)

	require.Len(t, []rune(rule.Name), usageAlertRuleNameMaxLength)
}

func TestUsageAlertStepAllowsTriggerRequiresCooldownAndStep(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	step := 5.0
	lastValue := 18.0
	lastTriggeredAt := now.Add(-30 * time.Minute)
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorLTE,
		Threshold:       20,
		StepPercent:     &step,
		CooldownMinutes: 60,
	}
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
	}

	require.True(t, usageAlertStepAllowsTrigger(nil, rule, 18, now))
	require.False(t, usageAlertStepAllowsTrigger(state, rule, 12, now))

	lastTriggeredAt = now.Add(-2 * time.Hour)
	state.LastTriggeredAt = &lastTriggeredAt
	require.False(t, usageAlertStepAllowsTrigger(state, rule, 19, now))
	require.True(t, usageAlertStepAllowsTrigger(state, rule, 14, now))
}

func TestUsageAlertStepAllowsTriggerSupportsIncreasingMetric(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	step := 5.0
	lastValue := 80.0
	lastTriggeredAt := now.Add(-2 * time.Hour)
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorGTE,
		Threshold:       80,
		StepPercent:     &step,
		CooldownMinutes: 60,
	}
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
	}

	require.False(t, usageAlertStepAllowsTrigger(state, rule, 84, now))
	require.True(t, usageAlertStepAllowsTrigger(state, rule, 85, now))
}

func TestUsageAlertStepAllowsTriggerWithZeroCooldown(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	step := 20.0
	lastValue := 20.0
	lastTriggeredAt := now
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorGTE,
		Threshold:       20,
		StepPercent:     &step,
		CooldownMinutes: 0,
	}
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
	}

	require.True(t, usageAlertStepAllowsTrigger(state, rule, 40, now))
}

func TestUsageAlertStepLevelUsesFixedThresholds(t *testing.T) {
	step := 20.0
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorGTE,
		Threshold:       20,
		StepPercent:     &step,
		CooldownMinutes: 240,
	}

	tests := []struct {
		value float64
		level float64
	}{
		{value: 20.1, level: 20},
		{value: 40, level: 40},
		{value: 67.5, level: 60},
		{value: 80.2, level: 80},
		{value: 100, level: 100},
	}
	for _, tt := range tests {
		level, ok := usageAlertStepLevel(rule, tt.value)
		require.True(t, ok)
		require.Equal(t, tt.level, level)
	}

	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	lastTriggeredAt := now.Add(-5 * time.Hour)
	firstSample := 20.1
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &firstSample,
	}
	require.True(t, usageAlertStepAllowsTrigger(state, rule, 40, now), "a 20.1%% first sample must not shift the next level to 40.1%%")
}

func TestCommitUsageAlertTriggerPersistsFixedStepLevelInsteadOfSampleValue(t *testing.T) {
	step := 20.0
	realAccountID := int64(1)
	rule := &UsageAlertRule{
		ID:              9,
		RealAccountID:   &realAccountID,
		UsageType:       UsageAlertTypeOverall,
		Window:          UsageAlertWindow7d,
		Metric:          UsageAlertMetricUsed,
		Operator:        UsageAlertOperatorGTE,
		Threshold:       20,
		StepPercent:     &step,
		CooldownMinutes: 240,
		Enabled:         true,
	}
	repo := &usageAlertGenerationStateRepoStub{}
	svc := NewUsageAlertService(repo, nil)
	current := &UsageAlertSnapshot{
		RealAccountID: realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 43.7, RemainingPercent: 56.3},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), nil, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.Nil(t, repo.lastUpsertedState, "evaluation must not advance state before webhook delivery")
	require.NoError(t, svc.commitUsageAlertTrigger(triggers[0], realAccountID))
	require.NotNil(t, repo.lastUpsertedState)
	require.Equal(t, 40.0, *repo.lastUpsertedState.LastValue)
	require.Equal(t, 43.7, triggers[0].Value, "webhook event should retain the actual sampled value")
}

func TestUsageAlertStepTimelineEmitsTwentyThroughOneHundredAcrossSeventeenHours(t *testing.T) {
	step := 20.0
	rule := &UsageAlertRule{
		Operator:        UsageAlertOperatorGTE,
		Threshold:       20,
		StepPercent:     &step,
		CooldownMinutes: 240,
	}
	start := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	samples := []struct {
		after time.Duration
		value float64
	}{
		{after: 0, value: 20.2},
		{after: 2 * time.Hour, value: 39.8},
		{after: 4*time.Hour + time.Minute, value: 40.1},
		{after: 8*time.Hour + 2*time.Minute, value: 60.4},
		{after: 12*time.Hour + 3*time.Minute, value: 80.6},
		{after: 17 * time.Hour, value: 100},
	}

	var state *UsageAlertState
	triggeredLevels := make([]float64, 0, 5)
	for _, sample := range samples {
		now := start.Add(sample.after)
		if !usageAlertStepAllowsTrigger(state, rule, sample.value, now) {
			continue
		}
		level, ok := usageAlertStepLevel(rule, sample.value)
		require.True(t, ok)
		triggeredAt := now
		state = &UsageAlertState{
			LastStatus:      UsageAlertStatusTriggered,
			LastTriggeredAt: &triggeredAt,
			LastValue:       &level,
		}
		triggeredLevels = append(triggeredLevels, level)
	}

	require.Equal(t, []float64{20, 40, 60, 80, 100}, triggeredLevels)
}

func TestDeliverJSONPostWebhook(t *testing.T) {
	var got UsageAlertWebhookEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Sub2API-UsageAlert/1.0", r.Header.Get("User-Agent"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	svc := &UsageAlertService{httpClient: server.Client()}
	event := UsageAlertWebhookEvent{
		Event:       "account.usage_alert",
		EventID:     "evt-test",
		TriggeredAt: time.Now().UTC(),
		RuleName:    "low remaining",
		Window:      UsageAlertWindow7d,
	}
	err := svc.deliverWebhookWithRetry(context.Background(), UsageAlertWebhook{
		Name:       "json",
		Type:       UsageAlertWebhookTypeJSONPost,
		URL:        server.URL,
		Enabled:    true,
		RetryCount: 0,
	}, event)
	require.NoError(t, err)
	require.Equal(t, "evt-test", got.EventID)
}

type usageAlertDeliveryRepoStub struct {
	UsageAlertRepository
	mu       sync.Mutex
	snapshot *UsageAlertSnapshot
	state    *UsageAlertState
	states   map[int64]*UsageAlertState
	rule     *UsageAlertRule
	rules    []*UsageAlertRule
	webhook  *UsageAlertWebhook
	webhooks []*UsageAlertWebhook
	receipts map[string]bool
}

func (s *usageAlertDeliveryRepoStub) GetSnapshot(_ context.Context, _ int64, _ string) (*UsageAlertSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *usageAlertDeliveryRepoStub) UpsertSnapshot(_ context.Context, snapshot *UsageAlertSnapshot) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot != nil && snapshot.SampledAt.Before(s.snapshot.SampledAt) {
		return false, nil
	}
	s.snapshot = snapshot
	return true, nil
}

func (s *usageAlertDeliveryRepoStub) ListEnabledRules(_ context.Context, _ int64, _ string) ([]*UsageAlertRule, error) {
	if s.rules != nil {
		return s.rules, nil
	}
	return []*UsageAlertRule{s.rule}, nil
}

func (s *usageAlertDeliveryRepoStub) ListEnabledWebhooksForRealAccount(_ context.Context, _ int64) ([]*UsageAlertWebhook, error) {
	if s.webhooks != nil {
		return s.webhooks, nil
	}
	return []*UsageAlertWebhook{s.webhook}, nil
}

func (s *usageAlertDeliveryRepoStub) GetRealAccount(_ context.Context, id int64) (*RealAccount, error) {
	return &RealAccount{ID: id, Name: "OpenAI Main", Platform: UsageAlertPlatformOpenAI}, nil
}

func (s *usageAlertDeliveryRepoStub) GetState(_ context.Context, _, ruleID int64, _, _ string) (*UsageAlertState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states != nil {
		return s.states[ruleID], nil
	}
	return s.state, nil
}

func (s *usageAlertDeliveryRepoStub) UpsertState(_ context.Context, state *UsageAlertState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states != nil {
		s.states[state.RuleID] = state
	}
	s.state = state
	return nil
}

func (s *usageAlertDeliveryRepoStub) ClaimWebhookDelivery(_ context.Context, eventID string, _, _, webhookID int64, _ string, _ time.Duration) (UsageAlertDeliveryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts == nil {
		s.receipts = make(map[string]bool)
	}
	key := fmt.Sprintf("%s:%d", eventID, webhookID)
	if s.receipts[key] {
		return UsageAlertDeliveryAlreadyDelivered, nil
	}
	return UsageAlertDeliveryClaimed, nil
}

func (s *usageAlertDeliveryRepoStub) CompleteWebhookDelivery(_ context.Context, eventID string, webhookID int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts == nil {
		s.receipts = make(map[string]bool)
	}
	s.receipts[fmt.Sprintf("%s:%d", eventID, webhookID)] = true
	return nil
}

func (s *usageAlertDeliveryRepoStub) ReleaseWebhookDelivery(_ context.Context, _ string, _ int64, _ string) error {
	return nil
}

func (s *usageAlertDeliveryRepoStub) currentState() *UsageAlertState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *usageAlertDeliveryRepoStub) currentStateForRule(ruleID int64) *UsageAlertState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[ruleID]
}

func TestObserveAsyncRetriesUncommittedTriggerAfterWebhookFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	realAccountID := int64(1)
	step := 20.0
	repo := &usageAlertDeliveryRepoStub{
		rule: &UsageAlertRule{
			ID:              9,
			RealAccountID:   &realAccountID,
			UsageType:       UsageAlertTypeOverall,
			Window:          UsageAlertWindow7d,
			Metric:          UsageAlertMetricUsed,
			Operator:        UsageAlertOperatorGTE,
			Threshold:       20,
			StepPercent:     &step,
			CooldownMinutes: 60,
			Enabled:         true,
		},
		webhook: &UsageAlertWebhook{
			Name:       "json",
			Type:       UsageAlertWebhookTypeJSONPost,
			URL:        server.URL,
			Enabled:    true,
			RetryCount: 0,
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = server.Client()
	sampledAt := time.Now().UTC()
	snapshot := UsageAlertSnapshot{
		AccountID:     2,
		RealAccountID: realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Platform:      UsageAlertPlatformOpenAI,
		SampledAt:     sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 20, RemainingPercent: 80},
		},
	}

	retry := svc.observeAsync(snapshot)
	require.True(t, retry, "failed terminal delivery must remain queued for background retry")
	require.Nil(t, repo.currentState(), "failed delivery must leave the trigger eligible for retry")

	snapshot.SampledAt = sampledAt.Add(time.Minute)
	require.False(t, svc.observeAsync(snapshot))

	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, UsageAlertStatusTriggered, repo.currentState().LastStatus)
}

func TestObserveAsyncRetriesOnlyUndeliveredWebhookWithStableEventID(t *testing.T) {
	var healthyRequests atomic.Int32
	var flakyRequests atomic.Int32
	var eventIDsMu sync.Mutex
	var eventIDs []string
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyRequests.Add(1)
		var event UsageAlertWebhookEvent
		require.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		eventIDsMu.Lock()
		eventIDs = append(eventIDs, event.EventID)
		eventIDsMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthy.Close()
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := flakyRequests.Add(1)
		var event UsageAlertWebhookEvent
		require.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		eventIDsMu.Lock()
		eventIDs = append(eventIDs, event.EventID)
		eventIDsMu.Unlock()
		if attempt == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer flaky.Close()

	realAccountID := int64(1)
	repo := &usageAlertDeliveryRepoStub{
		rule: &UsageAlertRule{
			ID:              9,
			RealAccountID:   &realAccountID,
			UsageType:       UsageAlertTypeOverall,
			Window:          UsageAlertWindow7d,
			Metric:          UsageAlertMetricUsed,
			Operator:        UsageAlertOperatorGTE,
			Threshold:       20,
			CooldownMinutes: 0,
			Enabled:         true,
		},
		webhooks: []*UsageAlertWebhook{
			{ID: 1, Name: "healthy", Type: UsageAlertWebhookTypeJSONPost, URL: healthy.URL, Enabled: true},
			{ID: 2, Name: "flaky", Type: UsageAlertWebhookTypeJSONPost, URL: flaky.URL, Enabled: true},
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = healthy.Client()
	resetAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	snapshot := UsageAlertSnapshot{
		AccountID:     2,
		RealAccountID: realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Platform:      UsageAlertPlatformOpenAI,
		SampledAt:     time.Now().UTC(),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 20, RemainingPercent: 80, ResetAt: &resetAt},
		},
	}

	svc.observeAsync(snapshot)
	require.Nil(t, repo.currentState())
	snapshot.SampledAt = snapshot.SampledAt.Add(time.Minute)
	snapshot.Windows[UsageAlertWindow7d] = UsageAlertWindowSnapshot{UsedPercent: 25, RemainingPercent: 75, ResetAt: &resetAt}
	svc.observeAsync(snapshot)

	require.Equal(t, int32(1), healthyRequests.Load(), "a delivered endpoint must not be resent")
	require.Equal(t, int32(2), flakyRequests.Load())
	require.Equal(t, UsageAlertStatusTriggered, repo.currentState().LastStatus)
	eventIDsMu.Lock()
	require.Len(t, eventIDs, 3)
	require.Equal(t, eventIDs[0], eventIDs[1])
	require.Equal(t, eventIDs[1], eventIDs[2])
	eventIDsMu.Unlock()
}

func TestObserveAsyncDeduplicatesAccountResetPerWebhookAcrossRules(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	var got UsageAlertWebhookEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer secondServer.Close()

	realAccountID := int64(1)
	manualAt := time.Date(2026, 8, 13, 0, 11, 12, 0, time.UTC)
	manualResetAt := manualAt.Add(7 * 24 * time.Hour)
	officialAt := manualAt.Add(11*time.Hour + 22*time.Minute)
	officialResetAt := manualResetAt.Add(11*time.Hour + 22*time.Minute)
	rule20 := &UsageAlertRule{
		ID: 7, Name: "used 20", Platform: UsageAlertPlatformOpenAI, RealAccountID: &realAccountID,
		UsageType: UsageAlertTypeOverall, Window: UsageAlertWindow7d, Metric: UsageAlertMetricUsed,
		Operator: UsageAlertOperatorGTE, Threshold: 20, Enabled: true,
	}
	rule80 := &UsageAlertRule{
		ID: 8, Name: "used 80", Platform: UsageAlertPlatformOpenAI, RealAccountID: &realAccountID,
		UsageType: UsageAlertTypeOverall, Window: UsageAlertWindow7d, Metric: UsageAlertMetricUsed,
		Operator: UsageAlertOperatorGTE, Threshold: 80, Enabled: true,
	}
	repo := &usageAlertDeliveryRepoStub{
		snapshot: &UsageAlertSnapshot{
			AccountID: 2, RealAccountID: realAccountID, UsageType: UsageAlertTypeOverall,
			Platform: UsageAlertPlatformOpenAI, Source: UsageAlertSourceOpenAIQuotaReset, SampledAt: manualAt,
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {
					UsedPercent: 0, RemainingPercent: 100, ResetAt: &manualResetAt, SampledAt: &manualAt,
					Generation: 3, OfficialResetAnchorAt: &manualResetAt, AwaitingOfficialReset: true,
				},
			},
		},
		states: map[int64]*UsageAlertState{
			7: {RealAccountID: realAccountID, RuleID: 7, UsageType: UsageAlertTypeOverall, Window: UsageAlertWindow7d, LastStatus: UsageAlertStatusNormal, LastGeneration: 3},
			8: {RealAccountID: realAccountID, RuleID: 8, UsageType: UsageAlertTypeOverall, Window: UsageAlertWindow7d, LastStatus: UsageAlertStatusNormal, LastGeneration: 3},
		},
		rules: []*UsageAlertRule{rule20, rule80},
		webhooks: []*UsageAlertWebhook{
			{ID: 1, Name: "json-1", Type: UsageAlertWebhookTypeJSONPost, URL: server.URL, Enabled: true},
			{ID: 2, Name: "json-2", Type: UsageAlertWebhookTypeJSONPost, URL: secondServer.URL, Enabled: true},
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = server.Client()

	retry := svc.observeAsync(UsageAlertSnapshot{
		AccountID: 2, RealAccountID: realAccountID, UsageType: UsageAlertTypeOverall,
		Platform: UsageAlertPlatformOpenAI, Source: UsageAlertSourceOpenAICodexUsageAPI, SampledAt: officialAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 17, RemainingPercent: 83, ResetAt: &officialResetAt},
		},
	})

	require.False(t, retry)
	require.Equal(t, int32(1), firstRequests.Load())
	require.Equal(t, int32(1), secondRequests.Load())
	require.Equal(t, UsageAlertEventReset, got.Event)
	require.Zero(t, got.RuleID)
	require.Empty(t, got.RuleName)
	require.Equal(t, UsageAlertWindow7d, got.Window)
	require.Equal(t, int64(4), repo.currentStateForRule(7).LastGeneration)
	require.Equal(t, int64(4), repo.currentStateForRule(8).LastGeneration)
	require.Equal(t, UsageAlertStatusNormal, repo.currentStateForRule(7).LastStatus)
	require.Equal(t, UsageAlertStatusNormal, repo.currentStateForRule(8).LastStatus)
}

func TestObservePreservesFailedTriggerWhenRecoverySampleWakesBackoff(t *testing.T) {
	var requests atomic.Int32
	var eventsMu sync.Mutex
	var events []UsageAlertWebhookEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event UsageAlertWebhookEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode event: %v", err)
		}
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
		if requests.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	realAccountID := int64(1)
	repo := &usageAlertDeliveryRepoStub{
		rule: &UsageAlertRule{
			ID:              9,
			RealAccountID:   &realAccountID,
			UsageType:       UsageAlertTypeOverall,
			Window:          UsageAlertWindow7d,
			Metric:          UsageAlertMetricUsed,
			Operator:        UsageAlertOperatorGTE,
			Threshold:       20,
			CooldownMinutes: 0,
			Enabled:         true,
		},
		webhook: &UsageAlertWebhook{
			ID:      1,
			Name:    "json",
			Type:    UsageAlertWebhookTypeJSONPost,
			URL:     server.URL,
			Enabled: true,
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = server.Client()
	sampledAt := time.Now().UTC()
	snapshot := func(used float64, at time.Time) *UsageAlertSnapshot {
		return &UsageAlertSnapshot{
			AccountID:     2,
			RealAccountID: realAccountID,
			UsageType:     UsageAlertTypeOverall,
			Platform:      UsageAlertPlatformOpenAI,
			SampledAt:     at,
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {UsedPercent: used, RemainingPercent: 100 - used},
			},
		}
	}

	svc.Observe(context.Background(), snapshot(95, sampledAt))
	require.Eventually(t, func() bool {
		actual, ok := svc.workers.Load("1:overall")
		if !ok {
			return false
		}
		worker, ok := actual.(*usageAlertObservationWorker)
		if !ok {
			return false
		}
		worker.mu.Lock()
		defer worker.mu.Unlock()
		return worker.waiting && worker.retryPending != nil
	}, 2*time.Second, 10*time.Millisecond)

	svc.Observe(context.Background(), snapshot(10, sampledAt.Add(time.Minute)))

	require.Eventually(t, func() bool {
		state := repo.currentState()
		return requests.Load() == 3 && state != nil && state.LastStatus == UsageAlertStatusNormal
	}, 2*time.Second, 10*time.Millisecond)
	eventsMu.Lock()
	require.Len(t, events, 3)
	require.Equal(t, UsageAlertEventTriggered, events[0].Event)
	require.Equal(t, events[0].EventID, events[1].EventID)
	require.Equal(t, UsageAlertEventResolved, events[2].Event)
	eventsMu.Unlock()
}

func TestUsageAlertRuleZeroCooldownIsEdgeOnlyWithoutStep(t *testing.T) {
	now := time.Now().UTC()
	rule := validUsageAlertRuleForTest()
	rule.CooldownMinutes = 0
	value := 10.0
	lastTriggeredAt := now.Add(-time.Hour)
	state := &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &value,
	}
	previous := &UsageAlertSnapshot{Windows: map[string]UsageAlertWindowSnapshot{
		UsageAlertWindow7d: {RemainingPercent: 10},
	}}
	current := &UsageAlertSnapshot{Windows: map[string]UsageAlertWindowSnapshot{
		UsageAlertWindow7d: {RemainingPercent: 9},
	}}

	require.False(t, usageAlertRuleAllowsTrigger(previous, current, rule, state, 9, now))
	require.True(t, usageAlertRuleAllowsTrigger(previous, current, rule, nil, 9, now), "first observation in a generation must trigger")
}

func TestObserveAsyncSerializesSamplesForSameUsageScope(t *testing.T) {
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	realAccountID := int64(1)
	step := 20.0
	repo := &usageAlertDeliveryRepoStub{
		rule: &UsageAlertRule{
			ID:              9,
			RealAccountID:   &realAccountID,
			UsageType:       UsageAlertTypeOverall,
			Window:          UsageAlertWindow7d,
			Metric:          UsageAlertMetricUsed,
			Operator:        UsageAlertOperatorGTE,
			Threshold:       20,
			StepPercent:     &step,
			CooldownMinutes: 0,
			Enabled:         true,
		},
		webhook: &UsageAlertWebhook{
			Name:       "json",
			Type:       UsageAlertWebhookTypeJSONPost,
			URL:        server.URL,
			Enabled:    true,
			RetryCount: 0,
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = server.Client()
	sampledAt := time.Now().UTC()
	snapshot := func(used float64, at time.Time) UsageAlertSnapshot {
		return UsageAlertSnapshot{
			AccountID:     2,
			RealAccountID: realAccountID,
			UsageType:     UsageAlertTypeOverall,
			Platform:      UsageAlertPlatformOpenAI,
			SampledAt:     at,
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {UsedPercent: used, RemainingPercent: 100 - used},
			},
		}
	}

	var evaluations sync.WaitGroup
	evaluations.Add(2)
	go func() {
		defer evaluations.Done()
		svc.observeAsync(snapshot(20, sampledAt))
	}()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("timed out waiting for first webhook delivery")
	}
	go func() {
		defer evaluations.Done()
		svc.observeAsync(snapshot(40, sampledAt.Add(time.Minute)))
	}()

	require.Never(t, func() bool {
		return requests.Load() > 1
	}, 100*time.Millisecond, 10*time.Millisecond)
	close(releaseFirst)
	evaluations.Wait()

	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, 40.0, *repo.currentState().LastValue)
}

func TestObserveCoalescesPendingSamplesPerUsageScope(t *testing.T) {
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	realAccountID := int64(1)
	step := 20.0
	repo := &usageAlertDeliveryRepoStub{
		rule: &UsageAlertRule{
			ID:              9,
			RealAccountID:   &realAccountID,
			UsageType:       UsageAlertTypeOverall,
			Window:          UsageAlertWindow7d,
			Metric:          UsageAlertMetricUsed,
			Operator:        UsageAlertOperatorGTE,
			Threshold:       20,
			StepPercent:     &step,
			CooldownMinutes: 0,
			Enabled:         true,
		},
		webhook: &UsageAlertWebhook{
			ID:      1,
			Name:    "json",
			Type:    UsageAlertWebhookTypeJSONPost,
			URL:     server.URL,
			Enabled: true,
		},
	}
	svc := NewUsageAlertService(repo, nil)
	svc.httpClient = server.Client()
	sampledAt := time.Now().UTC()
	snapshot := func(used float64, at time.Time) *UsageAlertSnapshot {
		return &UsageAlertSnapshot{
			AccountID:     2,
			RealAccountID: realAccountID,
			UsageType:     UsageAlertTypeOverall,
			Platform:      UsageAlertPlatformOpenAI,
			SampledAt:     at,
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {UsedPercent: used, RemainingPercent: 100 - used},
			},
		}
	}

	svc.Observe(context.Background(), snapshot(20, sampledAt))
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("timed out waiting for first webhook delivery")
	}
	for used := 40.0; used <= 100; used += 20 {
		svc.Observe(context.Background(), snapshot(used, sampledAt.Add(time.Duration(used)*time.Second)))
	}

	actual, ok := svc.workers.Load("1:overall")
	require.True(t, ok)
	worker, ok := actual.(*usageAlertObservationWorker)
	require.True(t, ok)
	worker.mu.Lock()
	require.NotNil(t, worker.pending)
	require.Equal(t, 100.0, worker.pending.Windows[UsageAlertWindow7d].UsedPercent)
	worker.mu.Unlock()
	require.Equal(t, int32(1), requests.Load())

	close(releaseFirst)
	require.Eventually(t, func() bool {
		state := repo.currentState()
		return requests.Load() == 2 && state != nil && state.LastValue != nil && *state.LastValue == 100
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRedactUsageAlertSecret(t *testing.T) {
	got := redactUsageAlertSecret(`Post "https://api.telegram.org/bot123456:abcDEF/sendMessage": timeout`, "123456:abcDEF")
	require.NotContains(t, got, "123456:abcDEF")
	require.Contains(t, got, "[redacted]")
}

func TestBuildUsageAlertWebhookEventUsesResolvedType(t *testing.T) {
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	step := 5.0
	rule.StepPercent = &step
	triggeredAt := time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC)
	snapshot := &UsageAlertSnapshot{
		AccountID:     11,
		RealAccountID: 22,
		UsageType:     UsageAlertTypeSpark,
		Platform:      UsageAlertPlatformOpenAI,
		Source:        UsageAlertSourceOpenAICodexHeaders,
	}

	trigger := UsageAlertTrigger{
		Rule:        rule,
		Window:      rule.Window,
		Value:       90,
		WindowState: UsageAlertWindowSnapshot{UsedPercent: 10, RemainingPercent: 90},
		TriggeredAt: triggeredAt,
		Resolved:    true,
		StateAnchor: "triggered:1:20:2",
	}
	event := buildUsageAlertWebhookEvent(snapshot, &RealAccount{Name: "OpenAI Main"}, trigger)

	require.Equal(t, UsageAlertEventResolved, event.Event)
	require.Equal(t, "OpenAI Main", event.RealAccountName)
	require.Equal(t, UsageAlertTypeSpark, event.UsageType)
	require.Equal(t, QuotaDimensionSpark, event.QuotaDimension)
	require.True(t, strings.HasPrefix(event.EventID, "ua-"))
	require.Equal(t, 90.0, event.Value)

	retry := trigger
	retry.TriggeredAt = triggeredAt.Add(time.Minute)
	retry.Value = 95
	retry.WindowState.RemainingPercent = 95
	retryRule := *rule
	retryStep := step
	retryRule.StepPercent = &retryStep
	retry.Rule = &retryRule
	retryEvent := buildUsageAlertWebhookEvent(snapshot, &RealAccount{Name: "OpenAI Main"}, retry)
	require.Equal(t, event.EventID, retryEvent.EventID, "an uncommitted transition must keep the same id across retries")
}

func TestBuildUsageAlertWebhookEventDeduplicatesAccountResetAcrossRules(t *testing.T) {
	snapshot := &UsageAlertSnapshot{RealAccountID: 22, UsageType: UsageAlertTypeOverall, Platform: UsageAlertPlatformOpenAI}
	resetAt := time.Date(2026, 8, 20, 11, 33, 17, 0, time.UTC)
	ruleA := validUsageAlertRuleForTest()
	ruleA.ID = 7
	ruleB := validUsageAlertRuleForTest()
	ruleB.ID = 8
	trigger := UsageAlertTrigger{
		Rule: ruleA, Window: UsageAlertWindow7d, WindowState: UsageAlertWindowSnapshot{Generation: 4, ResetAt: &resetAt},
		Resolved: true, AccountReset: true, StateAnchor: "new-generation",
	}

	eventA := buildUsageAlertWebhookEvent(snapshot, nil, trigger)
	trigger.Rule = ruleB
	eventB := buildUsageAlertWebhookEvent(snapshot, nil, trigger)

	require.Equal(t, eventA.EventID, eventB.EventID)
	require.Equal(t, UsageAlertEventReset, eventA.Event)
	require.Zero(t, eventA.RuleID)
	require.Empty(t, eventA.RuleName)
	payload, err := json.Marshal(eventA)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"rule_id":0`)
	require.Contains(t, string(payload), `"threshold":0`)
}

func TestFinalUsageAlertStateTriggersKeepsLastTransitionPerRule(t *testing.T) {
	ruleA := validUsageAlertRuleForTest()
	ruleA.ID = 7
	ruleB := validUsageAlertRuleForTest()
	ruleB.ID = 8
	triggers := []UsageAlertTrigger{
		{Rule: ruleA, Resolved: true, AccountReset: true},
		{Rule: ruleA, Resolved: false},
		{Rule: ruleB, Resolved: true, AccountReset: true},
	}

	final := finalUsageAlertStateTriggers(triggers)

	require.Len(t, final, 2)
	require.Equal(t, int64(7), final[0].Rule.ID)
	require.False(t, final[0].Resolved)
	require.Equal(t, int64(8), final[1].Rule.ID)
	require.True(t, final[1].Resolved)
}

func TestFormatUsageAlertTelegramMessageUsesLanguageAndTimezone(t *testing.T) {
	resetAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	event := UsageAlertWebhookEvent{
		Event:            "account.usage_alert",
		TriggeredAt:      time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC),
		RealAccountName:  "OpenAI Main",
		UsageType:        UsageAlertTypeSpark,
		Platform:         UsageAlertPlatformOpenAI,
		RuleName:         "weekly remaining low",
		Window:           UsageAlertWindow7d,
		Metric:           UsageAlertMetricRemaining,
		Operator:         UsageAlertOperatorLTE,
		Threshold:        20,
		UsedPercent:      81.5,
		RemainingPercent: 18.5,
		ResetAt:          &resetAt,
	}

	message := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "zh",
		Timezone: "Asia/Shanghai",
	})

	require.Contains(t, message, "[Sub2API] 用量告警")
	require.Contains(t, message, "账户：OpenAI Main")
	require.Contains(t, message, "用量类型：Spark")
	require.Contains(t, message, "阈值：剩余 <= 20.0%")
	require.Contains(t, message, "触发时间：2026-06-28 18:30:00")
	require.Contains(t, message, "重置时间：2026-06-28 20:00:00")
	require.NotContains(t, message, "UTC")
}

func TestFormatUsageAlertTelegramMessageUsesResetTitle(t *testing.T) {
	event := UsageAlertWebhookEvent{
		Event:            UsageAlertEventResolved,
		TriggeredAt:      time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC),
		RealAccountName:  "OpenAI Main",
		Platform:         UsageAlertPlatformOpenAI,
		RuleName:         "weekly remaining low",
		Window:           UsageAlertWindow7d,
		Metric:           UsageAlertMetricRemaining,
		Operator:         UsageAlertOperatorLTE,
		Threshold:        20,
		UsedPercent:      10,
		RemainingPercent: 90,
	}

	zh := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "zh",
		Timezone: "Asia/Shanghai",
	})
	require.Contains(t, zh, "[Sub2API] 用量告警已重置")
	require.Contains(t, zh, "重置通知时间：2026-06-28 18:30:00")

	en := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{
		Language: "en",
		Timezone: "UTC",
	})
	require.Contains(t, en, "[Sub2API] Usage alert reset")
	require.Contains(t, en, "Reset notified: 2026-06-28 10:30:00")
}

func TestFormatUsageAlertTelegramMessageUsesAccountResetLayout(t *testing.T) {
	event := UsageAlertWebhookEvent{
		Event: UsageAlertEventResolved, TriggeredAt: time.Date(2026, 8, 13, 3, 33, 17, 0, time.UTC),
		RealAccountName: "OpenAI Main", Platform: UsageAlertPlatformOpenAI, UsageType: UsageAlertTypeOverall,
		Window: UsageAlertWindow7d, UsedPercent: 17, RemainingPercent: 83, AccountReset: true,
	}

	message := formatUsageAlertTelegramMessage(event, UsageAlertTelegramConfig{Language: "zh", Timezone: "Asia/Shanghai"})

	require.Contains(t, message, "[Sub2API] 账号用量已重置")
	require.NotContains(t, message, "规则：")
	require.NotContains(t, message, "阈值：")
}

func TestUsageAlertTelegramConfigRejectsInvalidTimezone(t *testing.T) {
	_, err := usageAlertTelegramConfig(map[string]any{
		"bot_token": "123456:abcDEF",
		"chat_id":   "-1001234567890",
		"timezone":  "Mars/Olympus",
	})
	require.ErrorContains(t, err, "telegram timezone is invalid")
}

type usageAlertAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (s *usageAlertAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	return s.accounts[id], nil
}

type usageAlertRepoStub struct {
	UsageAlertRepository
	ensuredAccountID  int64
	attachedRealID    int64
	attachedAccountID int64
}

func (s *usageAlertRepoStub) EnsureRealAccountForAccount(_ context.Context, account *Account) (*RealAccount, error) {
	s.ensuredAccountID = account.ID
	return &RealAccount{ID: 99, Name: account.Name, Platform: account.Platform}, nil
}

func (s *usageAlertRepoStub) AttachAccount(_ context.Context, realAccountID, accountID int64) error {
	s.attachedRealID = realAccountID
	s.attachedAccountID = accountID
	return nil
}

func TestResolveUsageAlertScopeSharesRealAccountButSeparatesSparkQuota(t *testing.T) {
	parentID := int64(10)
	accountRepo := &usageAlertAccountRepoStub{accounts: map[int64]*Account{
		parentID: {
			ID:       parentID,
			Name:     "OpenAI Main",
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
		},
		20: {
			ID:              20,
			Name:            "OpenAI Main (Spark)",
			Platform:        PlatformOpenAI,
			Type:            AccountTypeOAuth,
			ParentAccountID: &parentID,
			QuotaDimension:  QuotaDimensionSpark,
		},
	}}
	alertRepo := &usageAlertRepoStub{}
	svc := NewUsageAlertService(alertRepo, accountRepo)

	realAccountID, usageType := svc.resolveUsageAlertScope(context.Background(), 20)

	require.Equal(t, int64(99), realAccountID)
	require.Equal(t, UsageAlertTypeSpark, usageType)
	require.Equal(t, parentID, alertRepo.ensuredAccountID)
	require.Equal(t, int64(99), alertRepo.attachedRealID)
	require.Equal(t, int64(20), alertRepo.attachedAccountID)
}

type rejectedUsageAlertSnapshotRepoStub struct {
	UsageAlertRepository
	previous      *UsageAlertSnapshot
	realAccount   *RealAccount
	upsertCalls   int
	listRuleCalls int
}

type usageAlertBoundaryRefreshRecorder struct {
	accountIDs []int64
	force      bool
	calls      int
}

func (r *usageAlertBoundaryRefreshRecorder) RefreshOpenAICodexUsageSnapshot(accountID int64, force bool) {
	r.accountIDs = append(r.accountIDs, accountID)
	r.force = force
	r.calls++
}

func (s *rejectedUsageAlertSnapshotRepoStub) GetRealAccount(_ context.Context, _ int64) (*RealAccount, error) {
	return s.realAccount, nil
}

func (s *rejectedUsageAlertSnapshotRepoStub) GetSnapshot(_ context.Context, _ int64, _ string) (*UsageAlertSnapshot, error) {
	return s.previous, nil
}

func (s *rejectedUsageAlertSnapshotRepoStub) UpsertSnapshot(_ context.Context, _ *UsageAlertSnapshot) (bool, error) {
	s.upsertCalls++
	return false, nil
}

func (s *rejectedUsageAlertSnapshotRepoStub) ListEnabledRules(_ context.Context, _ int64, _ string) ([]*UsageAlertRule, error) {
	s.listRuleCalls++
	return nil, nil
}

func TestObserveAsyncDoesNotEvaluateRejectedStaleSnapshot(t *testing.T) {
	now := time.Now().UTC()
	repo := &rejectedUsageAlertSnapshotRepoStub{
		previous: &UsageAlertSnapshot{SampledAt: now},
	}
	svc := NewUsageAlertService(repo, nil)

	retry := svc.observeAsync(UsageAlertSnapshot{
		AccountID:     2,
		RealAccountID: 10,
		UsageType:     UsageAlertTypeOverall,
		Platform:      UsageAlertPlatformAnthropic,
		SampledAt:     now.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5},
		},
	})

	require.False(t, retry)
	require.Equal(t, 2, repo.upsertCalls)
	require.Zero(t, repo.listRuleCalls)
}

func TestObserveAsyncRequestsForcedCodexRefreshAtWeeklyBoundary(t *testing.T) {
	boundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	weeklyReset := boundary.Add(7 * 24 * time.Hour)
	repo := &rejectedUsageAlertSnapshotRepoStub{
		previous: &UsageAlertSnapshot{
			AccountID:     82,
			RealAccountID: 10,
			Platform:      UsageAlertPlatformOpenAI,
			UsageType:     UsageAlertTypeOverall,
			SampledAt:     boundary.Add(-time.Minute),
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {
					ResetAt:    &weeklyReset,
					BoundaryAt: &boundary,
					Generation: 3,
				},
			},
		},
	}
	refresher := &usageAlertBoundaryRefreshRecorder{}
	svc := NewUsageAlertService(repo, nil)
	svc.SetOpenAICodexBoundaryRefresher(refresher)

	retry := svc.observeAsync(UsageAlertSnapshot{
		AccountID:     82,
		RealAccountID: 10,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Source:        UsageAlertSourceOpenAICodexHeaders,
		SampledAt:     boundary.Add(time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 4, RemainingPercent: 96, ResetAt: &weeklyReset},
		},
	})

	require.False(t, retry)
	require.Equal(t, 1, refresher.calls)
	require.Equal(t, []int64{82}, refresher.accountIDs)
	require.True(t, refresher.force)
	require.Zero(t, repo.upsertCalls)
	require.Zero(t, repo.listRuleCalls)
}

func TestObserveAsyncStillRequestsCodexRefreshAfterRepeatedCASRejection(t *testing.T) {
	boundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	sampledAt := boundary.Add(time.Minute)
	weeklyReset := sampledAt.Add(7 * 24 * time.Hour)
	fiveHourReset := sampledAt.Add(5 * time.Hour)
	repo := &rejectedUsageAlertSnapshotRepoStub{
		previous: &UsageAlertSnapshot{
			AccountID:     82,
			RealAccountID: 10,
			Platform:      UsageAlertPlatformOpenAI,
			UsageType:     UsageAlertTypeOverall,
			SampledAt:     boundary.Add(-time.Minute),
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow5h: {Generation: 2},
				UsageAlertWindow7d: {
					ResetAt:    &weeklyReset,
					BoundaryAt: &boundary,
					Generation: 8,
				},
			},
		},
	}
	refresher := &usageAlertBoundaryRefreshRecorder{}
	svc := NewUsageAlertService(repo, nil)
	svc.SetOpenAICodexBoundaryRefresher(refresher)

	retry := svc.observeAsync(UsageAlertSnapshot{
		AccountID:     82,
		RealAccountID: 10,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Source:        UsageAlertSourceOpenAICodexHeaders,
		SampledAt:     sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow5h: {UsedPercent: 30, RemainingPercent: 70, ResetAt: &fiveHourReset},
			UsageAlertWindow7d: {UsedPercent: 4, RemainingPercent: 96, ResetAt: &weeklyReset},
		},
	})

	require.False(t, retry)
	require.Equal(t, 2, repo.upsertCalls)
	require.Equal(t, 1, refresher.calls)
	require.Zero(t, repo.listRuleCalls)
}

func TestCodexBoundaryRefreshUsesLinkedAccountsAndThrottlesRetries(t *testing.T) {
	repo := &rejectedUsageAlertSnapshotRepoStub{realAccount: &RealAccount{Accounts: []*Account{
		{ID: 82, Platform: PlatformOpenAI},
		{ID: 83, Platform: PlatformOpenAI},
		{ID: 84, Platform: PlatformAnthropic},
	}}}
	refresher := &usageAlertBoundaryRefreshRecorder{}
	svc := NewUsageAlertService(repo, nil)
	svc.SetOpenAICodexBoundaryRefresher(refresher)
	snapshot := UsageAlertSnapshot{AccountID: 82, RealAccountID: 10}

	svc.requestCodex7dBoundaryRefresh(context.Background(), snapshot, true)
	svc.requestCodex7dBoundaryRefresh(context.Background(), snapshot, true)

	require.Equal(t, []int64{82, 83}, refresher.accountIDs)
	require.Equal(t, 2, refresher.calls)
	require.True(t, refresher.force)
}

func TestEnqueueObservationPreservesCodexGenerationEvidence(t *testing.T) {
	svc := NewUsageAlertService(nil, nil)
	worker := &usageAlertObservationWorker{running: true, wake: make(chan struct{}, 1)}
	svc.workers.Store("10:overall", worker)
	base := UsageAlertSnapshot{
		AccountID:     82,
		RealAccountID: 10,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Windows:       map[string]UsageAlertWindowSnapshot{UsageAlertWindow7d: {}},
	}
	manual := base
	manual.Source = UsageAlertSourceOpenAIQuotaReset
	authority := base
	authority.Source = UsageAlertSourceOpenAICodexUsageAPI
	headers := base
	headers.Source = UsageAlertSourceOpenAICodexHeaders

	svc.enqueueObservation(manual)
	svc.enqueueObservation(authority)
	svc.enqueueObservation(headers)

	worker.mu.Lock()
	defer worker.mu.Unlock()
	require.Equal(t, UsageAlertSourceOpenAIQuotaReset, worker.manualPending.Source)
	require.Equal(t, UsageAlertSourceOpenAICodexUsageAPI, worker.authorityPending.Source)
	require.Equal(t, UsageAlertSourceOpenAICodexHeaders, worker.pending.Source)
}

func TestPrepareUsageAlertSnapshotRejectsLateSampleFromOlderResetWindow(t *testing.T) {
	oldReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 5, RemainingPercent: 95, ResetAt: &newReset},
		},
	}
	current := UsageAlertSnapshot{
		SampledAt: oldReset.Add(time.Hour),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldReset},
		},
	}

	_, _, accepted, _ := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
}

func TestPrepareUsageAlertSnapshotKeepsFreshWindowAndPreservesNewerLinkedWindow(t *testing.T) {
	oldWeeklyReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newWeeklyReset := oldWeeklyReset.Add(7 * 24 * time.Hour)
	fiveHourReset := oldWeeklyReset.Add(5 * time.Hour)
	previousWeekly := UsageAlertWindowSnapshot{UsedPercent: 5, RemainingPercent: 95, ResetAt: &newWeeklyReset}
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{UsageAlertWindow7d: previousWeekly},
	}
	currentFiveHour := UsageAlertWindowSnapshot{UsedPercent: 25, RemainingPercent: 75, ResetAt: &fiveHourReset}
	current := UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow5h: currentFiveHour,
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldWeeklyReset},
		},
	}

	evaluation, persisted, accepted, _ := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.Equal(t, 1, len(evaluation.Windows))
	require.Equal(t, 25.0, evaluation.Windows[UsageAlertWindow5h].UsedPercent)
	require.Equal(t, int64(1), evaluation.Windows[UsageAlertWindow5h].Generation)
	require.Equal(t, evaluation.Windows[UsageAlertWindow5h], persisted.Windows[UsageAlertWindow5h])
	require.Equal(t, previousWeekly.ResetAt, persisted.Windows[UsageAlertWindow7d].ResetAt)
	require.Equal(t, int64(1), persisted.Windows[UsageAlertWindow7d].Generation)
}

func TestPrepareUsageAlertSnapshotRejectsUnknownGenerationAfterKnownReset(t *testing.T) {
	resetAt := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	previous := &UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 5, RemainingPercent: 95, ResetAt: &resetAt},
		},
	}
	current := UsageAlertSnapshot{
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5},
		},
	}

	_, _, accepted, _ := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
}

func TestPrepareCodex7dAcceptsResetAtDriftWithoutAdvancingGeneration(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	boundary := sampledAt.Add(24*time.Hour + 42*time.Minute)
	driftedReset := boundary.Add(-4 * time.Minute)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: sampledAt.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				UsedPercent: 19,
				ResetAt:     &boundary,
				BoundaryAt:  &boundary,
				Generation:  4,
			},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexHeaders,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 62, RemainingPercent: 38, ResetAt: &driftedReset},
		},
	}

	evaluation, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, 62.0, evaluation.Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, int64(4), persisted.Windows[UsageAlertWindow7d].Generation)
	require.Equal(t, &boundary, persisted.Windows[UsageAlertWindow7d].BoundaryAt)
	require.Equal(t, &driftedReset, persisted.Windows[UsageAlertWindow7d].ResetAt)
}

func TestPrepareCodex7dManualResetAdvancesGenerationAndKeepsOfficialBoundary(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	officialBoundary := sampledAt.Add(5 * time.Minute)
	manualResetAt := sampledAt.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: sampledAt.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {ResetAt: &officialBoundary, BoundaryAt: &officialBoundary, Generation: 7},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAIQuotaReset,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 0, RemainingPercent: 100, ResetAt: &manualResetAt},
		},
	}

	_, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
	weekly := persisted.Windows[UsageAlertWindow7d]

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(8), weekly.Generation)
	require.Equal(t, &officialBoundary, weekly.BoundaryAt)
	require.Equal(t, &manualResetAt, weekly.ResetAt)
	require.True(t, weekly.AwaitingOfficialReset)

	// Reconciliation retries are idempotent while the same official boundary is pending.
	retryPrevious := persisted
	_, retried, retryAccepted, _ := prepareUsageAlertSnapshot(&retryPrevious, current)
	require.True(t, retryAccepted)
	require.Equal(t, int64(8), retried.Windows[UsageAlertWindow7d].Generation)
}

func TestPrepareCodex7dRejectsOlderManualResetEvidence(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	boundary := sampledAt.Add(time.Hour)
	previousSampledAt := sampledAt.Add(time.Minute)
	previous := UsageAlertWindowSnapshot{
		ResetAt:    &boundary,
		BoundaryAt: &boundary,
		Generation: 7,
		SampledAt:  &previousSampledAt,
	}
	current := UsageAlertWindowSnapshot{
		ResetAt:   usageAlertTimePtr(sampledAt.Add(7 * 24 * time.Hour)),
		SampledAt: usageAlertTimePtr(sampledAt),
	}

	_, accepted, refresh := prepareCodex7dWindow(previous, current, UsageAlertSourceOpenAIQuotaReset, sampledAt)

	require.False(t, accepted)
	require.False(t, refresh)
}

func TestPrepareCodex7dOfficialResetWithinOneHourIsIgnored(t *testing.T) {
	manualAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	officialBoundary := manualAt.Add(5 * time.Minute)
	manualResetAt := manualAt.Add(7 * 24 * time.Hour)
	authoritativeAt := officialBoundary.Add(time.Minute)
	officialResetAt := authoritativeAt.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: manualAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				ResetAt:               &manualResetAt,
				BoundaryAt:            &officialBoundary,
				Generation:            8,
				AwaitingOfficialReset: true,
			},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexUsageAPI,
		SampledAt: authoritativeAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 0, RemainingPercent: 100, ResetAt: &officialResetAt},
		},
	}

	_, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
	weekly := persisted.Windows[UsageAlertWindow7d]

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(8), weekly.Generation)
	require.False(t, weekly.AwaitingOfficialReset)
}

func TestPrepareCodex7dOfficialResetAfterOneHourAdvancesGeneration(t *testing.T) {
	manualAt := time.Date(2026, 8, 13, 0, 11, 12, 0, time.FixedZone("UTC+8", 8*60*60))
	frozenBoundary := time.Date(2026, 8, 15, 11, 48, 46, 0, manualAt.Location())
	manualResetAt := time.Date(2026, 8, 20, 0, 11, 12, 0, manualAt.Location())
	officialAt := time.Date(2026, 8, 13, 11, 33, 17, 0, manualAt.Location())
	officialResetAt := time.Date(2026, 8, 20, 11, 33, 17, 0, manualAt.Location())
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: manualAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				ResetAt:               &manualResetAt,
				SampledAt:             &manualAt,
				BoundaryAt:            &frozenBoundary,
				Generation:            3,
				AwaitingOfficialReset: true,
			},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexUsageAPI,
		SampledAt: officialAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 17, RemainingPercent: 83, ResetAt: &officialResetAt},
		},
	}

	evaluation, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
	weekly := persisted.Windows[UsageAlertWindow7d]

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, 17.0, evaluation.Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, int64(4), weekly.Generation)
	require.Equal(t, &officialResetAt, weekly.BoundaryAt)
	require.False(t, weekly.AwaitingOfficialReset)
}

func TestPrepareCodex7dOfficialResetOneHourBoundary(t *testing.T) {
	manualAt := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	manualResetAt := manualAt.Add(7 * 24 * time.Hour)
	for _, tc := range []struct {
		name               string
		delta              time.Duration
		accepted           bool
		expectedGeneration int64
		expectedAwaiting   bool
	}{
		{name: "59 minutes", delta: 59 * time.Minute, accepted: true, expectedGeneration: 3, expectedAwaiting: true},
		{name: "exactly one hour", delta: time.Hour, accepted: true, expectedGeneration: 3, expectedAwaiting: true},
		{name: "after one hour", delta: time.Hour + time.Second, accepted: true, expectedGeneration: 4, expectedAwaiting: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := UsageAlertWindowSnapshot{
				ResetAt:               &manualResetAt,
				SampledAt:             &manualAt,
				BoundaryAt:            usageAlertTimePtr(manualAt.Add(2 * 24 * time.Hour)),
				OfficialResetAnchorAt: &manualResetAt,
				Generation:            3,
				AwaitingOfficialReset: true,
			}
			currentResetAt := manualResetAt.Add(tc.delta)
			current := UsageAlertWindowSnapshot{ResetAt: &currentResetAt, SampledAt: usageAlertTimePtr(manualAt.Add(tc.delta))}

			got, accepted, refresh := prepareCodex7dWindow(previous, current, UsageAlertSourceOpenAICodexUsageAPI, manualAt.Add(tc.delta))

			require.Equal(t, tc.accepted, accepted)
			require.False(t, refresh)
			require.Equal(t, tc.expectedGeneration, got.Generation)
			require.Equal(t, tc.expectedAwaiting, got.AwaitingOfficialReset)
		})
	}
}

func TestPrepareCodex7dOfficialResetIgnoreWindowDoesNotRatchetWithDrift(t *testing.T) {
	manualAt := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	manualResetAt := manualAt.Add(7 * 24 * time.Hour)
	previous := UsageAlertWindowSnapshot{
		ResetAt:               &manualResetAt,
		SampledAt:             &manualAt,
		BoundaryAt:            usageAlertTimePtr(manualAt.Add(2 * 24 * time.Hour)),
		OfficialResetAnchorAt: &manualResetAt,
		Generation:            3,
		AwaitingOfficialReset: true,
	}
	firstResetAt := manualResetAt.Add(40 * time.Minute)
	firstSampleAt := manualAt.Add(40 * time.Minute)
	first, accepted, refresh := prepareCodex7dWindow(
		previous,
		UsageAlertWindowSnapshot{ResetAt: &firstResetAt, SampledAt: &firstSampleAt},
		UsageAlertSourceOpenAICodexUsageAPI,
		firstSampleAt,
	)
	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(3), first.Generation)
	require.Equal(t, &manualResetAt, first.OfficialResetAnchorAt)

	secondResetAt := manualResetAt.Add(80 * time.Minute)
	secondSampleAt := manualAt.Add(80 * time.Minute)
	second, accepted, refresh := prepareCodex7dWindow(
		first,
		UsageAlertWindowSnapshot{ResetAt: &secondResetAt, SampledAt: &secondSampleAt},
		UsageAlertSourceOpenAICodexUsageAPI,
		secondSampleAt,
	)
	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(4), second.Generation)
	require.False(t, second.AwaitingOfficialReset)
	require.Nil(t, second.OfficialResetAnchorAt)
}

func TestPrepareCodex7dRejectsOlderAuthorityWhileAwaitingOfficialReset(t *testing.T) {
	manualAt := time.Date(2026, 8, 13, 0, 11, 12, 0, time.UTC)
	manualResetAt := manualAt.Add(7 * 24 * time.Hour)
	previous := UsageAlertWindowSnapshot{
		ResetAt:               &manualResetAt,
		SampledAt:             &manualAt,
		BoundaryAt:            usageAlertTimePtr(manualAt.Add(2 * 24 * time.Hour)),
		OfficialResetAnchorAt: &manualResetAt,
		Generation:            3,
		AwaitingOfficialReset: true,
	}
	olderSampleAt := manualAt.Add(-time.Minute)
	officialResetAt := manualResetAt.Add(11 * time.Hour)

	_, accepted, refresh := prepareCodex7dWindow(
		previous,
		UsageAlertWindowSnapshot{ResetAt: &officialResetAt, SampledAt: &olderSampleAt},
		UsageAlertSourceOpenAICodexUsageAPI,
		olderSampleAt,
	)

	require.False(t, accepted)
	require.False(t, refresh)
}

func TestPrepareCodex7dAuthorityRecoversAwaitingSnapshotWithoutResetAnchor(t *testing.T) {
	manualAt := time.Date(2026, 8, 13, 0, 11, 12, 0, time.UTC)
	previous := UsageAlertWindowSnapshot{
		SampledAt:             &manualAt,
		Generation:            3,
		AwaitingOfficialReset: true,
	}
	authoritativeAt := manualAt.Add(2 * time.Hour)
	officialResetAt := authoritativeAt.Add(7 * 24 * time.Hour)

	got, accepted, refresh := prepareCodex7dWindow(
		previous,
		UsageAlertWindowSnapshot{ResetAt: &officialResetAt, SampledAt: &authoritativeAt},
		UsageAlertSourceOpenAICodexUsageAPI,
		authoritativeAt,
	)

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(3), got.Generation)
	require.Equal(t, &officialResetAt, got.BoundaryAt)
	require.False(t, got.AwaitingOfficialReset)
}

func TestPrepareCodex7dRegularBoundaryUsesOneHourResetDriftWindow(t *testing.T) {
	boundary := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	previous := UsageAlertWindowSnapshot{
		ResetAt:    &boundary,
		SampledAt:  usageAlertTimePtr(boundary.Add(-time.Minute)),
		BoundaryAt: &boundary,
		Generation: 3,
	}
	for _, tc := range []struct {
		name               string
		delta              time.Duration
		expectedGeneration int64
	}{
		{name: "59 minutes", delta: 59 * time.Minute, expectedGeneration: 3},
		{name: "exactly one hour", delta: time.Hour, expectedGeneration: 3},
		{name: "after one hour", delta: time.Hour + time.Second, expectedGeneration: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAt := boundary.Add(tc.delta)
			sampledAt := boundary.Add(time.Minute)

			got, accepted, refresh := prepareCodex7dWindow(
				previous,
				UsageAlertWindowSnapshot{ResetAt: &resetAt, SampledAt: &sampledAt},
				UsageAlertSourceOpenAICodexUsageAPI,
				sampledAt,
			)

			require.True(t, accepted)
			require.False(t, refresh)
			require.Equal(t, tc.expectedGeneration, got.Generation)
			require.Equal(t, &resetAt, got.BoundaryAt)
		})
	}
}

func TestPrepareCodex7dRegularBoundaryAcceptsProductionResetHorizon(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	previousSampledAt := time.Date(2026, 9, 4, 0, 10, 3, 0, location)
	boundary := time.Date(2026, 9, 4, 0, 24, 45, 0, location)
	previousResetAt := time.Date(2026, 9, 7, 10, 28, 9, 0, location)
	authoritativeAt := time.Date(2026, 9, 4, 14, 36, 12, 0, location)
	authoritativeResetAt := time.Date(2026, 9, 7, 10, 26, 36, 0, location)
	previous := &UsageAlertSnapshot{
		RealAccountID: 3,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		SampledAt:     previousSampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				UsedPercent: 78,
				ResetAt:     &previousResetAt,
				SampledAt:   &previousSampledAt,
				BoundaryAt:  &boundary,
				Generation:  6,
			},
		},
	}
	current := UsageAlertSnapshot{
		RealAccountID: 3,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Source:        UsageAlertSourceOpenAICodexUsageAPI,
		SampledAt:     authoritativeAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				UsedPercent:      88,
				RemainingPercent: 12,
				ResetAt:          &authoritativeResetAt,
			},
		},
	}

	evaluation, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
	weekly := persisted.Windows[UsageAlertWindow7d]

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, 88.0, evaluation.Windows[UsageAlertWindow7d].UsedPercent)
	require.Equal(t, int64(6), weekly.Generation)
	require.Equal(t, &authoritativeResetAt, weekly.BoundaryAt)
	require.False(t, weekly.AwaitingOfficialReset)

	lastValue := 78.0
	stateRepo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:     UsageAlertStatusNormal,
		LastValue:      &lastValue,
		LastResetAt:    &previousResetAt,
		LastGeneration: 6,
	}}
	svc := NewUsageAlertService(stateRepo, nil)
	realAccountID := int64(3)
	step := 5.0
	rule := &UsageAlertRule{
		ID:            8,
		Platform:      UsageAlertPlatformOpenAI,
		RealAccountID: &realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Window:        UsageAlertWindow7d,
		Metric:        UsageAlertMetricUsed,
		Operator:      UsageAlertOperatorGTE,
		Threshold:     80,
		StepPercent:   &step,
		Enabled:       true,
	}

	triggers, err := svc.evaluateRules(context.Background(), previous, &evaluation, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.False(t, triggers[0].AccountReset)
	require.NotEqual(t, "new-generation", triggers[0].StateAnchor)
	require.Equal(t, 88.0, triggers[0].Value)
}

func TestPrepareCodex7dRegularBoundaryAdvancesPreannouncedFullCycle(t *testing.T) {
	boundary := time.Date(2026, 9, 7, 10, 26, 36, 0, time.UTC)
	preannouncedResetAt := boundary.Add(7 * 24 * time.Hour)
	previousSampledAt := boundary.Add(-time.Hour)
	authoritativeAt := boundary.Add(time.Minute)
	previous := &UsageAlertSnapshot{
		RealAccountID: 3,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		SampledAt:     previousSampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				UsedPercent: 88,
				ResetAt:     &preannouncedResetAt,
				SampledAt:   &previousSampledAt,
				BoundaryAt:  &boundary,
				Generation:  6,
			},
		},
	}
	current := UsageAlertSnapshot{
		RealAccountID: 3,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Source:        UsageAlertSourceOpenAICodexUsageAPI,
		SampledAt:     authoritativeAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				UsedPercent:      0,
				RemainingPercent: 100,
				ResetAt:          &preannouncedResetAt,
			},
		},
	}

	evaluation, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
	weekly := persisted.Windows[UsageAlertWindow7d]

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(7), weekly.Generation)
	require.Equal(t, &preannouncedResetAt, weekly.BoundaryAt)

	lastValue := 88.0
	stateRepo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:     UsageAlertStatusTriggered,
		LastValue:      &lastValue,
		LastResetAt:    &boundary,
		LastGeneration: 6,
	}}
	svc := NewUsageAlertService(stateRepo, nil)
	realAccountID := int64(3)
	step := 5.0
	rule := &UsageAlertRule{
		ID:            8,
		Platform:      UsageAlertPlatformOpenAI,
		RealAccountID: &realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Window:        UsageAlertWindow7d,
		Metric:        UsageAlertMetricUsed,
		Operator:      UsageAlertOperatorGTE,
		Threshold:     80,
		StepPercent:   &step,
		Enabled:       true,
	}

	triggers, err := svc.evaluateRules(context.Background(), previous, &evaluation, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.True(t, triggers[0].AccountReset)
	require.Equal(t, "new-generation", triggers[0].StateAnchor)
}

func TestPrepareCodex7dRegularBoundaryRejectsInvalidAuthority(t *testing.T) {
	boundary := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	previousSampledAt := boundary.Add(2 * time.Hour)
	previous := UsageAlertWindowSnapshot{
		ResetAt:    &boundary,
		SampledAt:  &previousSampledAt,
		BoundaryAt: &boundary,
		Generation: 3,
	}
	for _, tc := range []struct {
		name            string
		sampledAt       time.Time
		resetAt         time.Time
		expectedRefresh bool
	}{
		{
			name:            "older sample",
			sampledAt:       previousSampledAt.Add(-time.Minute),
			resetAt:         boundary.Add(7 * 24 * time.Hour),
			expectedRefresh: false,
		},
		{
			name:            "expired reset",
			sampledAt:       boundary.Add(3 * time.Hour),
			resetAt:         boundary.Add(2 * time.Hour),
			expectedRefresh: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, accepted, refresh := prepareCodex7dWindow(
				previous,
				UsageAlertWindowSnapshot{ResetAt: &tc.resetAt, SampledAt: &tc.sampledAt},
				UsageAlertSourceOpenAICodexUsageAPI,
				tc.sampledAt,
			)

			require.False(t, accepted)
			require.Equal(t, tc.expectedRefresh, refresh)
		})
	}
}

func TestPrepareCodex7dConfirmsOfficialResetWhenAuthorityArrivesDaysLate(t *testing.T) {
	boundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	previousReset := boundary
	authoritativeAt := boundary.Add(2 * 24 * time.Hour)
	officialResetAt := boundary.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: boundary.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {ResetAt: &previousReset, BoundaryAt: &boundary, Generation: 4},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexUsageAPI,
		SampledAt: authoritativeAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {ResetAt: &officialResetAt},
		},
	}

	_, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.False(t, refresh)
	require.Equal(t, int64(5), persisted.Windows[UsageAlertWindow7d].Generation)
}

func TestPrepareCodex7dRetriesUnconfirmedAuthority(t *testing.T) {
	boundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	manualResetAt := boundary.Add(7 * 24 * time.Hour)
	shortResetAt := boundary.Add(5 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: boundary.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				ResetAt:               &manualResetAt,
				BoundaryAt:            &boundary,
				Generation:            8,
				AwaitingOfficialReset: true,
			},
		},
	}
	for _, currentReset := range []*time.Time{nil, &shortResetAt} {
		current := UsageAlertSnapshot{
			Platform:  UsageAlertPlatformOpenAI,
			UsageType: UsageAlertTypeOverall,
			Source:    UsageAlertSourceOpenAICodexUsageAPI,
			SampledAt: boundary.Add(2 * time.Hour),
			Windows: map[string]UsageAlertWindowSnapshot{
				UsageAlertWindow7d: {ResetAt: currentReset},
			},
		}

		_, _, accepted, refresh := prepareUsageAlertSnapshot(previous, current)
		require.False(t, accepted)
		require.True(t, refresh)
	}
}

func TestPrepareCodex7dRejectsOlderSampleInSameGeneration(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	boundary := sampledAt.Add(24 * time.Hour)
	previousSampledAt := sampledAt.Add(time.Minute)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: previousSampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {ResetAt: &boundary, BoundaryAt: &boundary, Generation: 4, SampledAt: &previousSampledAt},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexHeaders,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {ResetAt: &boundary},
		},
	}

	_, _, accepted, _ := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
}

func TestPrepareCodex7dDueBoundaryRequestsAuthorityWithoutMaskingFreshFiveHour(t *testing.T) {
	boundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	sampledAt := boundary.Add(time.Minute)
	weeklyReset := sampledAt.Add(7 * 24 * time.Hour)
	fiveHourReset := sampledAt.Add(5 * time.Hour)
	previousWeekly := UsageAlertWindowSnapshot{
		UsedPercent:           3,
		RemainingPercent:      97,
		ResetAt:               &weeklyReset,
		BoundaryAt:            &boundary,
		Generation:            8,
		AwaitingOfficialReset: true,
	}
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: boundary.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow5h: {Generation: 2},
			UsageAlertWindow7d: previousWeekly,
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexHeaders,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow5h: {UsedPercent: 45, RemainingPercent: 55, ResetAt: &fiveHourReset},
			UsageAlertWindow7d: {UsedPercent: 4, RemainingPercent: 96, ResetAt: &weeklyReset},
		},
	}

	evaluation, persisted, accepted, refresh := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.True(t, refresh)
	require.Contains(t, evaluation.Windows, UsageAlertWindow5h)
	require.NotContains(t, evaluation.Windows, UsageAlertWindow7d)
	persistedWeekly := persisted.Windows[UsageAlertWindow7d]
	require.Equal(t, previousWeekly.ResetAt, persistedWeekly.ResetAt)
	require.Equal(t, previousWeekly.BoundaryAt, persistedWeekly.BoundaryAt)
	require.Equal(t, previousWeekly.Generation, persistedWeekly.Generation)
	require.True(t, persistedWeekly.AwaitingOfficialReset)
	require.Equal(t, previous.SampledAt, *persistedWeekly.SampledAt)
}

func TestPrepareCodex7dRejectsLatePreManualSample(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	boundary := sampledAt.Add(5 * time.Minute)
	manualResetAt := sampledAt.Add(7 * 24 * time.Hour)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: sampledAt.Add(-time.Second),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				ResetAt:               &manualResetAt,
				BoundaryAt:            &boundary,
				Generation:            8,
				AwaitingOfficialReset: true,
			},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexHeaders,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 90, RemainingPercent: 10, ResetAt: &boundary},
		},
	}

	_, _, accepted, refresh := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
	require.False(t, refresh)
}

func TestPrepareCodex7dRejectsDriftedOldHorizonWhileAwaitingOfficialReset(t *testing.T) {
	sampledAt := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	boundary := sampledAt.Add(5 * time.Minute)
	manualResetAt := sampledAt.Add(7 * 24 * time.Hour)
	driftedOldResetAt := boundary.Add(3 * time.Minute)
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		SampledAt: sampledAt.Add(-time.Second),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {
				ResetAt:               &manualResetAt,
				BoundaryAt:            &boundary,
				Generation:            8,
				AwaitingOfficialReset: true,
			},
		},
	}
	current := UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Source:    UsageAlertSourceOpenAICodexHeaders,
		SampledAt: sampledAt,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 90, RemainingPercent: 10, ResetAt: &driftedOldResetAt},
		},
	}

	_, _, accepted, refresh := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
	require.False(t, refresh)
}

func TestEvaluateCodex7dUsesGenerationInsteadOfResetAtDrift(t *testing.T) {
	previousReset := time.Date(2026, 8, 8, 11, 42, 0, 0, time.UTC)
	driftedReset := previousReset.Add(-4 * time.Minute)
	lastValue := 19.0
	repo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:     UsageAlertStatusNormal,
		LastValue:      &lastValue,
		LastResetAt:    &previousReset,
		LastGeneration: 4,
	}}
	svc := NewUsageAlertService(repo, nil)
	realAccountID := int64(1)
	step := 20.0
	rule := &UsageAlertRule{
		ID:            7,
		Platform:      UsageAlertPlatformOpenAI,
		RealAccountID: &realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Window:        UsageAlertWindow7d,
		Metric:        UsageAlertMetricUsed,
		Operator:      UsageAlertOperatorGTE,
		Threshold:     20,
		StepPercent:   &step,
		Enabled:       true,
	}
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 19, ResetAt: &previousReset, Generation: 4},
		},
	}
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 62, RemainingPercent: 38, ResetAt: &driftedReset, Generation: 4},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), previous, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.Equal(t, 62.0, triggers[0].Value)
	require.NotEqual(t, "new-generation", triggers[0].StateAnchor)
}

func TestEvaluateCodex7dRearmsLegacyStateAfterManualGenerationAdvance(t *testing.T) {
	officialBoundary := time.Date(2026, 8, 7, 11, 5, 0, 0, time.UTC)
	manualResetAt := officialBoundary.Add(7 * 24 * time.Hour)
	lastValue := 20.0
	lastTriggeredAt := officialBoundary.Add(-time.Hour)
	repo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
		LastResetAt:     &officialBoundary,
		LastGeneration:  0,
	}}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	rule.CooldownMinutes = 0
	previous := &UsageAlertSnapshot{
		Platform:  UsageAlertPlatformOpenAI,
		UsageType: UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {RemainingPercent: 10, ResetAt: &officialBoundary},
		},
	}
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {RemainingPercent: 10, ResetAt: &manualResetAt, Generation: 2},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), previous, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 2)
	require.True(t, triggers[0].AccountReset)
	require.True(t, triggers[0].Resolved)
	require.Equal(t, "new-generation", triggers[1].StateAnchor)
	require.False(t, triggers[1].Resolved)
}

func TestEvaluateCodex7dLegacyStateKeepsResetTriggerAcrossSnapshotReplay(t *testing.T) {
	oldReset := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	repo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:     UsageAlertStatusNormal,
		LastResetAt:    &oldReset,
		LastGeneration: 0,
	}}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		Platform:      UsageAlertPlatformOpenAI,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {RemainingPercent: 90, ResetAt: &newReset, Generation: 2},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), current, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.True(t, triggers[0].AccountReset)
}

func TestUsageAlertEventIDIsStableAcrossResetAtDriftWithinGeneration(t *testing.T) {
	firstReset := time.Date(2026, 8, 8, 11, 42, 0, 0, time.UTC)
	secondReset := firstReset.Add(-4 * time.Minute)
	realAccountID := int64(1)
	snapshot := &UsageAlertSnapshot{RealAccountID: 1, UsageType: UsageAlertTypeOverall}
	rule := &UsageAlertRule{
		ID:            7,
		RealAccountID: &realAccountID,
		UsageType:     UsageAlertTypeOverall,
		Window:        UsageAlertWindow7d,
		Metric:        UsageAlertMetricUsed,
		Operator:      UsageAlertOperatorGTE,
		Threshold:     20,
	}
	first := UsageAlertTrigger{
		Rule:        rule,
		Window:      UsageAlertWindow7d,
		WindowState: UsageAlertWindowSnapshot{ResetAt: &firstReset, Generation: 4},
		StateAnchor: "same-state",
	}
	second := first
	second.WindowState.ResetAt = &secondReset

	firstEvent := buildUsageAlertWebhookEvent(snapshot, nil, first)
	secondEvent := buildUsageAlertWebhookEvent(snapshot, nil, second)

	require.Equal(t, firstEvent.EventID, secondEvent.EventID)
}

type usageAlertGenerationStateRepoStub struct {
	UsageAlertRepository
	state             *UsageAlertState
	getStateErr       error
	lastUpsertedState *UsageAlertState
	upsertStateCalls  int
}

func (s *usageAlertGenerationStateRepoStub) GetState(_ context.Context, _, _ int64, _, _ string) (*UsageAlertState, error) {
	return s.state, s.getStateErr
}

func (s *usageAlertGenerationStateRepoStub) UpsertState(_ context.Context, state *UsageAlertState) error {
	s.upsertStateCalls++
	s.lastUpsertedState = state
	return nil
}

func TestEvaluateRulesRejectsWindowOlderThanPersistedStateReset(t *testing.T) {
	oldReset := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	repo := &usageAlertGenerationStateRepoStub{
		state: &UsageAlertState{LastStatus: UsageAlertStatusNormal, LastResetAt: &newReset},
	}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5, ResetAt: &oldReset},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), nil, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Empty(t, triggers)
	require.Zero(t, repo.upsertStateCalls)
}

func TestEvaluateRulesTreatsNewResetGenerationAsFresh(t *testing.T) {
	oldReset := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	newReset := oldReset.Add(7 * 24 * time.Hour)
	lastValue := 10.0
	lastTriggeredAt := time.Now().UTC()
	repo := &usageAlertGenerationStateRepoStub{state: &UsageAlertState{
		LastStatus:      UsageAlertStatusTriggered,
		LastTriggeredAt: &lastTriggeredAt,
		LastValue:       &lastValue,
		LastResetAt:     &oldReset,
	}}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	rule.CooldownMinutes = 0
	previous := &UsageAlertSnapshot{Windows: map[string]UsageAlertWindowSnapshot{
		UsageAlertWindow7d: {RemainingPercent: 10, ResetAt: &oldReset},
	}}
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {RemainingPercent: 10, ResetAt: &newReset},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), previous, current, []*UsageAlertRule{rule})

	require.NoError(t, err)
	require.Len(t, triggers, 1)
	require.False(t, triggers[0].AccountReset)
	require.False(t, triggers[0].Resolved)
	require.Equal(t, "new-generation", triggers[0].StateAnchor)
}

func TestEvaluateRulesFailsClosedWhenStateReadFails(t *testing.T) {
	repo := &usageAlertGenerationStateRepoStub{getStateErr: errors.New("database unavailable")}
	svc := NewUsageAlertService(repo, nil)
	rule := validUsageAlertRuleForTest()
	rule.ID = 7
	current := &UsageAlertSnapshot{
		RealAccountID: 1,
		UsageType:     UsageAlertTypeOverall,
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {RemainingPercent: 10},
		},
	}

	triggers, err := svc.evaluateRules(context.Background(), nil, current, []*UsageAlertRule{rule})

	require.ErrorContains(t, err, "get state for rule 7")
	require.Empty(t, triggers)
	require.Zero(t, repo.upsertStateCalls)
}

func validUsageAlertRuleForTest() *UsageAlertRule {
	realAccountID := int64(1)
	return &UsageAlertRule{
		Name:            "weekly remaining low",
		Platform:        UsageAlertPlatformOpenAI,
		UsageType:       UsageAlertTypeOverall,
		RealAccountID:   &realAccountID,
		Window:          UsageAlertWindow7d,
		Metric:          UsageAlertMetricRemaining,
		Operator:        UsageAlertOperatorLTE,
		Threshold:       20,
		CooldownMinutes: 60,
		Enabled:         true,
	}
}
