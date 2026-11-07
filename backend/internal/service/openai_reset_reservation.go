package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const openAIResetReservationCutoff = 90 * time.Second

// "code" is redacted by the generic idempotency store as an OAuth secret key.
// Store the verified, non-sensitive outcome under a distinct field.
type openAIResetReservationReceipt struct {
	Outcome      string `json:"outcome"`
	WindowsReset int    `json:"windows_reset"`
}

func decodeReservationReceipt(data any) (openAIResetReservationReceipt, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return openAIResetReservationReceipt{}, err
	}
	var result openAIResetReservationReceipt
	err = json.Unmarshal(raw, &result)
	return result, err
}

// OpenAIResetReservation contains only hashes of upstream identifiers. It is
// persisted independently of account configuration and never exported/imported.
type OpenAIResetReservation struct {
	ID            string     `json:"id"`
	AccountID     int64      `json:"account_id"`
	IdentityHash  string     `json:"identity_hash"`
	CreditHash    string     `json:"credit_hash"`
	Status        string     `json:"status"`
	Version       int64      `json:"version"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ExecuteAt     time.Time  `json:"execute_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	AttemptedAt   *time.Time `json:"attempted_at,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	NextCheckAt   *time.Time `json:"next_check_at,omitempty"`
	// False for old records and after ambiguous/crashed attempts. A later
	// no-op must never erase uncertainty about an earlier request.
	OnlyNoopAttempts bool       `json:"only_noop_attempts,omitempty"`
	ConsumedAt       *time.Time `json:"consumed_at,omitempty"`
	WindowsReset     int        `json:"windows_reset"`
	ErrorCode        string     `json:"error_code,omitempty"`
}

type OpenAIResetReservationView struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	ExecuteAt    time.Time  `json:"execute_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	AttemptedAt  *time.Time `json:"attempted_at,omitempty"`
	WindowsReset int        `json:"windows_reset"`
	ErrorCode    string     `json:"error_code,omitempty"`
}

func (r *OpenAIResetReservation) View() *OpenAIResetReservationView {
	if r == nil {
		return nil
	}
	return &OpenAIResetReservationView{
		ID: r.ID, Status: r.Status, ExpiresAt: r.ExpiresAt, ExecuteAt: r.ExecuteAt,
		UpdatedAt: r.UpdatedAt, AttemptedAt: r.AttemptedAt,
		WindowsReset: r.WindowsReset, ErrorCode: r.ErrorCode,
	}
}

func (r *OpenAIResetReservation) active() bool {
	return r != nil && (r.Status == "pending" || r.Status == "resetting" || r.Status == "unknown" || r.Status == "consumed")
}

type OpenAIResetReservationRepository interface {
	Get(context.Context, int64) (*OpenAIResetReservation, error)
	GetActiveByIdentity(context.Context, string) (*OpenAIResetReservation, error)
	CompareAndSwap(context.Context, *OpenAIResetReservation, int64) (bool, error)
	ListRunnable(context.Context, int64, int) ([]int64, error)
}

type OpenAIResetReservationService struct {
	repo        OpenAIResetReservationRepository
	accounts    AccountRepository
	quota       openAIAutoResetQuota
	locks       LeaderLockCache
	idempotency *IdempotencyCoordinator
	recoverer   openAIAutoResetRecovery
	now         func() time.Time
}

func NewOpenAIResetReservationService(repo OpenAIResetReservationRepository, accounts AccountRepository,
	quota openAIAutoResetQuota, locks LeaderLockCache, idempotency *IdempotencyCoordinator,
	recoverer openAIAutoResetRecovery,
) *OpenAIResetReservationService {
	return &OpenAIResetReservationService{repo: repo, accounts: accounts, quota: quota,
		locks: locks, idempotency: idempotency, recoverer: recoverer, now: time.Now}
}

type openAIResetReservationLease struct {
	accountID int64
}

type openAIResetReservationLeaseKey struct{}

func openAIResetIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	id, _ := account.Credentials["chatgpt_account_id"].(string)
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return shortOpenAIAutoResetHash(strings.TrimSpace(id))
}

// lease serializes all reset entry points, including aliases of one subscription.
// Every caller has a 50s budget; the 120s lease outlives upstream and recovery
// timeouts. A missing/unavailable coordination service must not permit spending.
func (s *OpenAIResetReservationService) lease(ctx context.Context, accountID int64) (context.Context, func(), error) {
	if held, ok := ctx.Value(openAIResetReservationLeaseKey{}).(openAIResetReservationLease); ok && held.accountID == accountID {
		return ctx, func() {}, nil
	}
	if s.locks == nil {
		return nil, nil, infraerrors.ServiceUnavailable("OPENAI_RESET_LOCK_UNAVAILABLE", "reset coordination is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	owner := uuid.NewString()
	var keys []string
	release := func() {
		defer cancel()
		releaseCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		for i := len(keys) - 1; i >= 0; i-- {
			_ = s.locks.ReleaseLeaderLock(releaseCtx, keys[i], owner)
		}
	}
	acquire := func(key string) error {
		ok, err := s.locks.TryAcquireLeaderLock(ctx, key, owner, 2*time.Minute)
		if err != nil {
			return infraerrors.ServiceUnavailable("OPENAI_RESET_LOCK_UNAVAILABLE", "reset coordination is unavailable")
		}
		if !ok {
			return infraerrors.Conflict("OPENAI_RESET_BUSY", "another reset operation is in progress")
		}
		keys = append(keys, key)
		return nil
	}
	if err := acquire(fmt.Sprintf("openai:reset:account:%d", accountID)); err != nil {
		release()
		return nil, nil, err
	}
	account, err := s.accounts.GetByID(ctx, accountID)
	if err == nil && account == nil {
		err = ErrAccountNotFound
	}
	if err == nil {
		if identity := openAIResetIdentity(account); identity != "" {
			err = acquire("openai:reset:identity:" + identity)
		}
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return context.WithValue(ctx, openAIResetReservationLeaseKey{}, openAIResetReservationLease{accountID}), release, nil
}

// guardConsumption is used by both legacy manual and threshold reset paths.
// An active reservation owns that subscription's next reset. The reservation
// executor calls the quota service with its already-held lease.
func (s *OpenAIResetReservationService) guardConsumption(ctx context.Context, accountID int64) (context.Context, func(), error) {
	if held, ok := ctx.Value(openAIResetReservationLeaseKey{}).(openAIResetReservationLease); ok && held.accountID == accountID {
		return ctx, func() {}, nil
	}
	ctx, release, err := s.lease(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	account, err := s.accounts.GetByID(ctx, accountID)
	if err == nil {
		var reservation *OpenAIResetReservation
		reservation, err = s.repo.Get(ctx, accountID)
		if err == nil && !reservation.active() {
			reservation, err = s.repo.GetActiveByIdentity(ctx, openAIResetIdentity(account))
		}
		if err == nil && reservation.active() {
			err = infraerrors.Conflict("OPENAI_RESET_RESERVED", "a reset reservation is active; cancel a pending reservation before resetting manually")
		}
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return ctx, release, nil
}

func (s *OpenAIResetReservationService) Get(ctx context.Context, accountID int64) (*OpenAIResetReservationView, error) {
	if _, err := s.accounts.GetByID(ctx, accountID); err != nil {
		return nil, err
	}
	reservation, err := s.repo.Get(ctx, accountID)
	return reservation.View(), err
}

func (s *OpenAIResetReservationService) save(ctx context.Context, r *OpenAIResetReservation) error {
	expected := r.Version
	r.Version++
	r.UpdatedAt = s.now().UTC()
	ok, err := s.repo.CompareAndSwap(ctx, r, expected)
	if err != nil {
		return err
	}
	if !ok {
		return infraerrors.Conflict("OPENAI_RESET_RESERVATION_CHANGED", "reset reservation changed; refresh and retry")
	}
	return nil
}

func (s *OpenAIResetReservationService) Create(ctx context.Context, accountID int64, leadMinutes int) (*OpenAIResetReservationView, error) {
	if leadMinutes == 0 {
		leadMinutes = 20
	}
	if leadMinutes < 20 || leadMinutes > 1440 || leadMinutes%20 != 0 {
		return nil, infraerrors.BadRequest("OPENAI_RESET_LEAD_INVALID", "lead_minutes must be a multiple of 20 between 20 and 1440")
	}
	ctx, release, err := s.lease(ctx, accountID)
	if err != nil {
		return nil, err
	}
	defer release()
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !isOpenAIAutoResetCreditAccount(account) || !account.IsActive() || openAIResetIdentity(account) == "" {
		return nil, infraerrors.BadRequest("OPENAI_RESET_ACCOUNT_INVALID", "an active OpenAI OAuth parent account with a subscription identity is required")
	}
	previous, err := s.repo.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if previous.active() {
		return nil, infraerrors.Conflict("OPENAI_RESET_RESERVED", "a reservation already exists")
	}
	active, err := s.repo.GetActiveByIdentity(ctx, openAIResetIdentity(account))
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, infraerrors.Conflict("OPENAI_RESET_RESERVED", "this subscription already has a reservation")
	}
	usage, err := s.quota.QueryUsage(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if usage == nil || usage.RateLimitResetCredits == nil || usage.AccountID == "" ||
		shortOpenAIAutoResetHash(strings.TrimSpace(usage.AccountID)) != openAIResetIdentity(account) {
		return nil, infraerrors.Conflict("OPENAI_RESET_DETAILS_UNAVAILABLE", "subscription identity and reset details could not be confirmed")
	}
	candidate, err := selectOpenAIAutoResetCandidate(usage.autoResetCandidates, usage.RateLimitResetCredits.AvailableCount, nil, "")
	if err != nil {
		return nil, err
	}
	expires, _ := time.Parse(time.RFC3339, candidate.ExpiresAt)
	now := s.now().UTC()
	if !expires.After(now.Add(openAIResetReservationCutoff)) {
		return nil, infraerrors.Conflict("OPENAI_RESET_TOO_LATE", "the earliest reset is expired or too close to expiry")
	}
	r := &OpenAIResetReservation{
		ID: uuid.NewString(), AccountID: accountID, IdentityHash: openAIResetIdentity(account),
		CreditHash: shortOpenAIAutoResetHash(candidate.ID), Status: "pending",
		ExpiresAt: expires, ExecuteAt: expires.Add(-time.Duration(leadMinutes) * time.Minute), CreatedAt: now,
	}
	if previous != nil {
		r.Version = previous.Version
	}
	if err := s.save(ctx, r); err != nil {
		return nil, err
	}
	NotifyOpenAIAutoResetCredit(accountID)
	return r.View(), nil
}

func (s *OpenAIResetReservationService) Cancel(ctx context.Context, accountID int64, reservationID string) (*OpenAIResetReservationView, error) {
	ctx, release, err := s.lease(ctx, accountID)
	if err != nil {
		return nil, err
	}
	defer release()
	r, err := s.repo.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if r == nil || r.ID != reservationID {
		return nil, infraerrors.Conflict("OPENAI_RESET_RESERVATION_CHANGED", "refresh the reservation before cancelling")
	}
	if r.Status == "cancelled" {
		return r.View(), nil
	}
	if r.Status != "pending" || r.AttemptedAt != nil {
		return nil, infraerrors.Conflict("OPENAI_RESET_ALREADY_STARTED", "the reset has already started; its result must be confirmed")
	}
	r.Status = "cancelled"
	if err := s.save(ctx, r); err != nil {
		return nil, err
	}
	return r.View(), nil
}

// Process handles a reservation before the legacy threshold evaluator. It also
// allows the reserved card to be redeemed early by existing usage thresholds.
func (s *OpenAIResetReservationService) Process(ctx context.Context, accountID int64) (bool, error) {
	r, err := s.repo.Get(ctx, accountID)
	if err != nil || !r.active() {
		return false, err
	}
	originalCtx := ctx
	ctx, release, err := s.lease(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			switch {
			case r.Status == "consumed":
				r.Status, r.ErrorCode = "completed", "ACCOUNT_REMOVED_AFTER_REDEMPTION"
			case r.AttemptedAt == nil:
				r.Status, r.ErrorCode = "invalidated", "ACCOUNT_REMOVED"
			case s.now().After(r.ExpiresAt.Add(2 * time.Minute)):
				r.Status, r.ErrorCode = "unconfirmed", "ACCOUNT_REMOVED_AFTER_ATTEMPT"
			default:
				r.Status, r.ErrorCode = "unknown", "ACCOUNT_REMOVED_AFTER_ATTEMPT"
			}
			return true, s.save(originalCtx, r)
		}
		return true, err
	}
	defer release()
	r, err = s.repo.Get(ctx, accountID)
	if err != nil || !r.active() {
		return true, err
	}
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return true, err
	}
	finish := func(status, code string) (bool, error) {
		r.Status, r.ErrorCode = status, code
		return true, s.save(ctx, r)
	}
	// An unknown outcome never becomes "success". Once the target has expired
	// and any old lease/request has drained, release the subscription fence.
	if r.Status == "unknown" && s.now().After(r.ExpiresAt.Add(2*time.Minute)) {
		return finish("unconfirmed", "RESET_RESULT_UNCONFIRMED")
	}
	if !isOpenAIAutoResetCreditAccount(account) || openAIResetIdentity(account) != r.IdentityHash {
		if r.Status == "consumed" {
			return finish("completed", "SUBSCRIPTION_CHANGED_AFTER_REDEMPTION")
		}
		if r.AttemptedAt != nil {
			return finish("unknown", "SUBSCRIPTION_CHANGED_AFTER_ATTEMPT")
		}
		return finish("invalidated", "SUBSCRIPTION_CHANGED")
	}
	if r.Status == "consumed" {
		return true, s.recover(ctx, r)
	}
	// A successful receipt can survive a failed reservation write or a crash.
	// Read it BEFORE querying the remaining cards or checking the deadline.
	key := "oars:" + r.IdentityHash + ":" + r.CreditHash
	if r.AttemptedAt != nil {
		record, receiptErr := s.idempotency.repo.GetByScopeAndKeyHash(ctx, "openai_reset_reservation", HashIdempotencyKey(key))
		if receiptErr != nil {
			return true, receiptErr
		}
		if record != nil && record.Status == IdempotencyStatusSucceeded {
			data, decodeErr := s.idempotency.decodeStoredResponse(record.ResponseBody)
			if decodeErr != nil {
				return true, decodeErr
			}
			receipt, decodeErr := decodeReservationReceipt(data)
			if decodeErr != nil {
				return true, decodeErr
			}
			if receipt.Outcome == "no_credit" {
				return finish("unavailable", "RESERVED_CREDIT_UNAVAILABLE")
			}
			if receipt.Outcome != "ok" {
				return finish("unknown", "RESET_RESULT_UNCONFIRMED")
			}
			consumedAt := s.now().UTC()
			r.Status, r.WindowsReset, r.ErrorCode, r.ConsumedAt = "consumed", receipt.WindowsReset, "", &consumedAt
			if err := s.save(ctx, r); err != nil {
				return true, err
			}
			return true, s.recover(ctx, r)
		}
		if record != nil && record.Status == IdempotencyStatusProcessing && s.now().Sub(*r.AttemptedAt) > 2*time.Minute {
			return finish("unknown", "RESET_RESULT_UNCONFIRMED")
		}
	}
	if r.Status == "unknown" {
		return true, nil
	}
	now := s.now().UTC()
	pastCutoff := !now.Before(r.ExpiresAt.Add(-openAIResetReservationCutoff))
	if pastCutoff {
		if r.AttemptedAt != nil && !r.OnlyNoopAttempts {
			return finish("unknown", "RESET_RESULT_UNCONFIRMED")
		}
		if r.AttemptedAt == nil {
			code := r.ErrorCode
			if code == "" {
				code = "RESET_DEADLINE_PASSED"
			}
			return finish("expired", code)
		}
		if !now.Before(r.ExpiresAt) || !account.IsActive() {
			return finish("unknown", "RESET_RESULT_UNCONFIRMED")
		}
	}
	if !account.IsActive() {
		return true, nil
	}
	if r.AttemptedAt == nil && now.Before(r.ExecuteAt) {
		config := ResolveOpenAIAutoResetCreditConfig(account)
		sevenDay, _ := resolveOpenAIQuotaUtilization(account.Extra, "7d", now)
		if !config.Enabled || !openAIResetWeeklyThresholdReached(sevenDay, config.Threshold7d) {
			// Preserve the legacy evaluator's cache/backoff and available-credit
			// state, which the scheduler needs between pause and reset thresholds.
			return false, nil
		}
	}
	if !pastCutoff && r.NextCheckAt != nil && now.Before(*r.NextCheckAt) {
		return true, nil
	}
	if !pastCutoff && r.NextCheckAt == nil && r.AttemptedAt != nil && now.Before(r.UpdatedAt.Add(openAIResetRetryInterval)) {
		return true, nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < openaiQuotaUpstreamTimeout+5*time.Second {
		return true, nil
	}
	firstAttempt := r.AttemptedAt == nil
	nextCheck := now.Add(openAIResetRetryInterval)
	r.NextCheckAt = &nextCheck
	// Persist before querying so query failures and hot notifications cannot
	// amplify retries, including after a process restart.
	if err := s.save(ctx, r); err != nil {
		return true, err
	}
	usage, err := s.quota.QueryUsage(ctx, accountID)
	if err != nil {
		r.ErrorCode = "RESET_QUERY_FAILED"
		return true, s.save(ctx, r)
	}
	if usage == nil || usage.RateLimitResetCredits == nil || usage.AccountID == "" ||
		shortOpenAIAutoResetHash(strings.TrimSpace(usage.AccountID)) != r.IdentityHash ||
		len(usage.autoResetCandidates) < usage.RateLimitResetCredits.AvailableCount {
		r.ErrorCode = "RESET_DETAILS_UNCONFIRMED"
		return true, s.save(ctx, r)
	}
	_ = s.quota.CacheResetCreditsSnapshot(ctx, accountID, usage.RateLimitResetCredits)
	var candidate *openAIAutoResetCreditCandidate
	for i := range usage.autoResetCandidates {
		if shortOpenAIAutoResetHash(usage.autoResetCandidates[i].ID) == r.CreditHash {
			candidate = &usage.autoResetCandidates[i]
			break
		}
	}
	if candidate == nil {
		if r.AttemptedAt != nil {
			return finish("unknown", "RESET_RESULT_UNCONFIRMED")
		}
		return finish("unavailable", "RESERVED_CREDIT_UNAVAILABLE")
	}
	if pastCutoff {
		return finish("expired", "OPENAI_RESET_NOTHING_TO_RESET")
	}
	config := ResolveOpenAIAutoResetCreditConfig(account)
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	thresholdReached := config.Enabled && openAIResetWeeklyThresholdReached(readOpenAIQuotaUsedPercent(updates, "7d")/100, config.Threshold7d)
	if r.AttemptedAt == nil && now.Before(r.ExecuteAt) && !thresholdReached {
		return true, nil
	}
	if !openAIResetUsageEligible(usage, now) {
		r.ErrorCode = "OPENAI_RESET_USAGE_TOO_LOW"
		return true, s.save(ctx, r)
	}
	// The operation is bound to identity+credit, independent of appointment UUID
	// and quota windows. Cancelling/rebooking can never change its redeem ID.
	account, err = s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return true, err
	}
	if !isOpenAIAutoResetCreditAccount(account) || openAIResetIdentity(account) != r.IdentityHash {
		if r.AttemptedAt != nil {
			return finish("unknown", "SUBSCRIPTION_CHANGED_AFTER_ATTEMPT")
		}
		return finish("invalidated", "SUBSCRIPTION_CHANGED")
	}
	if !account.IsActive() {
		return true, nil
	}
	if r.AttemptedAt == nil {
		r.AttemptedAt = &now
	}
	onlyNoops := firstAttempt || r.OnlyNoopAttempts
	r.LastAttemptAt, r.OnlyNoopAttempts = &now, false
	r.Status, r.ErrorCode = "resetting", ""
	if err := s.save(ctx, r); err != nil {
		return true, err
	}
	sent := false
	result, err := s.idempotency.Execute(ctx, IdempotencyExecuteOptions{
		Scope: "openai_reset_reservation", ActorScope: r.IdentityHash, Method: http.MethodPost,
		Route: "/system/openai/reset-credit/reservation", IdempotencyKey: key, RequireKey: true,
		Payload: map[string]string{"identity_hash": r.IdentityHash, "credit_hash": r.CreditHash},
		TTL:     r.ExpiresAt.Sub(now) + 30*24*time.Hour,
	}, func(execCtx context.Context) (any, error) {
		current, loadErr := s.accounts.GetByID(execCtx, accountID)
		if loadErr != nil {
			return nil, loadErr
		}
		if !isOpenAIAutoResetCreditAccount(current) || !current.IsActive() || openAIResetIdentity(current) != r.IdentityHash {
			return nil, infraerrors.Conflict("OPENAI_RESET_ACCOUNT_CHANGED", "account changed before redemption")
		}
		if !s.now().Before(r.ExpiresAt.Add(-openAIResetReservationCutoff)) {
			return nil, infraerrors.Conflict("OPENAI_RESET_TOO_LATE", "reset deadline passed")
		}
		if deadline, ok := execCtx.Deadline(); ok && time.Until(deadline) < openaiQuotaUpstreamTimeout+5*time.Second {
			return nil, infraerrors.Conflict("OPENAI_RESET_RETRY_LATER", "insufficient time to start redemption")
		}
		sent = true
		reset, resetErr := s.quota.ResetCreditTargeted(withOpenAIResetUsage(execCtx, accountID, usage), accountID, candidate.ID, uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String())
		if resetErr != nil {
			if openAIResetNotSent(resetErr) {
				sent = false
			}
			return nil, resetErr
		}
		if reset == nil {
			return nil, infraerrors.Conflict("OPENAI_RESET_RESULT_UNCONFIRMED", "unexpected upstream reset result")
		}
		outcome := openAIResetOutcome(reset.Code)
		switch outcome {
		case "ok", "success":
			outcome = "ok"
		case "no_credit":
		case "nothing_to_reset":
			return nil, openAIResetNoopError()
		default:
			return nil, infraerrors.Conflict("OPENAI_RESET_RESULT_UNCONFIRMED", "unexpected upstream reset result")
		}
		return openAIResetReservationReceipt{Outcome: outcome, WindowsReset: reset.WindowsReset}, nil
	})
	// Persist the outcome even if the worker/client deadline just expired.
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if err != nil {
		// Start the next interval after the call completes, not before the
		// preceding usage query. Otherwise the next check can arrive a few
		// seconds before the shared cooldown expires and lose an opportunity.
		nextCheck = s.now().UTC().Add(openAIResetRetryInterval)
		r.NextCheckAt = &nextCheck
		if firstAttempt && !sent && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyInProgress) {
			r.Status, r.AttemptedAt = "pending", nil
		}
		r.ErrorCode = infraerrors.Reason(err)
		if r.ErrorCode == "OPENAI_RESET_NOTHING_TO_RESET" {
			r.OnlyNoopAttempts = onlyNoops
		}
		if openAIResetNotSent(err) {
			r.OnlyNoopAttempts = onlyNoops
		}
		if r.ErrorCode == "" {
			r.ErrorCode = "RESET_REQUEST_FAILED"
		}
		if infraerrors.Reason(err) == infraerrors.Reason(ErrIdempotencyInProgress) && now.Sub(*r.AttemptedAt) > 2*time.Minute {
			r.Status = "unknown"
		}
		return true, s.save(postCtx, r)
	}
	consumed, decodeErr := decodeReservationReceipt(result.Data)
	if decodeErr != nil || (consumed.Outcome != "ok" && consumed.Outcome != "no_credit") {
		r.Status, r.ErrorCode = "unknown", "RESET_RESULT_UNCONFIRMED"
		return true, s.save(postCtx, r)
	}
	if consumed.Outcome == "no_credit" {
		r.Status, r.ErrorCode = "unavailable", "RESERVED_CREDIT_UNAVAILABLE"
		return true, s.save(postCtx, r)
	}
	consumedAt := s.now().UTC()
	r.Status, r.WindowsReset, r.ErrorCode, r.ConsumedAt = "consumed", consumed.WindowsReset, "", &consumedAt
	if err := s.save(postCtx, r); err != nil {
		return true, err
	}
	return true, s.recover(postCtx, r)
}

func (s *OpenAIResetReservationService) recover(ctx context.Context, r *OpenAIResetReservation) error {
	post := RunOpenAIQuotaResetPostProcess(ctx, r.AccountID, s.quota, s.recoverer, s.accounts.GetByID)
	r.ErrorCode = post.WarningCode
	recoveryDeadline := r.ExpiresAt
	if r.ConsumedAt != nil && r.ConsumedAt.Add(10*time.Minute).Before(recoveryDeadline) {
		recoveryDeadline = r.ConsumedAt.Add(10 * time.Minute)
	}
	if post.AccountStateRecovered || !s.now().Before(recoveryDeadline) {
		r.Status = "completed"
	}
	// A timed-out cache refresh must not discard the already confirmed outcome.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return s.save(persistCtx, r)
}
