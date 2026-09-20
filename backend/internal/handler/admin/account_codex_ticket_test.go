package admin

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountResponseCodexTicketsUsesConfiguredPolicy(t *testing.T) {
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	h := &AccountHandler{cfg: &config.Config{}}
	require.Empty(t, h.accountResponseFromService(account).CodexTurnTickets)
	require.Empty(t, h.accountListResponseFromService(account).CodexTurnTickets)
	h.cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"configured-model"}, FailClosed: false}
	status := h.accountListResponseFromService(account).CodexTurnTickets
	require.Len(t, status, 1)
	require.Equal(t, "configured-model", status[0].Model)
	require.False(t, status[0].Blocked)
	h.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	require.True(t, h.accountResponseFromService(account).CodexTurnTickets[0].Blocked)
}

func TestAccountResponseCodexTicketsReadsLiveSettingsAfterRestart(t *testing.T) {
	cfg := &config.Config{}
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketEnabled: "true"}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg}
	h.SetCodexTicketSettings(settings)
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeSetupToken}
	require.Len(t, h.accountListResponseFromService(account).CodexTurnTickets, 2)
	require.False(t, cfg.Gateway.OpenAICodexTicket.Enabled)
	repo.values[service.SettingKeyOpenAICodexTicketEnabled] = "false"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	require.Empty(t, h.accountResponseFromService(account).CodexTurnTickets)
}

func TestCodexTicketSettingsAPIHotUpdateMasksSecretsAndRejectsInvalidTiming(t *testing.T) {
	cfg := &config.Config{}
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg, codexTicketSettings: settings}
	router := gin.New()
	router.GET("/ticket", h.GetCodexTicketSettings)
	router.PUT("/ticket", h.UpdateCodexTicketSettings)
	initial := httptest.NewRecorder()
	router.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, "/ticket", nil))
	require.Equal(t, http.StatusOK, initial.Code)
	var envelope struct {
		Data config.OpenAICodexTicketConfig `json:"data"`
	}
	require.NoError(t, json.Unmarshal(initial.Body.Bytes(), &envelope))
	envelope.Data.Enabled = true
	envelope.Data.HarvestProxyURL = "http://user:never-expose@proxy.example:8080"
	envelope.Data.Instructions = ""
	envelope.Data.UserPrompt = "中文提示词"
	envelope.Data.HarvestProbeIntervalSeconds = 42
	call := func(value config.OpenAICodexTicketConfig) *httptest.ResponseRecorder {
		b, err := json.Marshal(value)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPut, "/ticket", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	updated := call(envelope.Data)
	require.Equal(t, http.StatusOK, updated.Code)
	require.NotContains(t, updated.Body.String(), "never-expose")
	require.Contains(t, updated.Body.String(), "中文提示词")
	require.NoError(t, json.Unmarshal(updated.Body.Bytes(), &envelope))
	require.True(t, service.IsMaskedProxyURL(envelope.Data.HarvestProxyURL))
	require.Equal(t, 42, envelope.Data.HarvestProbeIntervalSeconds)
	require.Empty(t, envelope.Data.Instructions)
	maskedSave := call(envelope.Data)
	require.Equal(t, http.StatusOK, maskedSave.Code)
	require.Equal(t, "http://user:never-expose@proxy.example:8080", repo.values[service.SettingKeyOpenAICodexTicketHarvestProxyURL])
	envelope.Data.RefreshBeforeSeconds = envelope.Data.TTLSeconds
	require.Equal(t, http.StatusBadRequest, call(envelope.Data).Code)
}
