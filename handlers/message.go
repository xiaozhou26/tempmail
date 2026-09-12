package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"tempmail/ingest"
	"tempmail/models"
)

type MessageHandler struct {
	DB              *gorm.DB
	Domains         []string
	AllowSubdomains bool
	Ingest          *ingest.OnDemand // optional; when set, list/get trigger a relay fetch first
}

// messageDetail wraps a Message so the raw RFC822 source (which the model
// serialises with json:"-") is included on the detail endpoint only. Lists stay
// lean because they return the model directly.
type messageDetail struct {
	*models.Message
	Raw string `json:"raw"`
}

func (h *MessageHandler) sync(c *gin.Context) {
	if h.Ingest != nil {
		_ = h.Ingest.SyncContext(c.Request.Context())
	}
}

func (h *MessageHandler) isConfiguredAddress(address string) bool {
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return false
	}
	domain := address[at+1:]
	for _, configured := range h.Domains {
		configured = strings.ToLower(strings.TrimSpace(configured))
		if domain == configured {
			return true
		}
		if h.AllowSubdomains && strings.HasSuffix(domain, "."+configured) {
			return true
		}
	}
	return false
}

// ListMessages lists messages for a mailbox, newest first.
// GET /api/mailboxes/:address/messages
//
// When on-demand ingestion is configured, this endpoint first pulls new mail
// from the relay inbox, then returns whatever is stored for the mailbox.
func (h *MessageHandler) ListMessages(c *gin.Context) {
	h.sync(c)

	address := strings.ToLower(c.Param("address"))
	var mb models.Mailbox
	if err := h.DB.First(&mb, "address = ?", address).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		if h.isConfiguredAddress(address) {
			local := address
			if i := strings.IndexByte(address, '@'); i >= 0 {
				local = address[:i]
			}
			mb = models.Mailbox{
				Address:   address,
				Name:      local,
				ExpiresAt: time.Now().AddDate(1, 0, 0),
			}
			if createErr := h.DB.Create(&mb).Error; createErr != nil {
				if retryErr := h.DB.First(&mb, "address = ?", address).Error; retryErr != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": createErr.Error()})
					return
				}
			}
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
			return
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var msgs []models.Message
	// Omit the potentially multi-MB Raw column: it is json:"-" and never appears
	// in list output, so loading it would waste I/O and memory on every poll.
	h.DB.Omit("Raw").Where("mailbox_id = ?", mb.ID).Order("received_at DESC").Find(&msgs)
	c.JSON(http.StatusOK, msgs)
}

// GetMessage returns a single message including the raw source.
// GET /api/messages/:id
func (h *MessageHandler) GetMessage(c *gin.Context) {
	h.sync(c)

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
