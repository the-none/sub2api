package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetCodexTicketGateway(gateway *service.OpenAIGatewayService) {
	h.codexTicketGateway = gateway
}

func (h *AccountHandler) GetCodexTicketSettings(c *gin.Context) {
	if h.codexTicketSettings == nil {
		response.Error(c, 503, "Ticket settings unavailable")
		return
	}
	fallback := config.OpenAICodexTicketConfig{}
	if h.cfg != nil {
		fallback = h.cfg.Gateway.OpenAICodexTicket
	}
	cfg := h.codexTicketSettings.GetCodexTicketConfig(c.Request.Context(), fallback)
	cfg.HarvestProxyURL = service.MaskProxyURL(cfg.HarvestProxyURL)
	response.Success(c, cfg)
}

func (h *AccountHandler) UpdateCodexTicketSettings(c *gin.Context) {
	if h.codexTicketSettings == nil {
		response.Error(c, 503, "Ticket settings unavailable")
		return
	}
	var req struct {
		config.OpenAICodexTicketConfig
		ClearProxy bool `json:"clear_proxy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid ticket settings")
		return
	}
	if err := h.codexTicketSettings.SaveCodexTicketConfig(c.Request.Context(), req.OpenAICodexTicketConfig, req.ClearProxy); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.GetCodexTicketSettings(c)
}

func (h *AccountHandler) ticketAccountID(c *gin.Context) (int64, bool) {
	if h.codexTicketGateway == nil {
		response.Error(c, 503, "Ticket service unavailable")
		return 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}

func (h *AccountHandler) GetCodexTicketAccount(c *gin.Context) {
	id, ok := h.ticketAccountID(c)
	if !ok {
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.codexTicketGateway.CodexTicketAccountView(c.Request.Context(), account))
}

func (h *AccountHandler) UpdateCodexTicketAccount(c *gin.Context) {
	id, ok := h.ticketAccountID(c)
	if !ok {
		return
	}
	var req service.CodexTicketPolicy
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid ticket policy")
		return
	}
	if err := h.codexTicketGateway.SaveCodexTicketPolicy(c.Request.Context(), id, req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.GetCodexTicketAccount(c)
}

func (h *AccountHandler) HarvestCodexTicket(c *gin.Context) {
	id, ok := h.ticketAccountID(c)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "model is required")
		return
	}
	state, err := h.codexTicketGateway.TriggerCodexTicket(c.Request.Context(), id, req.Model)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"state": state})
}
