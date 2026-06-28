-- Real account usage alerting MVP.
-- A real account represents the actual upstream subscription identity. Multiple
-- schedulable Sub2API accounts may point at the same real account so usage
-- thresholds and webhook notifications de-duplicate at the real source level.

CREATE TABLE IF NOT EXISTS real_accounts (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,
    platform    VARCHAR(50)  NOT NULL CHECK (platform IN ('openai', 'anthropic')),
    identifier  VARCHAR(255),
    notes       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS real_accounts_platform_idx
    ON real_accounts (platform);
CREATE INDEX IF NOT EXISTS real_accounts_name_idx
    ON real_accounts (name);

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS real_account_id BIGINT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_name = 'accounts_real_account_id_fkey'
          AND table_name = 'accounts'
          AND table_schema = current_schema()
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_real_account_id_fkey
            FOREIGN KEY (real_account_id)
            REFERENCES real_accounts (id)
            ON DELETE SET NULL
            NOT VALID;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS real_account_usage_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    real_account_id BIGINT NOT NULL REFERENCES real_accounts(id) ON DELETE CASCADE,
    platform        VARCHAR(50) NOT NULL CHECK (platform IN ('openai', 'anthropic')),
    source          VARCHAR(64) NOT NULL,
    snapshot_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    sampled_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS real_account_usage_snapshots_real_account_id_uq
    ON real_account_usage_snapshots (real_account_id);
CREATE INDEX IF NOT EXISTS real_account_usage_snapshots_platform_idx
    ON real_account_usage_snapshots (platform);
CREATE INDEX IF NOT EXISTS real_account_usage_snapshots_sampled_at_idx
    ON real_account_usage_snapshots (sampled_at);

CREATE TABLE IF NOT EXISTS usage_alert_rules (
    id                      BIGSERIAL PRIMARY KEY,
    name                    VARCHAR(100) NOT NULL,
    platform                VARCHAR(50) NOT NULL DEFAULT 'all',
    "window"                VARCHAR(32) NOT NULL,
    metric                  VARCHAR(32) NOT NULL,
    operator                VARCHAR(4) NOT NULL,
    threshold               DECIMAL(10,4) NOT NULL,
    min_reset_after_hours   DECIMAL(10,4),
    cooldown_minutes        INTEGER NOT NULL DEFAULT 240 CHECK (cooldown_minutes >= 0),
    enabled                 BOOLEAN NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMPTZ,
    CONSTRAINT usage_alert_rules_platform_check
        CHECK (platform IN ('all', 'openai', 'anthropic')),
    CONSTRAINT usage_alert_rules_window_check
        CHECK ("window" IN ('5h', '7d', '7d_sonnet')),
    CONSTRAINT usage_alert_rules_metric_check
        CHECK (metric IN ('used_percent', 'remaining_percent')),
    CONSTRAINT usage_alert_rules_operator_check
        CHECK (operator IN ('>=', '<='))
);

CREATE INDEX IF NOT EXISTS usage_alert_rules_enabled_platform_idx
    ON usage_alert_rules (enabled, platform)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS usage_alert_rules_window_idx
    ON usage_alert_rules ("window")
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS usage_alert_webhooks (
    id            BIGSERIAL PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    url           TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    retry_count   INTEGER NOT NULL DEFAULT 2 CHECK (retry_count >= 0 AND retry_count <= 10),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS usage_alert_webhooks_enabled_idx
    ON usage_alert_webhooks (enabled)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS usage_alert_webhooks_name_idx
    ON usage_alert_webhooks (name)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS usage_alert_bindings (
    id              BIGSERIAL PRIMARY KEY,
    real_account_id BIGINT NOT NULL REFERENCES real_accounts(id) ON DELETE CASCADE,
    webhook_id      BIGINT NOT NULL REFERENCES usage_alert_webhooks(id) ON DELETE CASCADE,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS usage_alert_bindings_real_account_webhook_uq
    ON usage_alert_bindings (real_account_id, webhook_id);
CREATE INDEX IF NOT EXISTS usage_alert_bindings_webhook_id_idx
    ON usage_alert_bindings (webhook_id);

CREATE TABLE IF NOT EXISTS usage_alert_states (
    id                  BIGSERIAL PRIMARY KEY,
    real_account_id     BIGINT NOT NULL REFERENCES real_accounts(id) ON DELETE CASCADE,
    rule_id             BIGINT NOT NULL REFERENCES usage_alert_rules(id) ON DELETE CASCADE,
    "window"            VARCHAR(32) NOT NULL,
    last_status         VARCHAR(20) NOT NULL DEFAULT 'normal',
    last_triggered_at   TIMESTAMPTZ,
    last_value          DECIMAL(10,4),
    last_reset_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT usage_alert_states_window_check
        CHECK ("window" IN ('5h', '7d', '7d_sonnet')),
    CONSTRAINT usage_alert_states_status_check
        CHECK (last_status IN ('normal', 'triggered'))
);

CREATE UNIQUE INDEX IF NOT EXISTS usage_alert_states_real_rule_window_uq
    ON usage_alert_states (real_account_id, rule_id, "window");
CREATE INDEX IF NOT EXISTS usage_alert_states_rule_id_idx
    ON usage_alert_states (rule_id);
