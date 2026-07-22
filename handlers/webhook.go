package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ErrNotForOurDomain is returned by StoreMessage when none of the recipient
// headers reference an address on the configured domain.
var ErrNotForOurDomain = errors.New("message not addressed to this domain")

type WebhookHandler struct {
	DB      *gorm.DB
	Domains []string
}

// WebhookPayload mirrors what a push source (e.g. an Email Worker, should you
// ever use one) posts. Kept available as an optional ingress path; the IMAP
// poller is the default ingest method and does not consume Worker requests.
type WebhookPayload struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Raw     string `json:"raw"` // full RFC822 message
}

// Receive accepts a pushed raw RFC822 message and stores it.
// POST /api/webhook/email  (X-Webhook-Secret)
//
// This endpoint is optional. It is only enabled when WEBHOOK_SECRET is set;
// otherwise the middleware refuses all calls. The IMAP poller is the
// recommended, Worker-free ingestion path.
func (h *WebhookHandler) Receive(c *gin.Context) {
	var p WebhookPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(p.Raw) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'raw' field"})
		return
	}
	msg, err := StoreMessage(h.DB, h.Domains, p.Raw)
	if err != nil {
		if errors.Is(err, ErrNotForOurDomain) {
			c.JSON(http.StatusAccepted, gin.H{"ok": true, "ignored": "not for our domain"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message_id": msg.ID, "mailbox": msg.To})
}
