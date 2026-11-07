package service

import (
	"context"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const openAIResetRetryInterval = 10 * time.Minute

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
