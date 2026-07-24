package models

import "time"

// Mailbox represents a temporary email address registered by a user.
// Mailboxes are virtual: any address on the domain can receive mail via the
// Cloudflare catch-all routing rule, but only registered mailboxes (and their
// messages) are persisted and queryable through the API.
type Mailbox struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Address   string    `gorm:"uniqueIndex;not null" json:"address"`
	Name      string    `json:"name"` // local part (before @)
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Messages  []Message `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE" json:"messages,omitempty"`
}

// Message is a single inbound email stored after the Cloudflare Worker posts
// the raw message to the webhook endpoint.
type Message struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	MailboxID  uint      `gorm:"not null;index:idx_msg_mbox_received,priority:1" json:"mailbox_id"`
	GraphID    string    `gorm:"index" json:"-"` // Microsoft Graph message id, for dedup when using the Graph poller
	From       string    `gorm:"index" json:"from"`
	To         string    `json:"to"`
	Subject    string    `json:"subject"`
	TextBody   string    `json:"text_body"`
	HTMLBody   string    `json:"html_body"`
	Raw        string    `gorm:"type:text" json:"-"` // full RFC822 source, omitted from JSON lists
	ReceivedAt time.Time `gorm:"index;index:idx_msg_mbox_received,priority:2" json:"received_at"`
}
