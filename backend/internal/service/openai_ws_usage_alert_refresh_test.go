package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexUsageSnapshotRefreshRecorder struct {
	calls chan bool
}

func (r *codexUsageSnapshotRefreshRecorder) RefreshOpenAICodexUsageSnapshot(_ int64, force bool) {
	r.calls <- force
}

func TestOpenAIGatewayService_WSTurnSchedulesAuthoritativeUsageRefresh(t *testing.T) {
	refresher := &codexUsageSnapshotRefreshRecorder{calls: make(chan bool, 1)}
	svc := &OpenAIGatewayService{usageRefresher: refresher}
	staleHandshakeHeaders := make(http.Header)
	staleHandshakeHeaders.Set("x-codex-primary-used-percent", "20")

	svc.UpdateCodexUsageSnapshotFromResult(context.Background(), 81, &OpenAIForwardResult{
		OpenAIWSMode:    true,
		ResponseHeaders: staleHandshakeHeaders,
	})

	select {
	case force := <-refresher.calls:
		require.False(t, force)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WS quota refresh request")
	}
}

func TestOpenAIGatewayService_SparkRequestSchedulesAuthoritativeUsageRefresh(t *testing.T) {
	refresher := &codexUsageSnapshotRefreshRecorder{calls: make(chan bool, 1)}
	svc := &OpenAIGatewayService{usageRefresher: refresher}
	parentID := int64(80)
	shadow := &Account{ID: 81, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}

	svc.UpdateCodexUsageSnapshotForAccount(context.Background(), shadow, &OpenAIForwardResult{
		ResponseHeaders: http.Header{"x-codex-primary-used-percent": []string{"99"}},
	})

	select {
	case force := <-refresher.calls:
		require.False(t, force)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Spark quota refresh request")
	}
}

func TestAccountUsageService_WSUsageRefreshIsThrottledAndForceable(t *testing.T) {
	updatesCh := make(chan map[string]any, 2)
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{{ID: 82}}},
		updateExtraCalls:      updatesCh,
	}
	var fetchCalls atomic.Int32
	svc := &AccountUsageService{
		accountRepo: repo,
		cache:       NewUsageCache(),
		openAIQuotaSnapshotFn: func(context.Context, int64) (*OpenAIQuotaUsage, error) {
			fetchCalls.Add(1)
			return &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        42,
					LimitWindowSeconds: 7 * 24 * 60 * 60,
					ResetAfterSeconds:  24 * 60 * 60,
				},
				SecondaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        12,
					LimitWindowSeconds: 5 * 60 * 60,
					ResetAfterSeconds:  60 * 60,
				},
			}}, nil
		},
	}

	svc.RefreshOpenAICodexUsageSnapshot(82, false)
	select {
	case updates := <-updatesCh:
		require.Equal(t, 42.0, updates["codex_7d_used_percent"])
		require.Equal(t, 12.0, updates["codex_5h_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for authoritative WS usage snapshot")
	}

	svc.RefreshOpenAICodexUsageSnapshot(82, false)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), fetchCalls.Load(), "normal WS turns must share the quota refresh TTL")

	svc.RefreshOpenAICodexUsageSnapshot(82, true)
	select {
	case <-updatesCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forced terminal quota refresh")
	}
	require.Equal(t, int32(2), fetchCalls.Load())
}

func TestAccountUsageService_WSUsageRefreshRetriesAfterFailure(t *testing.T) {
	updatesCh := make(chan map[string]any, 1)
	firstAttempt := make(chan struct{}, 1)
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{{ID: 84}}},
		updateExtraCalls:      updatesCh,
	}
	var fetchCalls atomic.Int32
	svc := &AccountUsageService{
		accountRepo: repo,
		cache:       NewUsageCache(),
		openAIQuotaSnapshotFn: func(context.Context, int64) (*OpenAIQuotaUsage, error) {
			if fetchCalls.Add(1) == 1 {
				firstAttempt <- struct{}{}
				return nil, errors.New("temporary upstream failure")
			}
			return &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        42,
					LimitWindowSeconds: 7 * 24 * 60 * 60,
					ResetAfterSeconds:  24 * 60 * 60,
				},
			}}, nil
		},
	}

	svc.RefreshOpenAICodexUsageSnapshot(84, false)
	select {
	case <-firstAttempt:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed quota refresh")
	}
	require.Eventually(t, func() bool {
		_, inFlight := svc.cache.openAIProbeFlight.Load(int64(84))
		return !inFlight
	}, time.Second, 10*time.Millisecond)

	svc.RefreshOpenAICodexUsageSnapshot(84, false)
	select {
	case <-updatesCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quota refresh retry")
	}
	require.Equal(t, int32(2), fetchCalls.Load())
}

func TestAccountUsageService_ForceDuringFlightSchedulesFollowupProbe(t *testing.T) {
	updatesCh := make(chan map[string]any, 2)
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{{ID: 85}}},
		updateExtraCalls:      updatesCh,
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var fetchCalls atomic.Int32
	svc := &AccountUsageService{
		accountRepo: repo,
		cache:       NewUsageCache(),
		openAIQuotaSnapshotFn: func(context.Context, int64) (*OpenAIQuotaUsage, error) {
			if fetchCalls.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        42,
					LimitWindowSeconds: 7 * 24 * 60 * 60,
					ResetAfterSeconds:  24 * 60 * 60,
				},
			}}, nil
		},
	}

	svc.RefreshOpenAICodexUsageSnapshot(85, false)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first quota refresh")
	}
	svc.RefreshOpenAICodexUsageSnapshot(85, true)
	close(releaseFirst)

	require.Eventually(t, func() bool {
		return fetchCalls.Load() == 2
	}, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		_, pending := svc.cache.openAIProbeForced.Load(int64(85))
		_, inFlight := svc.cache.openAIProbeFlight.Load(int64(85))
		return !pending && !inFlight
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAccountUsageService_SparkRefreshPersistsBengalfoxWindows(t *testing.T) {
	parentID := int64(86)
	shadow := Account{
		ID:              87,
		ParentAccountID: &parentID,
		QuotaDimension:  QuotaDimensionSpark,
	}
	updatesCh := make(chan map[string]any, 1)
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{shadow}},
		updateExtraCalls:      updatesCh,
	}
	svc := &AccountUsageService{
		accountRepo: repo,
		cache:       NewUsageCache(),
		openAIQuotaSnapshotFn: func(context.Context, int64) (*OpenAIQuotaUsage, error) {
			return &OpenAIQuotaUsage{
				RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        10,
					LimitWindowSeconds: 7 * 24 * 60 * 60,
				}},
				AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
					MeteredFeature: "codex_bengalfox",
					RateLimit: &OpenAIRateLimit{
						PrimaryWindow: &OpenAIRateLimitWindow{
							UsedPercent:        77,
							LimitWindowSeconds: 5 * 60 * 60,
							ResetAfterSeconds:  60 * 60,
						},
						SecondaryWindow: &OpenAIRateLimitWindow{
							UsedPercent:        55,
							LimitWindowSeconds: 7 * 24 * 60 * 60,
							ResetAfterSeconds:  24 * 60 * 60,
						},
					},
				}},
			}, nil
		},
	}

	svc.RefreshOpenAICodexUsageSnapshot(shadow.ID, false)

	select {
	case updates := <-updatesCh:
		require.Equal(t, 77.0, updates["codex_5h_used_percent"])
		require.Equal(t, 55.0, updates["codex_7d_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Spark usage snapshot")
	}
}

func TestAccountUsageService_RefreshFailsClosedWhenAccountLookupFails(t *testing.T) {
	updatesCh := make(chan map[string]any, 1)
	repo := &snapshotUpdateAccountRepo{updateExtraCalls: updatesCh}
	svc := &AccountUsageService{
		accountRepo: repo,
		cache:       NewUsageCache(),
		openAIQuotaSnapshotFn: func(context.Context, int64) (*OpenAIQuotaUsage, error) {
			return &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        42,
					LimitWindowSeconds: 7 * 24 * 60 * 60,
				},
			}}, nil
		},
	}

	svc.RefreshOpenAICodexUsageSnapshot(88, false)

	require.Eventually(t, func() bool {
		_, inFlight := svc.cache.openAIProbeFlight.Load(int64(88))
		return !inFlight
	}, 2*time.Second, 10*time.Millisecond)
	select {
	case <-updatesCh:
		t.Fatal("unknown account dimension must not persist an overall snapshot")
	default:
	}
}

func TestOpenAIGatewayService_WSRateLimitForcesAuthoritativeUsageRefresh(t *testing.T) {
	repo := &openAIWSRateLimitSignalRepo{}
	refresher := &codexUsageSnapshotRefreshRecorder{calls: make(chan bool, 1)}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
		usageRefresher:   refresher,
	}
	account := &Account{ID: 83, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-primary-reset-after-seconds", "3600")

	svc.persistOpenAIWSRateLimitSignal(
		context.Background(),
		account,
		headers,
		[]byte(`{"error":{"type":"usage_limit_reached"}}`),
		"usage_limit_reached",
		"usage_limit_error",
		"usage limit reached",
	)

	select {
	case force := <-refresher.calls:
		require.True(t, force)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal WS quota refresh")
	}
}
