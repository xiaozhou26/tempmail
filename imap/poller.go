package imappoll

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	imaplib "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"gorm.io/gorm"
	"tempmail/handlers"
)

// Poller periodically connects to an IMAP server, fetches unread messages, and
// stores them. It marks fetched messages as \Seen so they are not re-fetched.
type Poller struct {
	DB       *gorm.DB
	Domain   string
	Host     string
	Port     int
	Username string
	Password string
	Mailbox  string        // INBOX by default
	UseTLS   bool          // true = DialTLS, false = plain (NOT recommended)
	Interval time.Duration // how often to poll; defaults to 60s

	// Outlook OAuth2 / XOAUTH2
	AuthMode     string // plain | oauth2
	ClientID     string
	TenantID     string
	RefreshToken string
	TokenScope   string // OAuth2 scope; defaults to https://outlook.office.com/IMAP.AccessAsUser.All offline_access

	// MSA refresh tokens are single-use: each exchange rotates the refresh
	// token. OnRotated is called with the new token so callers can persist it
	// (e.g. back into .env); otherwise the next process start fails with
	// invalid_grant. Optional — nil disables persistence.
	OnRotated func(newRefreshToken string)

	// token cache so we don't exchange (and rotate) on every poll cycle.
	mu            sync.Mutex
	accessToken   string
	tokenExpiry   time.Time
}

// Run blocks until stop is closed, polling the IMAP server at each tick.
func (p *Poller) Run(stop <-chan struct{}) {
	interval := p.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	// Poll once immediately on startup so you don't wait a full interval.
	p.pollOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.pollOnce()
		case <-stop:
			return
		}
	}
}

func (p *Poller) pollOnce() {
	c, err := p.connect()
	if err != nil {
		log.Printf("imap connect: %v", err)
		return
	}
	defer func() {
		_ = c.Logout()
	}()

	mbox, err := c.Select(p.Mailbox, false)
	if err != nil {
		log.Printf("imap select %q: %v", p.Mailbox, err)
		return
	}
	if mbox.Messages == 0 {
		return
	}

	// Search for unread messages. Using \Seen as the high-water mark keeps
	// restarts safe: anything already read by an earlier run is skipped.
	criteria := imaplib.NewSearchCriteria()
	criteria.WithoutFlags = []string{imaplib.SeenFlag}
	ids, err := c.Search(criteria)
	if err != nil {
		log.Printf("imap search: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	seqset := new(imaplib.SeqSet)
	seqset.AddNum(ids...)

	// Fetch the full RFC822 body for each matched message.
	section, _ := imaplib.ParseBodySectionName("RFC822")
	ch := make(chan *imaplib.Message, len(ids))
	go func() {
		if err := c.Fetch(seqset, []imaplib.FetchItem{section.FetchItem()}, ch); err != nil {
			log.Printf("imap fetch: %v", err)
		}
	}()

	var stored, skipped int
	var toMarkSeen []uint32
	for msg := range ch {
		r := msg.GetBody(section)
		if r == nil {
			skipped++
			continue
		}
		raw, err := io.ReadAll(r)
		if err != nil || len(raw) == 0 {
			skipped++
			toMarkSeen = append(toMarkSeen, msg.SeqNum)
			continue
		}
		if _, err := handlers.StoreMessage(p.DB, p.Domain, string(raw)); err != nil {
			if errors.Is(err, handlers.ErrNotForOurDomain) {
				// Forwarded mail not for our domain — still mark read so we
				// don't loop on it forever.
			} else {
				log.Printf("store message: %v", err)
			}
			skipped++
		} else {
			stored++
		}
		toMarkSeen = append(toMarkSeen, msg.SeqNum)
	}

	// Mark everything we pulled as \Seen so it won't be re-fetched next cycle.
	if len(toMarkSeen) > 0 {
		flagSet := new(imaplib.SeqSet)
		flagSet.AddNum(toMarkSeen...)
		if err := c.Store(flagSet, imaplib.AddFlags, []interface{}{imaplib.SeenFlag}, nil); err != nil {
			log.Printf("imap mark seen: %v", err)
		}
	}
	log.Printf("imap poll: fetched %d, stored %d, skipped %d", len(ids), stored, skipped)
}

func (p *Poller) connect() (*client.Client, error) {
	addr := fmt.Sprintf("%s:%d", p.Host, p.Port)
	var c *client.Client
	var err error
	if p.UseTLS {
		c, err = client.DialTLS(addr, nil)
	} else {
		c, err = client.Dial(addr)
	}
	if err != nil {
		return nil, err
	}

	if p.AuthMode == "oauth2" {
		token, err := p.fetchAccessToken()
		if err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("oauth2 token: %w", err)
		}
		if err := c.Authenticate(newXOAuth2SASL(p.Username, token)); err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("xoauth2 authenticate: %w", err)
		}
	} else {
		if err := c.Login(p.Username, p.Password); err != nil {
			_ = c.Logout()
			return nil, err
		}
	}

	return c, nil
}

func (p *Poller) fetchAccessToken() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Reuse a cached token if it is still valid.
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.RefreshToken)
	scope := p.TokenScope
	if scope == "" {
		scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"
	}
	form.Set("scope", scope)

	tenant := p.TenantID
	if tenant == "" {
		tenant = "consumers"
	}
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)
	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}

	// MSA rotates the refresh token on every exchange. Persist the new one
	// in memory and notify the caller so it can be written back to disk.
	if tok.RefreshToken != "" && tok.RefreshToken != p.RefreshToken {
		p.RefreshToken = tok.RefreshToken
		if p.OnRotated != nil {
			p.OnRotated(tok.RefreshToken)
		}
	}

	p.accessToken = tok.AccessToken
	// Refresh slightly before real expiry; default 1h if omitted.
	exp := time.Duration(tok.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = time.Hour
	}
	p.tokenExpiry = time.Now().Add(exp).Add(-60 * time.Second)
	return tok.AccessToken, nil
}

type xoauth2SASL struct {
	raw string
}

func newXOAuth2SASL(user, accessToken string) *xoauth2SASL {
	raw := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", user, accessToken)
	return &xoauth2SASL{raw: raw}
}

func (a *xoauth2SASL) Start() (string, []byte, error) {
	// go-imap base64-encodes this initial response itself; return it raw.
	return "XOAUTH2", []byte(a.raw), nil
}

func (a *xoauth2SASL) Next(challenge []byte) ([]byte, error) {
	// XOAUTH2: after a successful auth the server may send a final empty
	// challenge. Returning nil signals "no further response" so go-imap
	// finalises the SASL exchange instead of sending another token.
	return nil, nil
}
