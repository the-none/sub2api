-- Runtime state deliberately lives outside accounts.extra: stale account edits,
-- imports and account duplication must never resurrect a cancelled reservation.
CREATE TABLE openai_reset_reservations (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    id UUID NOT NULL UNIQUE,
    identity_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'resetting', 'unknown', 'consumed', 'completed',
        'cancelled', 'expired', 'unavailable', 'invalidated', 'unconfirmed'
    )),
    version BIGINT NOT NULL CHECK (version > 0),
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Multiple local rows may represent the same upstream subscription.
CREATE UNIQUE INDEX openai_reset_reservations_active_identity
    ON openai_reset_reservations (identity_hash)
    WHERE status IN ('pending', 'resetting', 'unknown', 'consumed');
CREATE INDEX openai_reset_reservations_pending
    ON openai_reset_reservations (account_id)
    WHERE status IN ('pending', 'resetting', 'unknown', 'consumed');
