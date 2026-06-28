package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// UsageAlertHandler handles admin usage alert configuration.
type UsageAlertHandler struct {
	service *service.UsageAlertService
}

func NewUsageAlertHandler(service *service.UsageAlertService) *UsageAlertHandler {
	return &UsageAlertHandler{service: service}
}

type realAccountRequest struct {
	Name       string  `json:"name" binding:"required"`
	Platform   string  `json:"platform" binding:"required"`
	Identifier *string `json:"identifier"`
	Notes      *string `json:"notes"`
}

type attachAccountsRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

type usageAlertAccountResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	RealAccountID *int64 `json:"real_account_id,omitempty"`
}

type usageAlertRealAccountResponse struct {
	ID         int64                        `json:"id"`
	Name       string                       `json:"name"`
	Platform   string                       `json:"platform"`
	Identifier *string                      `json:"identifier,omitempty"`
	Notes      *string                      `json:"notes,omitempty"`
	Accounts   []*usageAlertAccountResponse `json:"accounts,omitempty"`
	CreatedAt  time.Time                    `json:"created_at"`
	UpdatedAt  time.Time                    `json:"updated_at"`
}

type usageAlertRuleRequest struct {
	Name               string   `json:"name"`
	RealAccountID      *int64   `json:"real_account_id"`
	Platform           string   `json:"platform"`
	Window             string   `json:"window" binding:"required"`
	Metric             string   `json:"metric" binding:"required"`
	Operator           string   `json:"operator" binding:"required"`
	Threshold          float64  `json:"threshold"`
	MinResetAfterHours *float64 `json:"min_reset_after_hours"`
	StepPercent        *float64 `json:"step_percent"`
	CooldownMinutes    *int     `json:"cooldown_minutes"`
	Enabled            *bool    `json:"enabled"`
}

type usageAlertWebhookRequest struct {
	Name       string         `json:"name" binding:"required"`
	Type       string         `json:"type"`
	URL        string         `json:"url"`
	Config     map[string]any `json:"config"`
	Enabled    *bool          `json:"enabled"`
	RetryCount *int           `json:"retry_count"`
}

type usageAlertBindingRequest struct {
	RealAccountID int64 `json:"real_account_id" binding:"required"`
	WebhookID     int64 `json:"webhook_id" binding:"required"`
	Enabled       *bool `json:"enabled"`
}

func parseUsageAlertID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid "+name)
		return 0, false
	}
	return id, true
}

func usageAlertEnabledOrDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func usageAlertCooldownOrDefault(value *int) int {
	if value == nil {
		return 240
	}
	return *value
}

func usageAlertRetryOrDefault(value *int) int {
	if value == nil {
		return 2
	}
	return *value
}

func usageAlertRealAccountResponsesFromService(items []*service.RealAccount) []*usageAlertRealAccountResponse {
	out := make([]*usageAlertRealAccountResponse, 0, len(items))
	for _, item := range items {
		out = append(out, usageAlertRealAccountResponseFromService(item))
	}
	return out
}

func usageAlertRealAccountResponseFromService(item *service.RealAccount) *usageAlertRealAccountResponse {
	if item == nil {
		return nil
	}
	out := &usageAlertRealAccountResponse{
		ID:         item.ID,
		Name:       item.Name,
		Platform:   item.Platform,
		Identifier: item.Identifier,
		Notes:      item.Notes,
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
	if len(item.Accounts) > 0 {
		out.Accounts = make([]*usageAlertAccountResponse, 0, len(item.Accounts))
		for _, account := range item.Accounts {
			out.Accounts = append(out.Accounts, usageAlertAccountResponseFromService(account))
		}
	}
	return out
}

func usageAlertAccountResponseFromService(account *service.Account) *usageAlertAccountResponse {
	if account == nil {
		return nil
	}
	return &usageAlertAccountResponse{
		ID:            account.ID,
		Name:          account.Name,
		Platform:      account.Platform,
		Type:          account.Type,
		Status:        account.Status,
		RealAccountID: account.RealAccountID,
	}
}

// ListRealAccounts lists real upstream accounts.
func (h *UsageAlertHandler) ListRealAccounts(c *gin.Context) {
	items, err := h.service.ListRealAccounts(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, usageAlertRealAccountResponsesFromService(items))
}

// GetRealAccount returns a real upstream account.
func (h *UsageAlertHandler) GetRealAccount(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	item, err := h.service.GetRealAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if item == nil {
		response.NotFound(c, "Real account not found")
		return
	}
	response.Success(c, usageAlertRealAccountResponseFromService(item))
}

// CreateRealAccount creates a real upstream account.
func (h *UsageAlertHandler) CreateRealAccount(c *gin.Context) {
	var req realAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.CreateRealAccount(c.Request.Context(), &service.RealAccount{
		Name:       req.Name,
		Platform:   req.Platform,
		Identifier: req.Identifier,
		Notes:      req.Notes,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, usageAlertRealAccountResponseFromService(item))
}

// UpdateRealAccount updates a real upstream account.
func (h *UsageAlertHandler) UpdateRealAccount(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	var req realAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.UpdateRealAccount(c.Request.Context(), &service.RealAccount{
		ID:         id,
		Name:       req.Name,
		Platform:   req.Platform,
		Identifier: req.Identifier,
		Notes:      req.Notes,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, usageAlertRealAccountResponseFromService(item))
}

// DeleteRealAccount deletes a real upstream account and detaches bound sub accounts.
func (h *UsageAlertHandler) DeleteRealAccount(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteRealAccount(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Real account deleted"})
}

// AttachAccounts binds one or more Sub2API accounts to a real upstream account.
func (h *UsageAlertHandler) AttachAccounts(c *gin.Context) {
	realAccountID, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	var req attachAccountsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.AccountIDs) == 0 {
		response.BadRequest(c, "account_ids is required")
		return
	}
	for _, accountID := range req.AccountIDs {
		if err := h.service.AttachAccount(c.Request.Context(), realAccountID, accountID); err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}
	response.Success(c, gin.H{"message": "Accounts attached"})
}

// DetachAccount removes a Sub2API account from its real upstream account.
func (h *UsageAlertHandler) DetachAccount(c *gin.Context) {
	accountID, ok := parseUsageAlertID(c, "account_id")
	if !ok {
		return
	}
	if err := h.service.DetachAccount(c.Request.Context(), accountID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Account detached"})
}

// GetSnapshot returns the latest normalized usage snapshot for a real account.
func (h *UsageAlertHandler) GetSnapshot(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	snapshot, err := h.service.GetSnapshot(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, snapshot)
}

func (h *UsageAlertHandler) ListRules(c *gin.Context) {
	items, err := h.service.ListRules(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *UsageAlertHandler) CreateRule(c *gin.Context) {
	var req usageAlertRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.CreateRule(c.Request.Context(), &service.UsageAlertRule{
		Name:               req.Name,
		RealAccountID:      req.RealAccountID,
		Platform:           req.Platform,
		Window:             req.Window,
		Metric:             req.Metric,
		Operator:           req.Operator,
		Threshold:          req.Threshold,
		MinResetAfterHours: req.MinResetAfterHours,
		StepPercent:        req.StepPercent,
		CooldownMinutes:    usageAlertCooldownOrDefault(req.CooldownMinutes),
		Enabled:            usageAlertEnabledOrDefault(req.Enabled, true),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) UpdateRule(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	var req usageAlertRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.UpdateRule(c.Request.Context(), &service.UsageAlertRule{
		ID:                 id,
		Name:               req.Name,
		RealAccountID:      req.RealAccountID,
		Platform:           req.Platform,
		Window:             req.Window,
		Metric:             req.Metric,
		Operator:           req.Operator,
		Threshold:          req.Threshold,
		MinResetAfterHours: req.MinResetAfterHours,
		StepPercent:        req.StepPercent,
		CooldownMinutes:    usageAlertCooldownOrDefault(req.CooldownMinutes),
		Enabled:            usageAlertEnabledOrDefault(req.Enabled, true),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) DeleteRule(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteRule(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Rule deleted"})
}

func (h *UsageAlertHandler) ListWebhooks(c *gin.Context) {
	items, err := h.service.ListWebhooks(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *UsageAlertHandler) CreateWebhook(c *gin.Context) {
	var req usageAlertWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.CreateWebhook(c.Request.Context(), &service.UsageAlertWebhook{
		Name:       req.Name,
		Type:       req.Type,
		URL:        req.URL,
		Config:     req.Config,
		Enabled:    usageAlertEnabledOrDefault(req.Enabled, true),
		RetryCount: usageAlertRetryOrDefault(req.RetryCount),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) UpdateWebhook(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	var req usageAlertWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.UpdateWebhook(c.Request.Context(), &service.UsageAlertWebhook{
		ID:         id,
		Name:       req.Name,
		Type:       req.Type,
		URL:        req.URL,
		Config:     req.Config,
		Enabled:    usageAlertEnabledOrDefault(req.Enabled, true),
		RetryCount: usageAlertRetryOrDefault(req.RetryCount),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) DeleteWebhook(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteWebhook(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Webhook deleted"})
}

func (h *UsageAlertHandler) TestWebhook(c *gin.Context) {
	var req usageAlertWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	err := h.service.TestWebhook(c.Request.Context(), &service.UsageAlertWebhook{
		Name:       req.Name,
		Type:       req.Type,
		URL:        req.URL,
		Config:     req.Config,
		Enabled:    true,
		RetryCount: usageAlertRetryOrDefault(req.RetryCount),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "Webhook test sent"})
}

func (h *UsageAlertHandler) ListBindings(c *gin.Context) {
	items, err := h.service.ListBindings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *UsageAlertHandler) CreateBinding(c *gin.Context) {
	var req usageAlertBindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.CreateBinding(c.Request.Context(), &service.UsageAlertBinding{
		RealAccountID: req.RealAccountID,
		WebhookID:     req.WebhookID,
		Enabled:       usageAlertEnabledOrDefault(req.Enabled, true),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) UpdateBinding(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	var req usageAlertBindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.service.UpdateBinding(c.Request.Context(), &service.UsageAlertBinding{
		ID:            id,
		RealAccountID: req.RealAccountID,
		WebhookID:     req.WebhookID,
		Enabled:       usageAlertEnabledOrDefault(req.Enabled, true),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *UsageAlertHandler) DeleteBinding(c *gin.Context) {
	id, ok := parseUsageAlertID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteBinding(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Binding deleted"})
}
