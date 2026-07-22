package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"tempmail/ingest"
	"tempmail/models"
)

type EmailHandler struct {
	DB       *gorm.DB
	Domain   string   // primary domain (default for mailbox creation)
	Domains  []string // all configured domains
	TTLHours int
	Ingest   *ingest.OnDemand // optional; GetMailbox triggers a relay fetch first
}

// CreateMailboxRequest is the body for POST /api/mailboxes.
type CreateMailboxRequest struct {
	Name     string `json:"name"`      // optional custom local-part; random if empty
	Domain   string `json:"domain"`    // optional; uses primary domain if empty
	TTLHours int    `json:"ttl_hours"` // optional override; defaults to server config
}

// CreateMailbox registers a new temporary mailbox and returns its address.
// POST /api/mailboxes
func (h *EmailHandler) CreateMailbox(c *gin.Context) {
	var req CreateMailboxRequest
	_ = c.ShouldBindJSON(&req) // body is optional

	// Resolve domain: use requested domain if valid, otherwise primary.
	domain := h.Domain
	if req.Domain != "" {
		reqDomain := strings.ToLower(strings.TrimSpace(req.Domain))
		found := false
		for _, d := range h.Domains {
			if d == reqDomain {
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain"})
			return
		}
		domain = reqDomain
	}

	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		// 10-char base32-ish id keeps addresses short and URL-safe.
		name = strings.ReplaceAll(uuid.NewString()[:10], "-", "")
	} else {
		// keep only safe characters for the local part
		name = sanitizeLocalPart(name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid name"})
			return
		}
	}
	address := name + "@" + domain

	// Avoid collisions on custom names.
	if req.Name != "" {
		var count int64
		h.DB.Model(&models.Mailbox{}).Where("address = ?", address).Count(&count)
		if count > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "address already exists"})
			return
		}
	}

	ttl := h.TTLHours
	if req.TTLHours > 0 {
		ttl = req.TTLHours
	}
	mb := &models.Mailbox{
		Address:   address,
		Name:      name,
		ExpiresAt: time.Now().Add(time.Duration(ttl) * time.Hour),
	}
	if err := h.DB.Create(mb).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, mb)
}

// ListMailboxes returns all mailboxes, newest first.
// GET /api/mailboxes
func (h *EmailHandler) ListMailboxes(c *gin.Context) {
	var boxes []models.Mailbox
	h.DB.Order("created_at DESC").Find(&boxes)
	c.JSON(http.StatusOK, boxes)
}

// GetMailbox returns a single mailbox by address, including its messages.
// GET /api/mailboxes/:address
//
// When on-demand ingestion is configured, this endpoint first pulls new mail
// from the relay inbox so the embedded messages list is fresh.
func (h *EmailHandler) GetMailbox(c *gin.Context) {
	if h.Ingest != nil {
		_ = h.Ingest.SyncContext(c.Request.Context())
	}
	address := strings.ToLower(c.Param("address"))
	var mb models.Mailbox
	err := h.DB.Preload("Messages", func(db *gorm.DB) *gorm.DB {
		return db.Order("received_at DESC")
	}).First(&mb, "address = ?", address).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, mb)
}

// DeleteMailbox removes a mailbox and all its messages.
// DELETE /api/mailboxes/:address
func (h *EmailHandler) DeleteMailbox(c *gin.Context) {
	address := strings.ToLower(c.Param("address"))
	res := h.DB.Where("address = ?", address).Delete(&models.Mailbox{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": res.Error.Error()})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": address})
}

// sanitizeLocalPart keeps only a-z0-9._- characters and trims dots.
func sanitizeLocalPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		}
	}
	return strings.Trim(b.String(), ".")
}

// CleanupExpired deletes mailboxes past their expiry. Intended to run on a ticker.
func (h *EmailHandler) CleanupExpired() (int64, error) {
	res := h.DB.Where("expires_at < ?", time.Now()).Delete(&models.Mailbox{})
	if res.Error != nil {
		return 0, fmt.Errorf("cleanup: %w", res.Error)
	}
	return res.RowsAffected, nil
}
