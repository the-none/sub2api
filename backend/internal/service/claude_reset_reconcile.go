package service

import (
	"context"
	"log/slog"
	"slices"
	"time"
)

// Local reconciliation after a confirmed Claude reset redemption.
//
// Upstream Redeem only reports the outcome. Without this step the account keeps
// the 429 cooldown of the window that was just reset (so it stays unschedulable
// until the original reset time) and the usage cache keeps serving pre-reset
// numbers. Recovery is driven by a fresh authoritative usage query, never by the
// cleared window names alone: a 5h-only reset must not lift a 7d or Fable limit
// that is still exhausted.

const (
	claudeResetReconcileTimeout = 20 * time.Second
	claudeResetFableClearReason = "anthropic_claude_reset_redeemed"
)

// claudeResetReconcileFunc runs after an upstream "reset" outcome has been
// persisted. It must be best effort: the credit is already consumed.
type claudeResetReconcileFunc func(ctx context.Context, accountID int64, cleared []string)

// SetResetReconciler installs the post-reset reconciliation hook.
func (s *ClaudeResetCreditService) SetResetReconciler(fn claudeResetReconcileFunc) {
	s.reconcileReset = fn
}

func (s *ClaudeResetCreditService) reconcileAfterReset(ctx context.Context, accountID int64, outcome *ClaudeResetOutcome) {
	if s.reconcileReset == nil || outcome == nil || outcome.Outcome != ClaudeResetOutcomeReset || len(outcome.Cleared) == 0 {
		return
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeResetReconcileTimeout)
	defer cancel()
	s.reconcileReset(reconcileCtx, accountID, slices.Clone(outcome.Cleared))
}

func newClaudeResetReconciler(usage *AccountUsageService, rateLimit *RateLimitService) claudeResetReconcileFunc {
	if usage == nil || rateLimit == nil {
		return nil
	}
	return func(ctx context.Context, accountID int64, cleared []string) {
		info, err := usage.RefreshClaudeUsageAfterReset(ctx, accountID)
		if err != nil || info == nil {
			slog.Warn("claude_reset_reconcile_usage_refresh_failed", "account_id", accountID, "error", err)
			return
		}
		if err := rateLimit.ReconcileClaudeReset(ctx, accountID, cleared, info); err != nil {
			slog.Warn("claude_reset_reconcile_failed", "account_id", accountID, "error", err)
		}
	}
}

// RefreshClaudeUsageAfterReset drops the cached /api/oauth/usage response and
// queries it again, so the passive snapshot and usage alerts see the post-reset
// windows immediately.
func (s *AccountUsageService) RefreshClaudeUsageAfterReset(ctx context.Context, accountID int64) (*UsageInfo, error) {
	if s.cache != nil {
		s.cache.apiCache.Delete(accountID)
	}
	return s.GetUsage(ctx, accountID, true)
}

// ReconcileClaudeReset lifts the local blocks that a confirmed reset removed,
// judged by a fresh authoritative usage snapshot:
//   - the account-level 429 cooldown (5h/7d) only when five_hour or seven_day
//     was cleared and neither window is still at its limit;
//   - the Fable 7d_oi model limit only when seven_day_overage_included was
//     cleared and the fresh Fable window is below its limit.
//
// Temporary-unschedulable, overload-only, and admin threshold pauses are left alone.
func (s *RateLimitService) ReconcileClaudeReset(ctx context.Context, accountID int64, cleared []string, usage *UsageInfo) error {
	if s == nil || s.accountRepo == nil || usage == nil {
		return nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	if account.Platform != PlatformAnthropic || account.Type != AccountTypeOAuth {
		return nil
	}
	now := time.Now()
	lifted := false

	accountWindowCleared := slices.Contains(cleared, "five_hour") || slices.Contains(cleared, "seven_day")
	if accountWindowCleared && account.IsRateLimited() &&
		claudeResetWindowBelowLimit(usage.FiveHour, false) && claudeResetWindowBelowLimit(usage.SevenDay, true) {
		if err := s.accountRepo.ClearRateLimit(ctx, accountID); err != nil {
			return err
		}
		lifted = true
		slog.Info("claude_reset_account_rate_limit_cleared", "account_id", accountID, "cleared", cleared)
	}

	if slices.Contains(cleared, "seven_day_overage_included") && account.isRateLimitActiveForKey(anthropicFableRateLimitKey) &&
		claudeResetWindowBelowLimit(usage.SevenDayFable, false) {
		// No single-scope delete exists; an already-expired entry is inert.
		if err := s.accountRepo.SetModelRateLimit(ctx, accountID, anthropicFableRateLimitKey, now, claudeResetFableClearReason); err != nil {
			return err
		}
		lifted = true
		slog.Info("claude_reset_fable_rate_limit_cleared", "account_id", accountID)
	}

	if lifted {
		s.notifyAccountSchedulingBlockCleared(accountID)
	}
	return nil
}

// claudeResetWindowBelowLimit reports whether a fresh usage window proves the
// limit is gone. A missing window counts as below the limit only when
// missingOK is set (the usage API omits seven_day when it has no reset time).
func claudeResetWindowBelowLimit(w *UsageProgress, missingOK bool) bool {
	if w == nil {
		return missingOK
	}
	return w.Utilization < 100
}
