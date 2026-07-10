// Package graphpoll provides a background poller that reads mail from a
// Microsoft 365 / Outlook.com inbox via the Microsoft Graph API instead of
// IMAP. It is more reliable than IMAP OAuth2 for personal Outlook accounts,
// which often return "User is authenticated but not connected" on IMAP XOAUTH2.
package graphpoll

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"gorm.io/gorm"
	"tempmail/handlers"
)

// Poller periodically reads new messages from the relay inbox via Graph.
type Poller struct {
	DB *gorm.DB
	// Domain used to route a message to the right temporary mailbox.
	Domain string

	ClientID     string
	ClientSecret string
	TenantID     string
	RefreshToken string
	TokenScope   string
	Account      string // relay inbox address
	MailFolder   string // folder name; empty = default inbox

	Interval time.Duration

	// MSA rotates the refresh token on every exchange; OnRotated lets the
	// caller persist the new token back to .env.
	OnRotated func(newRefreshToken string)

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
	since       time.Time // high-water mark for incremental fetch
}

const (
	defaultTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	defaultScope    = "https://graph.microsoft.com/.default offline_access"
	graphBase      = "https://graph.microsoft.com/v1.0/me/messages"
)

// Run blocks until stop is closed, polling at each tick.
func (p *Poller) Run(stop <-chan struct{}) {
	interval := p.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	// Start the high-water mark a little in the past so the first poll also
	// picks up very recently arrived mail, then advance it as we read.
	if p.since.IsZero() {
		p.since = time.Now().Add(-2 * time.Minute)
	}
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
	token, err := p.fetchAccessToken()
	if err != nil {
		log.Printf("graph token: %v", err)
		return
	}

	// Incremental fetch: only messages received after the high-water mark.
	since := p.since.UTC().Format(time.RFC3339)
	q := url.Values{}
	q.Set("$top", "50")
	q.Set("$orderby", "receivedDateTime DESC")
	q.Set("$filter", fmt.Sprintf("receivedDateTime ge %s", since))
	q.Set("$select", "id,subject,from,toRecipients,body,receivedDateTime,internetMessageId")
	endpoint := graphBase + "?" + q.Encode()

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		log.Printf("graph request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ConsistencyLevel", "eventual")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("graph fetch: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("graph fetch %d: %s", resp.StatusCode, string(body))
		return
	}

	var result struct {
		Value []handlers.GraphMessage `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("graph decode: %v", err)
		return
	}

	var stored, skipped int
	var latest time.Time
	// result is DESC by receivedDateTime; iterate oldest-first so the
	// high-water mark ends up at the newest message.
	for i := len(result.Value) - 1; i >= 0; i-- {
		gm := &result.Value[i]
		msg, err := handlers.StoreGraphMessage(p.DB, p.Domain, gm)
		if err != nil {
			if err == handlers.ErrNotForOurDomain {
				skipped++
			} else {
				log.Printf("graph store: %v", err)
				skipped++
			}
			continue
		}
		if msg != nil {
			stored++
		} else {
			skipped++ // duplicate graph id
		}
		if t, err := time.Parse(time.RFC3339, gm.ReceivedAt); err == nil && t.After(latest) {
			latest = t
		}
	}
	if !latest.IsZero() {
		p.since = latest.Add(time.Second)
	}
	log.Printf("graph poll: fetched %d, stored %d, skipped %d", len(result.Value), stored, skipped)
}

func (p *Poller) fetchAccessToken() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	scope := p.TokenScope
	if scope == "" {
		scope = defaultScope
	}
	tenant := p.TenantID
	if tenant == "" {
		tenant = "common"
	}
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)

	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.RefreshToken)
	form.Set("scope", scope)
	if p.ClientSecret != "" {
		form.Set("client_secret", p.ClientSecret)
	}

	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(b))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}

	if tok.RefreshToken != "" && tok.RefreshToken != p.RefreshToken {
		p.RefreshToken = tok.RefreshToken
		if p.OnRotated != nil {
			p.OnRotated(tok.RefreshToken)
		}
	}

	p.accessToken = tok.AccessToken
	exp := time.Duration(tok.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = time.Hour
	}
	p.tokenExpiry = time.Now().Add(exp).Add(-60 * time.Second)
	return tok.AccessToken, nil
}
