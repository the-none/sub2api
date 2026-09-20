package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketProxyExportImportRemapsDedicatedProxy(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.proxies = []service.Proxy{{ID: 11, Name: "harvest-only", Protocol: "http", Host: "proxy.example", Port: 8080, Status: service.StatusActive}}
	svc.accounts = []service.Account{{ID: 21, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: map[string]any{service.CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 11}}, Concurrency: 1}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var exported struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.Len(t, exported.Data.Proxies, 1, "a proxy used only for ticket harvesting must be exported")
	require.Len(t, exported.Data.Accounts, 1)
	require.NotNil(t, exported.Data.Accounts[0].TicketProxyKey)
	require.Nil(t, service.CodexTicketProxyID(&service.Account{Extra: exported.Data.Accounts[0].Extra}))
	destination, importer := setupAccountDataRouter()
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	imported := httptest.NewRecorder()
	destination.ServeHTTP(imported, request)
	require.Equal(t, http.StatusOK, imported.Code)
	require.Len(t, importer.createdAccounts, 1)
	id := service.CodexTicketProxyID(&service.Account{Extra: importer.createdAccounts[0].Extra})
	require.NotNil(t, id)
	require.Equal(t, int64(400), *id)
}

func TestCodexTicketLegacyImportDisablesUnmappedProxy(t *testing.T) {
	router, svc := setupAccountDataRouter()
	payload := DataImportRequest{Data: DataPayload{Type: dataType, Version: dataVersion, Proxies: []DataProxy{}, Accounts: []DataAccount{{Name: "legacy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: map[string]any{service.CodexTicketPolicyExtraKey: map[string]any{"proxy_id": 11}, service.CodexTicketEnabledExtraKey: true}, Concurrency: 1}}}}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, svc.createdAccounts, 1)
	require.Nil(t, service.CodexTicketProxyID(&service.Account{Extra: svc.createdAccounts[0].Extra}))
	require.Equal(t, false, svc.createdAccounts[0].Extra[service.CodexTicketEnabledExtraKey])
	var result struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Data.Warnings, 1)
}
