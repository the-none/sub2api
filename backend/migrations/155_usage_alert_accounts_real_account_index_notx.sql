CREATE INDEX CONCURRENTLY IF NOT EXISTS accounts_real_account_id_idx
    ON accounts (real_account_id)
    WHERE real_account_id IS NOT NULL;
