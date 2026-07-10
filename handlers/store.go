package handlers

import (
	"bytes"
	"strings"
	"time"

	"github.com/jhillyerd/enmime"
	"gorm.io/gorm"
	"tempmail/models"
)

// StoreMessage parses a raw RFC822 message and persists it under the mailbox
// matching the message's recipient address. If the mailbox does not exist it
// is auto-created (with a long expiry) so nothing is lost. Both the webhook
// handler and the IMAP poller call this.
//
// It returns the stored message and nil on success.
func StoreMessage(db *gorm.DB, domain, raw string) (*models.Message, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader([]byte(raw)))
	if err != nil {
		return nil, err
	}

	to := findRecipient(env, domain)
	if to == "" {
		// Not addressed to anyone on our domain — ignore silently.
		return nil, ErrNotForOurDomain
	}

	// Find or lazily create the destination mailbox.
	var mb models.Mailbox
	if err := db.First(&mb, "address = ?", to).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
		local := to
		if i := strings.IndexByte(to, '@'); i >= 0 {
			local = to[:i]
		}
		mb = models.Mailbox{
			Address:   to,
			Name:      local,
			ExpiresAt: time.Now().AddDate(1, 0, 0),
		}
		if e := db.Create(&mb).Error; e != nil {
			return nil, e
		}
	}

	subject := env.GetHeader("Subject")
	from := strings.TrimSpace(env.GetHeader("From"))

	msg := &models.Message{
		MailboxID:  mb.ID,
		From:       from,
		To:         to,
		Subject:    subject,
		TextBody:   env.Text,
		HTMLBody:   env.HTML,
		Raw:        raw,
		ReceivedAt: time.Now(),
	}
	if err := db.Create(msg).Error; err != nil {
		return nil, err
	}
	return msg, nil
}

// findRecipient inspects To / Delivered-To / X-Forwarded-To / X-Original-To
// headers (in that order) and returns the first address whose domain matches.
// Cloudflare Email Routing preserves the original To header, but some
// providers rewrite it, so the alternate headers are a fallback.
func findRecipient(env *enmime.Envelope, domain string) string {
	candidates := []string{
		env.GetHeader("To"),
		env.GetHeader("Delivered-To"),
		env.GetHeader("X-Forwarded-To"),
		env.GetHeader("X-Original-To"),
	}
	suffix := "@" + domain
	for _, h := range candidates {
		for _, addr := range extractAddresses(h) {
			addr = strings.ToLower(strings.TrimSpace(addr))
			if strings.HasSuffix(addr, suffix) {
				return addr
			}
		}
	}
	return ""
}

// extractAddresses pulls bare email addresses out of a header value that may
// contain display names and multiple recipients, e.g.
// `"Bob" <bob@x.com>, carol@y.com`.
func extractAddresses(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	var out []string
	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// <addr@x.com> form
		if i := strings.Index(part, "<"); i >= 0 {
			if j := strings.Index(part, ">"); j > i {
				out = append(out, part[i+1:j])
				continue
			}
		}
		out = append(out, part)
	}
	return out
}
