package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbrealaccount "github.com/Wei-Shaw/sub2api/ent/realaccount"
	dbrealaccountusagesnapshot "github.com/Wei-Shaw/sub2api/ent/realaccountusagesnapshot"
	dbusagealertbinding "github.com/Wei-Shaw/sub2api/ent/usagealertbinding"
	dbusagealertrule "github.com/Wei-Shaw/sub2api/ent/usagealertrule"
	dbusagealertstate "github.com/Wei-Shaw/sub2api/ent/usagealertstate"
	dbusagealertwebhook "github.com/Wei-Shaw/sub2api/ent/usagealertwebhook"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageAlertRepository struct {
	client *dbent.Client
	sql    sqlExecutor
}

func NewUsageAlertRepository(client *dbent.Client, sqlDB *sql.DB) service.UsageAlertRepository {
	return &usageAlertRepository{client: client, sql: sqlDB}
}

func (r *usageAlertRepository) ListRealAccounts(ctx context.Context) ([]*service.RealAccount, error) {
	rows, err := r.client.RealAccount.Query().
		WithAccounts().
		Order(dbent.Asc(dbrealaccount.FieldPlatform), dbent.Asc(dbrealaccount.FieldName), dbent.Asc(dbrealaccount.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.RealAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, realAccountEntityToService(row, true))
	}
	return out, nil
}

func (r *usageAlertRepository) GetRealAccount(ctx context.Context, id int64) (*service.RealAccount, error) {
	row, err := r.client.RealAccount.Query().
		Where(dbrealaccount.IDEQ(id)).
		WithAccounts().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAccountNotFound
		}
		return nil, err
	}
	return realAccountEntityToService(row, true), nil
}

func (r *usageAlertRepository) CreateRealAccount(ctx context.Context, account *service.RealAccount) (*service.RealAccount, error) {
	builder := r.client.RealAccount.Create().
		SetName(account.Name).
		SetPlatform(account.Platform).
		SetNillableIdentifier(account.Identifier).
		SetNillableNotes(account.Notes)
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return realAccountEntityToService(row, false), nil
}

func (r *usageAlertRepository) UpdateRealAccount(ctx context.Context, account *service.RealAccount) (*service.RealAccount, error) {
	builder := r.client.RealAccount.UpdateOneID(account.ID).
		SetName(account.Name).
		SetPlatform(account.Platform).
		SetNillableIdentifier(account.Identifier).
		SetNillableNotes(account.Notes)
	if account.Identifier == nil {
		builder.ClearIdentifier()
	}
	if account.Notes == nil {
		builder.ClearNotes()
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return realAccountEntityToService(row, false), nil
}

func (r *usageAlertRepository) DeleteRealAccount(ctx context.Context, id int64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Account.Update().
		Where(dbaccount.RealAccountIDEQ(id)).
		ClearRealAccountID().
		Save(ctx); err != nil {
		return err
	}
	if _, err := tx.UsageAlertBinding.Delete().
		Where(dbusagealertbinding.RealAccountIDEQ(id)).
		Exec(ctx); err != nil {
		return err
	}
	if _, err := tx.RealAccountUsageSnapshot.Delete().
		Where(dbrealaccountusagesnapshot.RealAccountIDEQ(id)).
		Exec(ctx); err != nil {
		return err
	}
	if _, err := tx.UsageAlertState.Delete().
		Where(dbusagealertstate.RealAccountIDEQ(id)).
		Exec(ctx); err != nil {
		return err
	}
	if err := tx.RealAccount.DeleteOneID(id).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	rollback = false
	return nil
}

func (r *usageAlertRepository) AttachAccount(ctx context.Context, realAccountID, accountID int64) error {
	realAccount, err := r.client.RealAccount.Get(ctx, realAccountID)
	if err != nil {
		return err
	}
	account, err := r.client.Account.Get(ctx, accountID)
	if err != nil {
		return err
	}
	if account.Platform != realAccount.Platform {
		return fmt.Errorf("account platform %q does not match real account platform %q", account.Platform, realAccount.Platform)
	}
	return r.client.Account.UpdateOneID(accountID).
		SetRealAccountID(realAccountID).
		Exec(ctx)
}

func (r *usageAlertRepository) DetachAccount(ctx context.Context, accountID int64) error {
	return r.client.Account.UpdateOneID(accountID).ClearRealAccountID().Exec(ctx)
}

func (r *usageAlertRepository) EnsureRealAccountForAccount(ctx context.Context, account *service.Account) (*service.RealAccount, error) {
	if account == nil || account.ID <= 0 {
		return nil, fmt.Errorf("account is required")
	}
	if account.Platform != service.PlatformOpenAI && account.Platform != service.PlatformAnthropic {
		return nil, fmt.Errorf("usage alerts only support openai and anthropic accounts")
	}
	if account.RealAccountID != nil && *account.RealAccountID > 0 {
		return r.GetRealAccount(ctx, *account.RealAccountID)
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	current, err := tx.Account.Get(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if current.RealAccountID != nil && *current.RealAccountID > 0 {
		row, err := tx.RealAccount.Get(ctx, *current.RealAccountID)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		rollback = false
		return realAccountEntityToService(row, false), nil
	}
	row, err := tx.RealAccount.Create().
		SetName(account.Name).
		SetPlatform(account.Platform).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := tx.Account.Update().
		Where(dbaccount.IDEQ(account.ID), dbaccount.RealAccountIDIsNil()).
		SetRealAccountID(row.ID).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated == 0 {
		if err := tx.RealAccount.DeleteOneID(row.ID).Exec(ctx); err != nil {
			return nil, err
		}
		current, err := tx.Account.Get(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		if current.RealAccountID == nil || *current.RealAccountID <= 0 {
			return nil, fmt.Errorf("real account was not bound")
		}
		row, err = tx.RealAccount.Get(ctx, *current.RealAccountID)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rollback = false
	return realAccountEntityToService(row, false), nil
}

func (r *usageAlertRepository) ListRules(ctx context.Context) ([]*service.UsageAlertRule, error) {
	rows, err := r.client.UsageAlertRule.Query().
		WithRealAccount().
		Order(dbent.Asc(dbusagealertrule.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.UsageAlertRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageAlertRuleEntityToService(row))
	}
	return out, nil
}

func (r *usageAlertRepository) ListEnabledRules(ctx context.Context, realAccountID int64, usageType string) ([]*service.UsageAlertRule, error) {
	rows, err := r.client.UsageAlertRule.Query().
		Where(
			dbusagealertrule.EnabledEQ(true),
			dbusagealertrule.RealAccountIDEQ(realAccountID),
			dbusagealertrule.UsageTypeEQ(usageType),
		).
		Order(dbent.Asc(dbusagealertrule.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.UsageAlertRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageAlertRuleEntityToService(row))
	}
	return out, nil
}

func (r *usageAlertRepository) GetRule(ctx context.Context, id int64) (*service.UsageAlertRule, error) {
	row, err := r.client.UsageAlertRule.Query().
		Where(dbusagealertrule.IDEQ(id)).
		WithRealAccount().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertRuleEntityToService(row), nil
}

func (r *usageAlertRepository) CreateRule(ctx context.Context, rule *service.UsageAlertRule) (*service.UsageAlertRule, error) {
	builder := r.client.UsageAlertRule.Create().
		SetName(rule.Name).
		SetPlatform(rule.Platform).
		SetUsageType(rule.UsageType).
		SetWindow(rule.Window).
		SetMetric(rule.Metric).
		SetOperator(rule.Operator).
		SetThreshold(rule.Threshold).
		SetCooldownMinutes(rule.CooldownMinutes).
		SetEnabled(rule.Enabled)
	if rule.RealAccountID != nil {
		builder.SetRealAccountID(*rule.RealAccountID)
	}
	if rule.MinResetAfterHours != nil {
		builder.SetMinResetAfterHours(*rule.MinResetAfterHours)
	}
	if rule.StepPercent != nil {
		builder.SetStepPercent(*rule.StepPercent)
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertRuleEntityToService(row), nil
}

func (r *usageAlertRepository) UpdateRule(ctx context.Context, rule *service.UsageAlertRule) (*service.UsageAlertRule, error) {
	builder := r.client.UsageAlertRule.UpdateOneID(rule.ID).
		SetName(rule.Name).
		SetPlatform(rule.Platform).
		SetUsageType(rule.UsageType).
		SetWindow(rule.Window).
		SetMetric(rule.Metric).
		SetOperator(rule.Operator).
		SetThreshold(rule.Threshold).
		SetCooldownMinutes(rule.CooldownMinutes).
		SetEnabled(rule.Enabled)
	if rule.RealAccountID != nil {
		builder.SetRealAccountID(*rule.RealAccountID)
	} else {
		builder.ClearRealAccountID()
	}
	if rule.MinResetAfterHours != nil {
		builder.SetMinResetAfterHours(*rule.MinResetAfterHours)
	} else {
		builder.ClearMinResetAfterHours()
	}
	if rule.StepPercent != nil {
		builder.SetStepPercent(*rule.StepPercent)
	} else {
		builder.ClearStepPercent()
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertRuleEntityToService(row), nil
}

func (r *usageAlertRepository) DeleteRule(ctx context.Context, id int64) error {
	return r.client.UsageAlertRule.DeleteOneID(id).Exec(ctx)
}

func (r *usageAlertRepository) ListWebhooks(ctx context.Context) ([]*service.UsageAlertWebhook, error) {
	rows, err := r.client.UsageAlertWebhook.Query().
		Order(dbent.Asc(dbusagealertwebhook.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.UsageAlertWebhook, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageAlertWebhookEntityToService(row))
	}
	return out, nil
}

func (r *usageAlertRepository) GetWebhook(ctx context.Context, id int64) (*service.UsageAlertWebhook, error) {
	row, err := r.client.UsageAlertWebhook.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return usageAlertWebhookEntityToService(row), nil
}

func (r *usageAlertRepository) CreateWebhook(ctx context.Context, webhook *service.UsageAlertWebhook) (*service.UsageAlertWebhook, error) {
	builder := r.client.UsageAlertWebhook.Create().
		SetName(webhook.Name).
		SetType(webhook.Type).
		SetConfig(webhook.Config).
		SetEnabled(webhook.Enabled).
		SetRetryCount(webhook.RetryCount)
	if webhook.URL != "" {
		builder.SetURL(webhook.URL)
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertWebhookEntityToService(row), nil
}

func (r *usageAlertRepository) UpdateWebhook(ctx context.Context, webhook *service.UsageAlertWebhook) (*service.UsageAlertWebhook, error) {
	builder := r.client.UsageAlertWebhook.UpdateOneID(webhook.ID).
		SetName(webhook.Name).
		SetType(webhook.Type).
		SetConfig(webhook.Config).
		SetEnabled(webhook.Enabled).
		SetRetryCount(webhook.RetryCount)
	if webhook.URL != "" {
		builder.SetURL(webhook.URL)
	} else {
		builder.ClearURL()
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertWebhookEntityToService(row), nil
}

func (r *usageAlertRepository) DeleteWebhook(ctx context.Context, id int64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.UsageAlertBinding.Delete().
		Where(dbusagealertbinding.WebhookIDEQ(id)).
		Exec(ctx); err != nil {
		return err
	}
	if err := tx.UsageAlertWebhook.DeleteOneID(id).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	rollback = false
	return nil
}

func (r *usageAlertRepository) ListBindings(ctx context.Context) ([]*service.UsageAlertBinding, error) {
	rows, err := r.client.UsageAlertBinding.Query().
		WithRealAccount().
		WithWebhook().
		Order(dbent.Asc(dbusagealertbinding.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.UsageAlertBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageAlertBindingEntityToService(row, true))
	}
	return out, nil
}

func (r *usageAlertRepository) ListEnabledWebhooksForRealAccount(ctx context.Context, realAccountID int64) ([]*service.UsageAlertWebhook, error) {
	rows, err := r.client.UsageAlertBinding.Query().
		Where(
			dbusagealertbinding.RealAccountIDEQ(realAccountID),
			dbusagealertbinding.EnabledEQ(true),
		).
		WithWebhook(func(q *dbent.UsageAlertWebhookQuery) {
			q.Where(dbusagealertwebhook.EnabledEQ(true))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.UsageAlertWebhook, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.Edges.Webhook == nil {
			continue
		}
		if _, ok := seen[row.Edges.Webhook.ID]; ok {
			continue
		}
		seen[row.Edges.Webhook.ID] = struct{}{}
		out = append(out, usageAlertWebhookEntityToService(row.Edges.Webhook))
	}
	return out, nil
}

func (r *usageAlertRepository) CreateBinding(ctx context.Context, binding *service.UsageAlertBinding) (*service.UsageAlertBinding, error) {
	row, err := r.client.UsageAlertBinding.Create().
		SetRealAccountID(binding.RealAccountID).
		SetWebhookID(binding.WebhookID).
		SetEnabled(binding.Enabled).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertBindingEntityToService(row, false), nil
}

func (r *usageAlertRepository) UpdateBinding(ctx context.Context, binding *service.UsageAlertBinding) (*service.UsageAlertBinding, error) {
	row, err := r.client.UsageAlertBinding.UpdateOneID(binding.ID).
		SetRealAccountID(binding.RealAccountID).
		SetWebhookID(binding.WebhookID).
		SetEnabled(binding.Enabled).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return usageAlertBindingEntityToService(row, false), nil
}

func (r *usageAlertRepository) DeleteBinding(ctx context.Context, id int64) error {
	return r.client.UsageAlertBinding.DeleteOneID(id).Exec(ctx)
}

func (r *usageAlertRepository) GetSnapshot(ctx context.Context, realAccountID int64, usageType string) (*service.UsageAlertSnapshot, error) {
	row, err := r.client.RealAccountUsageSnapshot.Query().
		Where(
			dbrealaccountusagesnapshot.RealAccountIDEQ(realAccountID),
			dbrealaccountusagesnapshot.UsageTypeEQ(usageType),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	snapshot, err := usageAlertSnapshotFromMap(row.SnapshotJSON)
	if err != nil || snapshot == nil {
		return snapshot, err
	}
	if snapshot.RealAccountID <= 0 {
		snapshot.RealAccountID = row.RealAccountID
	}
	if snapshot.UsageType == "" {
		snapshot.UsageType = row.UsageType
	}
	return snapshot, nil
}

func (r *usageAlertRepository) UpsertSnapshot(ctx context.Context, snapshot *service.UsageAlertSnapshot) error {
	if r.sql == nil {
		return fmt.Errorf("usage alert repository SQL executor not configured")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = r.sql.ExecContext(ctx, `
		INSERT INTO real_account_usage_snapshots (
				real_account_id, quota_dimension, platform, source, snapshot_json, sampled_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6, NOW(), NOW())
			ON CONFLICT (real_account_id, quota_dimension) DO UPDATE SET
			platform = EXCLUDED.platform,
			source = EXCLUDED.source,
			snapshot_json = EXCLUDED.snapshot_json,
			sampled_at = EXCLUDED.sampled_at,
			updated_at = NOW()
		`, snapshot.RealAccountID, snapshot.UsageType, snapshot.Platform, snapshot.Source, string(raw), snapshot.SampledAt)
	return err
}

func (r *usageAlertRepository) GetState(ctx context.Context, realAccountID, ruleID int64, usageType, window string) (*service.UsageAlertState, error) {
	row, err := r.client.UsageAlertState.Query().
		Where(
			dbusagealertstate.RealAccountIDEQ(realAccountID),
			dbusagealertstate.RuleIDEQ(ruleID),
			dbusagealertstate.UsageTypeEQ(usageType),
			dbusagealertstate.WindowEQ(window),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return usageAlertStateEntityToService(row), nil
}

func (r *usageAlertRepository) UpsertState(ctx context.Context, state *service.UsageAlertState) error {
	if r.sql == nil {
		return fmt.Errorf("usage alert repository SQL executor not configured")
	}
	var triggeredAt any
	if state.LastTriggeredAt != nil {
		triggeredAt = *state.LastTriggeredAt
	}
	var lastValue any
	if state.LastValue != nil {
		lastValue = *state.LastValue
	}
	var resetAt any
	if state.LastResetAt != nil {
		resetAt = *state.LastResetAt
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_alert_states (
				real_account_id, rule_id, quota_dimension, "window", last_status, last_triggered_at, last_value, last_reset_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
			ON CONFLICT (real_account_id, rule_id, quota_dimension, "window") DO UPDATE SET
			last_status = EXCLUDED.last_status,
			last_triggered_at = COALESCE(EXCLUDED.last_triggered_at, usage_alert_states.last_triggered_at),
			last_value = EXCLUDED.last_value,
			last_reset_at = EXCLUDED.last_reset_at,
			updated_at = NOW()
		`, state.RealAccountID, state.RuleID, state.UsageType, state.Window, state.LastStatus, triggeredAt, lastValue, resetAt)
	return err
}

func realAccountEntityToService(row *dbent.RealAccount, includeAccounts bool) *service.RealAccount {
	if row == nil {
		return nil
	}
	out := &service.RealAccount{
		ID:         row.ID,
		Name:       row.Name,
		Platform:   row.Platform,
		Identifier: row.Identifier,
		Notes:      row.Notes,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
	if includeAccounts && row.Edges.Accounts != nil {
		out.Accounts = make([]*service.Account, 0, len(row.Edges.Accounts))
		for _, account := range row.Edges.Accounts {
			out.Accounts = append(out.Accounts, accountEntityToService(account))
		}
	}
	return out
}

func usageAlertRuleEntityToService(row *dbent.UsageAlertRule) *service.UsageAlertRule {
	if row == nil {
		return nil
	}
	out := &service.UsageAlertRule{
		ID:                 row.ID,
		Name:               row.Name,
		Platform:           row.Platform,
		RealAccountID:      row.RealAccountID,
		UsageType:          row.UsageType,
		Window:             row.Window,
		Metric:             row.Metric,
		Operator:           row.Operator,
		Threshold:          row.Threshold,
		MinResetAfterHours: row.MinResetAfterHours,
		StepPercent:        row.StepPercent,
		CooldownMinutes:    row.CooldownMinutes,
		Enabled:            row.Enabled,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if row.Edges.RealAccount != nil {
		out.RealAccount = realAccountEntityToService(row.Edges.RealAccount, false)
	}
	return out
}

func usageAlertWebhookEntityToService(row *dbent.UsageAlertWebhook) *service.UsageAlertWebhook {
	if row == nil {
		return nil
	}
	url := ""
	if row.URL != nil {
		url = *row.URL
	}
	return &service.UsageAlertWebhook{
		ID:         row.ID,
		Name:       row.Name,
		Type:       row.Type,
		URL:        url,
		Config:     row.Config,
		Enabled:    row.Enabled,
		RetryCount: row.RetryCount,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func usageAlertBindingEntityToService(row *dbent.UsageAlertBinding, includeEdges bool) *service.UsageAlertBinding {
	if row == nil {
		return nil
	}
	out := &service.UsageAlertBinding{
		ID:            row.ID,
		RealAccountID: row.RealAccountID,
		WebhookID:     row.WebhookID,
		Enabled:       row.Enabled,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if includeEdges {
		out.RealAccount = realAccountEntityToService(row.Edges.RealAccount, false)
		out.Webhook = usageAlertWebhookEntityToService(row.Edges.Webhook)
	}
	return out
}

func usageAlertStateEntityToService(row *dbent.UsageAlertState) *service.UsageAlertState {
	if row == nil {
		return nil
	}
	return &service.UsageAlertState{
		ID:              row.ID,
		RealAccountID:   row.RealAccountID,
		RuleID:          row.RuleID,
		UsageType:       row.UsageType,
		Window:          row.Window,
		LastStatus:      row.LastStatus,
		LastTriggeredAt: row.LastTriggeredAt,
		LastValue:       row.LastValue,
		LastResetAt:     row.LastResetAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func usageAlertSnapshotFromMap(raw map[string]any) (*service.UsageAlertSnapshot, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var snapshot service.UsageAlertSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
