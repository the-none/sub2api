package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIOAuthHandler) resetReservationAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	if h.resetReservations == nil {
		response.BadRequest(c, "reset reservations are not enabled")
		return 0, false
	}
	return id, true
}

func (h *OpenAIOAuthHandler) GetResetReservation(c *gin.Context) {
	id, ok := h.resetReservationAccountID(c)
	if !ok {
		return
	}
	result, err := h.resetReservations.Get(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OpenAIOAuthHandler) CreateResetReservation(c *gin.Context) {
	id, ok := h.resetReservationAccountID(c)
	if !ok {
		return
	}
	var input struct {
		LeadMinutes int `json:"lead_minutes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid reservation")
		return
	}
	result, err := h.resetReservations.Create(c.Request.Context(), id, input.LeadMinutes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OpenAIOAuthHandler) CancelResetReservation(c *gin.Context) {
	id, ok := h.resetReservationAccountID(c)
	if !ok {
		return
	}
	var input struct {
		ID string `json:"id" binding:"required,uuid"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "A reservation ID is required")
		return
	}
	result, err := h.resetReservations.Cancel(c.Request.Context(), id, input.ID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
