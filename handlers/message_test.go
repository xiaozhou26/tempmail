package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"tempmail/models"
)

func setupMessageRouter(h *MessageHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/mailboxes/:address/messages", h.ListMessages)
	return r
}

func TestListMessagesAutoCreatesConfiguredMailbox(t *testing.T) {
	db := openTestDB(t)
	h := &MessageHandler{
		DB:      db,
		Domains: []string{"muskqq.com", "lulutem.xyz"},
	}
	r := setupMessageRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes/pkbbvies@lulutem.xyz/messages", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "[]" {
		t.Fatalf("body=%s want []", w.Body.String())
	}

	var mailbox models.Mailbox
	if err := db.Where("address = ?", "pkbbvies@lulutem.xyz").First(&mailbox).Error; err != nil {
		t.Fatalf("auto-created mailbox: %v", err)
	}
}

func TestListMessagesKeepsUnknownMailboxAsNotFound(t *testing.T) {
	db := openTestDB(t)
	h := &MessageHandler{DB: db, Domains: []string{"muskqq.com", "lulutem.xyz"}}
	r := setupMessageRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes/pkbbvies@other.example/messages", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListMessagesDoesNotAutoCreateDisabledSubdomain(t *testing.T) {
	db := openTestDB(t)
	h := &MessageHandler{
		DB:              db,
		Domains:         []string{"lulutem.xyz"},
		AllowSubdomains: false,
	}
	r := setupMessageRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes/pkbbvies@random.lulutem.xyz/messages", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
