package handlers

import (
	"strings"
	"time"

	"gorm.io/gorm"
	"tempmail/models"
)

// GraphMessage is the subset of the Microsoft Graph message resource that this
// project persists. Graph returns structured JSON (not RFC822), so we map its
// fields onto models.Message directly instead of going through enmime.
type GraphMessage struct {
	ID            string       `json:"id"`
	Subject       string       `json:"subject"`
	From          graphAddr    `json:"from"`
	ToRecipients  []graphRecip `json:"toRecipients"`
	CcRecipients  []graphRecip `json:"ccRecipients"`
	Body          graphBody    `json:"body"`
	ReceivedAt    string       `json:"receivedDateTime"`
	InternetMsgID string       `json:"internetMessageId"`
}

type graphAddr struct {
	EmailAddress struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type graphRecip struct {
	EmailAddress struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type graphBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

// StoreGraphMessage persists a Microsoft Graph message under the mailbox
// matching the first recipient on the configured domain. If the mailbox does
// not exist it is auto-created with a long expiry. The Graph message id is used
// for dedup so re-polling the same message does not insert a duplicate.
//
// Returns the stored message, or nil with ErrNotForOurDomain when no recipient
// is on our domain.
func StoreGraphMessage(db *gorm.DB, domain string, gm *GraphMessage) (*models.Message, error) {
	to := findGraphRecipient(gm, domain)
	if to == "" {
		return nil, ErrNotForOurDomain
	}

	// Dedup by Graph message id: skip if already stored.
	if gm.ID != "" {
		var count int64
		db.Model(&models.Message{}).Where("graph_id = ?", gm.ID).Count(&count)
		if count > 0 {
			return nil, nil
		}
	}

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

	from := ""
	if gm.From.EmailAddress.Address != "" {
		from = strings.TrimSpace(gm.From.EmailAddress.Address)
	}

	text, html := splitGraphBody(&gm.Body)

	received := time.Now()
	if t, err := time.Parse(time.RFC3339, gm.ReceivedAt); err == nil {
		received = t
	}

	msg := &models.Message{
		GraphID:    gm.ID,
		MailboxID:  mb.ID,
		From:       from,
		To:         to,
		Subject:    gm.Subject,
		TextBody:   text,
		HTMLBody:   html,
		Raw:        "",
		ReceivedAt: received,
	}
	if err := db.Create(msg).Error; err != nil {
		return nil, err
	}
	return msg, nil
}

// findGraphRecipient returns the first To recipient whose address is on the
// configured domain.
func findGraphRecipient(gm *GraphMessage, domain string) string {
	suffix := "@" + strings.ToLower(domain)
	for _, list := range [][]graphRecip{gm.ToRecipients, gm.CcRecipients} {
		for _, r := range list {
			addr := strings.ToLower(strings.TrimSpace(r.EmailAddress.Address))
			if strings.HasSuffix(addr, suffix) {
				return addr
			}
		}
	}
	return ""
}

// splitGraphBody returns (text, html) based on the Graph body content type.
// Graph typically returns either a "text" or "html" body; if html, the plain
// text stays empty (enough for the list/detail endpoints).
func splitGraphBody(b *graphBody) (string, string) {
	if b == nil {
		return "", ""
	}
	switch strings.ToLower(b.ContentType) {
	case "html":
		return "", b.Content
	default:
		return b.Content, ""
	}
}
