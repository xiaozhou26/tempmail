// Package graphpoll provides a background poller that reads mail from a
// Microsoft 365 / Outlook.com inbox via the Microsoft Graph API instead of
// IMAP. It is more reliable than IMAP OAuth2 for personal Outlook accounts,
// which often return "User is authenticated but not connected" on IMAP XOAUTH2.

package graphpoll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"tempmail/handlers"
)

// Poller reads new messages from the relay inbox via Graph.
// Prefer FetchOnce (on-demand). Run is optional continuous polling.
//
// Behaviour:
//   - dedicated HTTP client with timeouts
//   - reuses access tokens until near expiry
//   - drains full pages in a single FetchOnce call
type Poller struct {
	DB *gorm.DB
	// Domains used to route a message to the right temporary mailbox.
	Domains []string

	ClientID     string
	ClientSecret string
	TenantID     string
	RefreshToken string
	TokenScope   string
	Account      string // relay inbox address
	MailFolder   string // folder name; empty = default inbox

	// Interval is the poll period. Defaults to 1s.
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

// FetchOnce pulls new mail once (and drains full pages). Safe for concurrent
// callers only if serialized externally (use ingest.OnDemand).
func (p *Poller) FetchOnce(ctx context.Context) error {
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if p.since.IsZero() {
		p.since = time.Now().Add(-2 * time.Minute)
	}
	// Drain until a partial page so backlog is cleared in one client request.
	for {
		_, full, err := p.pollOnce(ctx)
		if err != nil {
			return err
		}
		if !full {
			return nil
		}
	}
}

// Run continuously polls until stop is closed (optional; prefer on-demand).
func (p *Poller) Run(stop <-chan struct{}) {
	base := p.Interval
	if base <= 0 {
		base = time.Second
	}
	for {
		if err := p.FetchOnce(context.Background()); err != nil {
			log.Printf("graph poll: %v", err)
		}
		t := time.NewTimer(base)
		select {
		case <-stop:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// pollOnce returns (hadWork, fullPage, err).
func (p *Poller) pollOnce(ctx context.Context) (hadWork bool, fullPage bool, err error) {
	token, err := p.fetchAccessToken(ctx)
	if err != nil {
		return false, false, err
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

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Prefer allows advanced filters; also keep latency-friendly defaults.
	req.Header.Set("ConsistencyLevel", "eventual")
	req.Header.Set("Prefer", `outlook.body-content-type="text"`)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if resp.StatusCode != 200 {
		return false, false, fmt.Errorf("graph fetch %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Value []handlers.GraphMessage `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, false, err
	}
	if len(result.Value) == 0 {
		return false, false, nil
	}

	var stored, skipped int
	var latest time.Time
	// ASC order: walk forward so the high-water mark ends at the newest message.
	for i := range result.Value {
		gm := &result.Value[i]
		msg, err := handlers.StoreGraphMessage(p.DB, p.Domains, gm)
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
	return stored > 0 || full, full, nil
}

func (p *Poller) fetchAccessToken(ctx context.Context) (string, error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
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
