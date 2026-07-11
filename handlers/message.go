package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"tempmail/ingest"
	"tempmail/models"
)

type MessageHandler struct {
	DB     *gorm.DB
	Ingest *ingest.OnDemand // optional; when set, list/get trigger a relay fetch first
}

// messageDetail wraps a Message so the raw RFC822 source (which the model
// serialises with json:"-") is included on the detail endpoint only. Lists stay
// lean because they return the model directly.
type messageDetail struct {
	*models.Message
	Raw string `json:"raw"`
}

func (h *MessageHandler) sync() {
	if h.Ingest != nil {
		_ = h.Ingest.Sync()
	}
}

// ListMessages lists messages for a mailbox, newest first.
// GET /api/mailboxes/:address/messages
//
// When on-demand ingestion is configured, this endpoint first pulls new mail
// from the relay inbox, then returns whatever is stored for the mailbox.
func (h *MessageHandler) ListMessages(c *gin.Context) {
	h.sync()

	address := strings.ToLower(c.Param("address"))
	var mb models.Mailbox
	if err := h.DB.First(&mb, "address = ?", address).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	var msgs []models.Message
	h.DB.Where("mailbox_id = ?", mb.ID).Order("received_at DESC").Find(&msgs)
	c.JSON(http.StatusOK, msgs)
}

// GetMessage returns a single message including the raw source.
// GET /api/messages/:id
func (h *MessageHandler) GetMessage(c *gin.Context) {
	h.sync()

	var msg models.Message
	if err := h.DB.First(&msg, c.Param("id")).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "message not found"})
		return
	}
	c.JSON(http.StatusOK, messageDetail{Message: &msg, Raw: msg.Raw})
}

// DeleteMessage deletes a single message.
// DELETE /api/messages/:id
func (h *MessageHandler) DeleteMessage(c *gin.Context) {
	res := h.DB.Delete(&models.Message{}, c.Param("id"))
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "message not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("id")})
}
