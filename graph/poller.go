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
//
// Speed-oriented behaviour:
//   - adaptive interval: short after a hit, backs off when the inbox is quiet
//   - dedicated HTTP client with timeouts (no DefaultClient stalls)
//   - reuses access tokens until near expiry
//   - drains a page of new mail and immediately re-polls when the page was full
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

	// Interval is the base (minimum) poll period. Defaults to 10s.
	Interval time.Duration

	// MSA rotates the refresh token on every exchange; OnRotated lets the
	// caller persist the new token back to .env.
	OnRotated func(newRefreshToken string)

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
	since       time.Time // high-water mark for incremental fetch

	httpClient *http.Client
}

const (
	defaultScope = "https://graph.microsoft.com/.default offline_access"
	graphBase    = "https://graph.microsoft.com/v1.0/me/messages"
	pageSize     = 50
)

// Run blocks until stop is closed, polling at an adaptive interval.
func (p *Poller) Run(stop <-chan struct{}) {
	base := p.Interval
	if base <= 0 {
		base = 10 * time.Second
	}
	// Cap quiet-time backoff so mail is never more than ~1 minute late by default.
	maxBackoff := base * 4
	if maxBackoff > time.Minute {
		maxBackoff = time.Minute
	}
	if maxBackoff < base {
		maxBackoff = base
	}
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	// Start the high-water mark a little in the past so the first poll also
	// picks up very recently arrived mail, then advance it as we read.
	if p.since.IsZero() {
		p.since = time.Now().Add(-2 * time.Minute)
	}

	backoff := base
	// Poll once immediately on startup so you don't wait a full interval.
	for {
		hit, fullPage := p.pollOnce()
		if hit {
			backoff = base
			// Page was full — more mail may be waiting; drain without sleeping.
			if fullPage {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
		} else if backoff < maxBackoff {
			next := backoff * 2
			if next > maxBackoff {
				next = maxBackoff
			}
			backoff = next
		}

		t := time.NewTimer(backoff)
		select {
		case <-stop:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// pollOnce returns (hadWork, fullPage).
// hadWork is true when any message was returned by Graph (stored or skipped).
// fullPage is true when the response filled the page size (likely more to fetch).
func (p *Poller) pollOnce() (hadWork bool, fullPage bool) {
	token, err := p.fetchAccessToken()
	if err != nil {
		log.Printf("graph token: %v", err)
		return false, false
	}

	// Incremental fetch: only messages received after the high-water mark.
	// Use gt (not ge) after advancing since by 1s so we never re-fetch the last
	// message as a full page of duplicates.
	since := p.since.UTC().Format(time.RFC3339)
	q := url.Values{}
	q.Set("$top", fmt.Sprintf("%d", pageSize))
	q.Set("$orderby", "receivedDateTime asc")
	q.Set("$filter", fmt.Sprintf("receivedDateTime ge %s", since))
	q.Set("$select", "id,subject,from,toRecipients,ccRecipients,body,receivedDateTime,internetMessageId")
	endpoint := graphBase + "?" + q.Encode()

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		log.Printf("graph request: %v", err)
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Prefer allows advanced filters; also keep latency-friendly defaults.
	req.Header.Set("ConsistencyLevel", "eventual")
	req.Header.Set("Prefer", `outlook.body-content-type="text"`)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		log.Printf("graph fetch: %v", err)
		return false, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if resp.StatusCode != 200 {
		log.Printf("graph fetch %d: %s", resp.StatusCode, string(body))
		return false, false
	}

	var result struct {
		Value []handlers.GraphMessage `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("graph decode: %v", err)
		return false, false
	}
	if len(result.Value) == 0 {
		return false, false
	}

	var stored, skipped int
	var latest time.Time
	// ASC order: walk forward so the high-water mark ends at the newest message.
	for i := range result.Value {
		gm := &result.Value[i]
		msg, err := handlers.StoreGraphMessage(p.DB, p.Domain, gm)
		if err != nil {
			if err == handlers.ErrNotForOurDomain {
				skipped++
			} else {
				log.Printf("graph store: %v", err)
				skipped++
			}
		} else if msg != nil {
			stored++
		} else {
			skipped++ // duplicate graph id
		}
		if t, err := time.Parse(time.RFC3339, gm.ReceivedAt); err == nil && t.After(latest) {
			latest = t
		}
	}
	full := len(result.Value) >= pageSize
	if !latest.IsZero() {
		// Graph timestamps are second-precision. Keep since at latest on a full
		// page so same-second overflow is not skipped; GraphID dedup handles
		// re-reads. Step past the second when the page is partial.
		if full {
			p.since = latest
		} else {
			p.since = latest.Add(time.Second)
		}
	}
	log.Printf("graph poll: fetched %d, stored %d, skipped %d", len(result.Value), stored, skipped)
	// Pure-duplicate / not-for-us pages count as idle so adaptive backoff grows.
	return stored > 0 || full, full
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

	hc := p.httpClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.PostForm(tokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
