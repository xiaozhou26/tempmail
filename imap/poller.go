package imappoll

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	imaplib "github.com/emersion/go-imap"
	idle "github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"
	"gorm.io/gorm"
	"tempmail/handlers"
)

// Poller connects to an IMAP server, fetches unread messages, and stores them.
// Prefer FetchOnce (on-demand). Run is optional continuous polling.
//
// Behaviour:
//   - reuses one TCP/TLS session across FetchOnce calls when possible
//   - drains all unread mail in a single FetchOnce
//   - Run may use IMAP IDLE when continuously polling
type Poller struct {
	DB       *gorm.DB
	Domains  []string
	Host     string
	Port     int
	Username string
	Password string
	Mailbox  string // INBOX by default
	UseTLS   bool   // true = DialTLS (implicit TLS, port 993) — Firstmail/Gmail
	// StartTLS upgrades a plain TCP connection. Ignored when UseTLS is true.
	StartTLS bool
	// InsecureSkipVerify skips TLS cert verification (debug only).
	InsecureSkipVerify bool
	Interval           time.Duration // poll interval when IDLE is unavailable; defaults to 1s

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
	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time

	// shared HTTP client for OAuth token exchange
	httpClient *http.Client

	// connMu guards the reused IMAP client for on-demand FetchOnce.
	connMu sync.Mutex
	conn   *client.Client
}

// FetchOnce connects, drains UNSEEN messages into the DB, then closes the
// session. Safe for concurrent use only if serialized (ingest.OnDemand).
//
// The connection is not kept idle between calls: many providers (and local
// proxies) drop quiet TLS sessions, and go-imap's client Timeout would otherwise
// log "i/o timeout" ~60s after the last command.
func (p *Poller) FetchOnce(ctx context.Context) error {
	if p.Mailbox == "" {
		p.Mailbox = "INBOX"
	}
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	p.connMu.Lock()
	defer p.connMu.Unlock()

	// Drop any leftover session from a previous interrupted call.
	if p.conn != nil {
		_ = p.conn.Logout()
		p.conn = nil
	}

	c, err := p.connect(ctx)
	if err != nil {
		return err
	}
	// Always close when done so the server/proxy does not hold a half-open
	// socket that later surfaces as "error reading response: i/o timeout".
	defer func() {
		_ = c.Logout()
		p.conn = nil
	}()
	p.conn = c

	// Drain until no unread remain.
	for {
		fetched, err := p.fetchUnread(c)
		if err != nil {
			return err
		}
		if fetched == 0 {
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
	if p.Mailbox == "" {
		p.Mailbox = "INBOX"
	}
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	var c *client.Client
	defer func() {
		if c != nil {
			_ = c.Logout()
		}
	}()

	reconnect := func() error {
		if c != nil {
			_ = c.Logout()
			c = nil
		}
		nc, err := p.connect(context.Background())
		if err != nil {
			return err
		}
		c = nc
		if _, err := c.Select(p.Mailbox, false); err != nil {
			_ = c.Logout()
			c = nil
			return fmt.Errorf("select %q: %w", p.Mailbox, err)
		}
		return nil
	}

	// Connect immediately so the first poll does not wait a full interval.
	if err := reconnect(); err != nil {
		log.Printf("imap connect: %v", err)
	}

	for {
		// Honour stop before doing work.
		select {
		case <-stop:
			return
		default:
		}

		if c == nil {
			if err := reconnect(); err != nil {
				log.Printf("imap connect: %v", err)
				if !sleepOrStop(stop, base) {
					return
				}
				continue
			}
		}

		fetched, err := p.fetchUnread(c)
		if err != nil {
			log.Printf("imap poll: %v", err)
			// Drop the session and reconnect next loop.
			_ = c.Logout()
			c = nil
			if !sleepOrStop(stop, base) {
				return
			}
			continue
		}

		if fetched > 0 {
			// Drain backlog immediately; do not wait for the next interval.
			continue
		}

		// No new mail. Prefer IDLE (near-instant wake-up); fall back to 1s sleep.
		if supportsIDLE(c) {
			if err := p.idleWait(c, stop, 25*time.Minute); err != nil {
				if errors.Is(err, errStopped) {
					return
				}
				// IDLE failure is usually a dropped connection.
				log.Printf("imap idle: %v", err)
				_ = c.Logout()
				c = nil
				if !sleepOrStop(stop, base) {
					return
				}
			}
			// On IDLE wake-up (new mail or timeout), loop and fetch.
			continue
		}

		if !sleepOrStop(stop, base) {
			return
		}
	}
}

func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func supportsIDLE(c *client.Client) bool {
	if c == nil {
		return false
	}
	caps, err := c.Capability()
	if err != nil {
		return false
	}
	return caps["IDLE"]
}

// idleWait blocks until the server signals a mailbox change, stop is closed,
// or maxWait elapses (servers typically drop IDLE after ~30 minutes).
func (p *Poller) idleWait(c *client.Client, stop <-chan struct{}, maxWait time.Duration) error {
	updates := make(chan client.Update, 8)
	c.Updates = updates
	defer func() { c.Updates = nil }()

	idleClient := idle.NewClient(c)
	done := make(chan error, 1)
	idleStop := make(chan struct{})

	go func() {
		// IdleWithFallback falls back to NOOP polling if IDLE is unavailable.
		done <- idleClient.IdleWithFallback(idleStop, 0)
	}()

	timer := time.NewTimer(maxWait)
	defer timer.Stop()

	var idleErr error
	select {
	case <-stop:
		close(idleStop)
		<-done
		return errStopped
	case <-timer.C:
		close(idleStop)
		idleErr = <-done
		return idleErr
	case update := <-updates:
		// Any EXISTS/RECENT/EXPUNGE-style update means we should re-fetch.
		_ = update
		close(idleStop)
		idleErr = <-done
		// Drain remaining updates so the channel does not block the library.
		for {
			select {
			case <-updates:
			default:
				return idleErr
			}
		}
	case idleErr = <-done:
		return idleErr
	}
}

var errStopped = errors.New("stopped")

// fetchUnread searches UNSEEN, stores each message, and marks them \Seen.
// Returns the number of messages fetched from the server (including skipped).
func (p *Poller) fetchUnread(c *client.Client) (int, error) {
	// Re-select to refresh EXISTS/RECENT after IDLE.
	if _, err := c.Select(p.Mailbox, false); err != nil {
		return 0, fmt.Errorf("select %q: %w", p.Mailbox, err)
	}

	criteria := imaplib.NewSearchCriteria()
	criteria.WithoutFlags = []string{imaplib.SeenFlag}
	ids, err := c.Search(criteria)
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	seqset := new(imaplib.SeqSet)
	seqset.AddNum(ids...)

	section, _ := imaplib.ParseBodySectionName("RFC822")
	ch := make(chan *imaplib.Message, 16)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []imaplib.FetchItem{section.FetchItem()}, ch)
	}()

	var stored, skipped int
	var toMarkSeen []uint32
	for msg := range ch {
		r := msg.GetBody(section)
		if r == nil {
			skipped++
			toMarkSeen = append(toMarkSeen, msg.SeqNum)
			continue
		}
		raw, err := io.ReadAll(r)
		if err != nil || len(raw) == 0 {
			skipped++
			toMarkSeen = append(toMarkSeen, msg.SeqNum)
			continue
		}
		if _, err := handlers.StoreMessage(p.DB, p.Domains, string(raw)); err != nil {
			if errors.Is(err, handlers.ErrNotForOurDomain) {
				// not for us — still mark seen so we never loop on it
			} else {
				log.Printf("store message: %v", err)
			}
			skipped++
		} else {
			stored++
		}
		toMarkSeen = append(toMarkSeen, msg.SeqNum)
	}
	if err := <-done; err != nil {
		return len(ids), fmt.Errorf("fetch: %w", err)
	}

	if len(toMarkSeen) > 0 {
		flagSet := new(imaplib.SeqSet)
		flagSet.AddNum(toMarkSeen...)
		if err := c.Store(flagSet, imaplib.AddFlags, []interface{}{imaplib.SeenFlag}, nil); err != nil {
			log.Printf("imap mark seen: %v", err)
		}
	}
	log.Printf("imap poll: fetched %d, stored %d, skipped %d", len(ids), stored, skipped)
	return len(ids), nil
}

func (p *Poller) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName:         p.Host,
		InsecureSkipVerify: p.InsecureSkipVerify, //nolint:gosec // optional escape hatch
		MinVersion:         tls.VersionTLS12,
	}
}

type contextDialer struct {
	ctx  context.Context
	done <-chan struct{}
	net.Dialer
}

func (d *contextDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := d.DialContext(d.ctx, network, address)
	if err != nil {
		return nil, err
	}
	go func() {
		select {
		case <-d.ctx.Done():
			_ = conn.Close()
		case <-d.done:
		}
	}()
	return conn, nil
}

func (p *Poller) connect(ctx context.Context) (*client.Client, error) {
	if p.Host == "" {
		return nil, fmt.Errorf("imap host is empty")
	}
	port := p.Port
	if port == 0 {
		if p.UseTLS {
			port = 993
		} else {
			port = 143
		}
	}
	addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", port))

	if ctx == nil {
		ctx = context.Background()
	}
	dialDone := make(chan struct{})
	defer close(dialDone)
	dialer := &contextDialer{
		ctx:  ctx,
		done: dialDone,
		Dialer: net.Dialer{
			Timeout: 20 * time.Second,
		},
	}

	var c *client.Client
	var err error
	switch {
	case p.UseTLS:
		// Implicit TLS — Firstmail (imap.firstmail.ltd:993), Gmail, most hosts.
		c, err = client.DialWithDialerTLS(dialer, addr, p.tlsConfig())
	case p.StartTLS:
		c, err = client.DialWithDialer(dialer, addr)
		if err == nil {
			if err = c.StartTLS(p.tlsConfig()); err != nil {
				_ = c.Logout()
				return nil, fmt.Errorf("starttls: %w", err)
			}
		}
	default:
		c, err = client.DialWithDialer(dialer, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c.Timeout = 45 * time.Second

	mode := strings.ToLower(strings.TrimSpace(p.AuthMode))
	if mode == "" {
		mode = "plain"
	}

	if mode == "oauth2" {
		token, err := p.fetchAccessToken(ctx)
		if err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("oauth2 token: %w", err)
		}
		if err := c.Authenticate(newXOAuth2SASL(p.Username, token)); err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("xoauth2 authenticate: %w", err)
		}
	} else {
		// Plain LOGIN — Firstmail, Gmail app password, most providers.
		if err := c.Login(p.Username, p.Password); err != nil {
			_ = c.Logout()
			return nil, fmt.Errorf("login as %q: %w", p.Username, err)
		}
	}

	return c, nil
}

func (p *Poller) fetchAccessToken(ctx context.Context) (string, error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	hc := p.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
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
