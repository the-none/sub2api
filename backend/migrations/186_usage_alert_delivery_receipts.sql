-- Persist one delivery receipt per alert event and webhook. This prevents
-- healthy endpoints from being resent while another endpoint is retried and
-- provides a lease-based claim across multiple application instances.
CREATE TABLE IF NOT EXISTS usage_alert_deliveries (
    event_id TEXT NOT NULL,
    real_account_id BIGINT NOT NULL REFERENCES real_accounts(id) ON DELETE CASCADE,
    rule_id BIGINT NOT NULL REFERENCES usage_alert_rules(id) ON DELETE CASCADE,
    webhook_id BIGINT NOT NULL REFERENCES usage_alert_webhooks(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    claim_token VARCHAR(64) NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, webhook_id),
    CONSTRAINT usage_alert_deliveries_status_check
        CHECK (status IN ('pending', 'delivered'))
);

CREATE INDEX IF NOT EXISTS idx_usage_alert_deliveries_cleanup
    ON usage_alert_deliveries (delivered_at)
    WHERE status = 'delivered';
