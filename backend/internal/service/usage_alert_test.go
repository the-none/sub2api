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
	rule     *UsageAlertRule
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

func (s *usageAlertDeliveryRepoStub) GetState(_ context.Context, _, _ int64, _, _ string) (*UsageAlertState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *usageAlertDeliveryRepoStub) UpsertState(_ context.Context, state *UsageAlertState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	upsertCalls   int
	listRuleCalls int
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

	svc.observeAsync(UsageAlertSnapshot{
		AccountID:     2,
		RealAccountID: 10,
		UsageType:     UsageAlertTypeOverall,
		Platform:      UsageAlertPlatformAnthropic,
		SampledAt:     now.Add(-time.Minute),
		Windows: map[string]UsageAlertWindowSnapshot{
			UsageAlertWindow7d: {UsedPercent: 95, RemainingPercent: 5},
		},
	})

	require.Equal(t, 1, repo.upsertCalls)
	require.Zero(t, repo.listRuleCalls)
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

	_, _, accepted := prepareUsageAlertSnapshot(previous, current)

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

	evaluation, persisted, accepted := prepareUsageAlertSnapshot(previous, current)

	require.True(t, accepted)
	require.Equal(t, map[string]UsageAlertWindowSnapshot{UsageAlertWindow5h: currentFiveHour}, evaluation.Windows)
	require.Equal(t, currentFiveHour, persisted.Windows[UsageAlertWindow5h])
	require.Equal(t, previousWeekly, persisted.Windows[UsageAlertWindow7d])
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

	_, _, accepted := prepareUsageAlertSnapshot(previous, current)

	require.False(t, accepted)
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
