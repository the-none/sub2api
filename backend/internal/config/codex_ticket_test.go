package config

import (
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodexTicketLoadPreservesExplicitZeroAndEmptyInstructions(t *testing.T) {
	resetViperWithJWTSecret(t)
	viper.Set("gateway.openai_codex_ticket.refresh_before_seconds", 0)
	viper.Set("gateway.openai_codex_ticket.instructions", "")
	cfg, err := Load()
	require.NoError(t, err)
	require.Zero(t, cfg.Gateway.OpenAICodexTicket.RefreshBeforeSeconds)
	require.Empty(t, cfg.Gateway.OpenAICodexTicket.Instructions)
}

func TestCodexTicketLoadEmptyInstructionsEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_INSTRUCTIONS", "")
	cfg, err := Load()
	require.NoError(t, err)
	require.Empty(t, cfg.Gateway.OpenAICodexTicket.Instructions)
}

func TestCodexTicketLoadRejectsInvalidStaticPolicy(t *testing.T) {
	for _, tt := range []struct {
		key   string
		value any
	}{
		{"ttl_seconds", 60}, {"max_concurrency", 33}, {"target_length", 0}, {"models", []string{"same", "same"}}, {"harvest_proxy_url", "ftp://invalid:21"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			viper.Set("gateway.openai_codex_ticket."+tt.key, tt.value)
			_, err := Load()
			require.ErrorContains(t, err, "openai_codex_ticket")
		})
	}
}
