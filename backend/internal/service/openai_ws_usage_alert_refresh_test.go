package service

import (
	"context"
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

func TestAccountUsageService_WSUsageRefreshIsThrottledAndForceable(t *testing.T) {
	updatesCh := make(chan map[string]any, 2)
	repo := &snapshotUpdateAccountRepo{updateExtraCalls: updatesCh}
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
