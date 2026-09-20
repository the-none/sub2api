package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketDisabledViewSerializesTicketListAsArray(t *testing.T) {
	for _, globalEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "global-off", true: "account-off"}[globalEnabled], func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: globalEnabled}, nil)
			account := ticketTestAccount(41)
			account.Status = StatusActive
			account.Extra = map[string]any{CodexTicketEnabledExtraKey: false}
			view := svc.CodexTicketAccountView(context.Background(), account)
			require.True(t, view.Eligible, "a disabled ticket policy must remain editable")
			require.False(t, view.Enabled)
			body, err := json.Marshal(view)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &fields))
			require.JSONEq(t, `[]`, string(fields["tickets"]))
		})
	}
}

func TestCodexTicketPolicyCanBeDisabledAndReenabled(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	repo := &ticketPolicyReviewRepo{account: *account}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = repo
	for _, enabled := range []bool{false, true, false, true} {
		current, err := repo.GetByID(context.Background(), 41)
		require.NoError(t, err)
		before := svc.CodexTicketAccountView(context.Background(), current)
		require.NoError(t, svc.SaveCodexTicketPolicyIfCurrent(context.Background(), 41, CodexTicketPolicy{Enabled: &enabled}, before.Revision))
		updated, err := repo.GetByID(context.Background(), 41)
		require.NoError(t, err)
		after := svc.CodexTicketAccountView(context.Background(), updated)
		require.Equal(t, enabled, after.Enabled)
		require.True(t, after.Eligible)
		require.NotNil(t, after.Tickets)
		require.NotEqual(t, before.Revision, after.Revision)
	}
}
