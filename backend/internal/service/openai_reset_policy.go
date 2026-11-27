package service

import (
	"context"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const (
	openAIResetRetryInterval               = 10 * time.Minute
	openAIResetIdempotencyExecutionTimeout = 45 * time.Second
)

func openAIResetWeeklyThresholdReached(utilization, threshold float64) bool {
	return !math.IsNaN(utilization) && !math.IsInf(utilization, 0) &&
		utilization > 0.20 && utilization <= 1 && utilization >= threshold
}

func openAIResetNotSent(err error) bool {
	switch infraerrors.Reason(err) {
	case "OPENAI_RESET_COOLDOWN", "OPENAI_RESET_USAGE_TOO_LOW", "OPENAI_RESET_LOCK_UNAVAILABLE", "OPENAI_RESET_BUSY", "OPENAI_RESET_RESERVED":
		return true
	}
	return false
}

// This is the administrator's spending policy, not a claim about upstream
// eligibility. Only the overall 7d window counts; 5h and Spark cannot authorize it.
func openAIResetUsageEligible(usage *OpenAIQuotaUsage, now time.Time) bool {
	if usage == nil || usage.RateLimit == nil {
		return false
	}
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil || window.LimitWindowSeconds != 604800 {
			continue
		}
		if math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) ||
			window.UsedPercent <= 20 || window.UsedPercent > 100 {
			continue
		}
		if window.ResetAt > 0 && window.ResetAt <= now.Unix() {
			continue
		}
		return true
	}
	return false
}

func openAIResetOutcome(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "ok", "success":
		return "ok"
	case "no_credit", "nocredit":
		return "no_credit"
	case "nothing_to_reset", "nothingtoreset":
		return "nothing_to_reset"
	default:
		return "unknown"
	}
}

// IsOpenAIQuotaResetConfirmedSuccess reports whether an upstream consume code
// explicitly confirms a successful credit redemption.
func IsOpenAIQuotaResetConfirmedSuccess(code string) bool {
	return openAIResetOutcome(code) == "ok"
}

func openAIUsageConfirmsSchedulable(usage *OpenAIQuotaUsage, pauseReached bool, now time.Time) bool {
	if pauseReached || usage == nil || usage.RateLimit == nil {
		return false
	}
	if !usage.RateLimit.Allowed || usage.RateLimit.LimitReached {
		return false
	}
	nowUnix := now.Unix()
	hasValidWindow := false
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 {
			return false
		}
		resetAt := window.ResetAt
		if resetAt <= 0 && window.ResetAfterSeconds > 0 {
			base := usage.FetchedAt
			if base <= 0 {
				base = nowUnix
			}
			resetAt = base + window.ResetAfterSeconds
		}
		if resetAt <= nowUnix {
			continue
		}
		if window.UsedPercent >= 100 {
			return false
		}
		hasValidWindow = true
	}
	return hasValidWindow
}

type openAIObservedQuotaRateLimitRecoverer interface {
	ClearObservedOpenAIQuotaRateLimit(ctx context.Context, observed *Account) error
}

func maybeRecoverOpenAIQuotaBlocks(ctx context.Context, observed *Account, recoverer openAIAutoResetRecovery) error {
	if recoverer == nil || observed == nil || observed.Status == StatusError ||
		(observed.RateLimitedAt == nil && observed.RateLimitResetAt == nil) {
		return nil
	}
	narrow, ok := recoverer.(openAIObservedQuotaRateLimitRecoverer)
	if !ok {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return narrow.ClearObservedOpenAIQuotaRateLimit(recoveryCtx, observed)
}

func openAIResetNoopError() error {
	return infraerrors.Conflict("OPENAI_RESET_NOTHING_TO_RESET", "upstream has no eligible window to reset; no reset was performed")
}

type openAIResetUsageContextKey struct{}

type openAIResetUsageEvidence struct {
	accountID int64
	usage     *OpenAIQuotaUsage
}

func withOpenAIResetUsage(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage) context.Context {
	return context.WithValue(ctx, openAIResetUsageContextKey{}, openAIResetUsageEvidence{accountID, usage})
}

type openAIResetDispatchContextKey struct{}

// withOpenAIResetDispatchTracker attaches a boolean pointer tracking whether
// ResetCredit / ResetCreditTargeted reached the upstream HTTP POST call.
// Callers initialize *dispatched = true inside the idempotency callback as a
// fail-closed default for unit-test stubs; OpenAIQuotaService.resetCredit
// resets *dispatched = false on entry and flips it to true immediately before
// the first HTTP POST.
func withOpenAIResetDispatchTracker(ctx context.Context, dispatched *bool) context.Context {
	if dispatched == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIResetDispatchContextKey{}, dispatched)
}

func setOpenAIResetDispatched(ctx context.Context, value bool) {
	if ctx == nil {
		return
	}
	if flag, ok := ctx.Value(openAIResetDispatchContextKey{}).(*bool); ok && flag != nil {
		*flag = value
	}
}

// Called under the account/identity lease by every real redemption path.
// The cooldown key is intentionally NOT released with that lease: it limits
// attempts across workers, aliases, manual actions and application restarts.
func (s *OpenAIResetReservationService) claimConsumptionInterval(ctx context.Context, accountID int64) error {
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	identity := openAIResetIdentity(account)
	if identity == "" || s.locks == nil {
		return infraerrors.ServiceUnavailable("OPENAI_RESET_LOCK_UNAVAILABLE", "reset coordination is unavailable")
	}
	ok, err := s.locks.TryAcquireLeaderLock(ctx, "openai:reset:cooldown:"+identity, uuid.NewString(), openAIResetRetryInterval)
	if err != nil {
		return infraerrors.ServiceUnavailable("OPENAI_RESET_LOCK_UNAVAILABLE", "reset coordination is unavailable")
	}
	if !ok {
		return infraerrors.Conflict("OPENAI_RESET_COOLDOWN", "reset attempts must be at least 10 minutes apart").
			WithMetadata(map[string]string{"retry_after": "600"})
	}
	return nil
}
