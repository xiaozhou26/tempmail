package handlers

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"tempmail/models"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCleanupOldMessages(t *testing.T) {
	db := openTestDB(t)
	h := &EmailHandler{DB: db}

	mb := models.Mailbox{Address: "a@example.com", Name: "a", ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	old := models.Message{
		MailboxID:  mb.ID,
		From:       "x@y.com",
		To:         mb.Address,
		Subject:    "old",
		TextBody:   "old",
		ReceivedAt: time.Now().Add(-48 * time.Hour),
	}
	fresh := models.Message{
		MailboxID:  mb.ID,
		From:       "x@y.com",
		To:         mb.Address,
		Subject:    "new",
		TextBody:   "new",
		ReceivedAt: time.Now(),
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}

	n, err := h.CleanupOldMessages(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d want 1", n)
	}
	var count int64
	db.Model(&models.Message{}).Count(&count)
	if count != 1 {
		t.Fatalf("remaining=%d want 1", count)
	}
	var left models.Message
	db.First(&left)
	if left.Subject != "new" {
		t.Fatalf("left subject=%q", left.Subject)
	}
}
