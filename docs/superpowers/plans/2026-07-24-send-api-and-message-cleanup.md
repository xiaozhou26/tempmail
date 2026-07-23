# Send API + Message-Age Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /api/send` for outbound email (relay preferred, direct MX fallback) and age-based message cleanup so the SQLite DB does not grow without bound.

**Architecture:** A new `sender` package owns delivery (`Sender` interface, relay + direct implementations, RFC822 assembly). A new `handlers.SendHandler` validates the HTTP request and calls `Sender.Send`. Config gains `SMTPSend` and `MessageTTLHours`. Existing `runCleanup` ticker also deletes messages older than `MESSAGE_TTL_HOURS`.

**Tech Stack:** Go 1.26, Gin, GORM + glebarez/sqlite, stdlib `net/smtp` / `net.LookupMX` / `mime/multipart`. No new third-party deps.

**Spec:** `docs/superpowers/specs/2026-07-23-send-api-and-message-cleanup-design.md`

## Global Constraints

- Match existing style: `*gorm.DB` in handlers, `gin.H{"error": ...}` errors, env via `config.Load` helpers.
- From-address domain must be in `cfg.Domains` (anti-open-relay).
- Do not persist sent mail.
- Do not add DKIM or rate limiting.
- Baseline: `go test ./...` and `go vet ./...` must pass.
- Keep CGO-free.

## File map

| File | Role |
|------|------|
| `config/config.go` | `SMTPSendConfig`, `MessageTTLHours` |
| `sender/message.go` | RFC822 body builder |
| `sender/sender.go` | `Sender` interface, relay/direct, `NewSender` |
| `sender/message_test.go` | Unit tests for message assembly |
| `handlers/send.go` | `SendHandler`, `POST /api/send` |
| `handlers/send_test.go` | Handler validation tests |
| `handlers/email.go` | `CleanupOldMessages` |
| `handlers/email_cleanup_test.go` | Cleanup unit test |
| `main.go` | Wire sender, route, cleanup param |
| `.env.example` | Document new env vars |

---

### Task 1: Config — SMTP send + message TTL

**Files:**
- Modify: `config/config.go`
- Modify: `.env.example`

**Interfaces:**
- Produces:
  - `config.SMTPSendConfig{Host, Port int, User, Pass string, StartTLS bool, From string}`
  - `Config.SMTPSend SMTPSendConfig`
  - `Config.MessageTTLHours int`

- [ ] **Step 1: Add types and load fields**

In `config/config.go`, after `SMTPConfig` (around line 56), add:

```go
// SMTPSendConfig holds settings for outbound mail delivery.
// When Host is non-empty, mail is sent via that SMTP relay; otherwise
// the sender falls back to direct MX delivery.
type SMTPSendConfig struct {
	Host     string
	Port     int
	User     string
	Pass     string
	StartTLS bool
	From     string // optional default From when request omits it
}
```

Add to `Config` struct (after `SMTP SMTPConfig`):

```go
	// Outbound SMTP (optional relay). Empty Host => direct MX delivery.
	SMTPSend SMTPSendConfig
	// Max age of stored inbound messages in hours; 0 disables age cleanup.
	MessageTTLHours int
```

In `Load()`, inside the `cfg := &Config{...}` literal:

```go
			MessageTTLHours: 24,
			SMTPSend: SMTPSendConfig{
				Host:     get("SMTP_SEND_HOST", ""),
				Port:     587,
				User:     get("SMTP_SEND_USER", ""),
				Pass:     get("SMTP_SEND_PASS", ""),
				StartTLS: getBool("SMTP_SEND_STARTTLS", true),
				From:     get("SMTP_SEND_FROM", ""),
			},
```

After the other `getInt` blocks (near `GRAPH_POLL_INTERVAL_SEC`), add:

```go
		if n, err := getInt("SMTP_SEND_PORT", cfg.SMTPSend.Port); err == nil {
			cfg.SMTPSend.Port = n
		} else {
			return nil, err
		}
		if n, err := getInt("MESSAGE_TTL_HOURS", cfg.MessageTTLHours); err == nil {
			cfg.MessageTTLHours = n
		} else {
			return nil, err
		}
```

No extra validation: all send fields optional; `MessageTTLHours` may be 0.

- [ ] **Step 2: Document env vars in `.env.example`**

Append under the `===== 服务可选 =====` section (after `CLEANUP_INTERVAL_MIN=30`):

```env
# 入站消息保留时长（小时）；0 = 关闭按年龄清理（仍按邮箱过期清理）
MESSAGE_TTL_HOURS=24

# ===== 发信（可选）=====
# 外部 SMTP 中继。留空则直连收件人 MX（需本机可出 25 端口）
# SMTP_SEND_HOST=smtp.example.com
# SMTP_SEND_PORT=587
# SMTP_SEND_USER=
# SMTP_SEND_PASS=
# SMTP_SEND_STARTTLS=true
# SMTP_SEND_FROM=noreply@yourdomain.com
```

- [ ] **Step 3: Compile-check**

Run: `go build -o tempmail .`
Expected: success (or only existing unrelated errors).

- [ ] **Step 4: Commit**

```bash
git add config/config.go .env.example
git commit -m "feat(config): add SMTP_SEND_* and MESSAGE_TTL_HOURS"
```

---

### Task 2: RFC822 message builder + tests (TDD)

**Files:**
- Create: `sender/message.go`
- Create: `sender/message_test.go`

**Interfaces:**
- Produces: `func BuildMessage(from string, to []string, subject, text, html string) ([]byte, error)`

- [ ] **Step 1: Write failing tests**

Create `sender/message_test.go`:

```go
package sender

import (
	"strings"
	"testing"
)

func TestBuildMessage_textOnly(t *testing.T) {
	raw, err := BuildMessage("a@example.com", []string{"b@x.com"}, "hello", "body", "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "From: a@example.com\r\n") {
		t.Fatalf("missing From: %s", s)
	}
	if !strings.Contains(s, "To: b@x.com\r\n") {
		t.Fatalf("missing To: %s", s)
	}
	if !strings.Contains(s, "Subject: hello\r\n") {
		t.Fatalf("missing Subject: %s", s)
	}
	if !strings.Contains(s, "Content-Type: text/plain; charset=utf-8") {
		t.Fatalf("expected text/plain: %s", s)
	}
	if !strings.Contains(s, "body") {
		t.Fatalf("missing body: %s", s)
	}
	if !strings.Contains(s, "Message-ID:") {
		t.Fatalf("missing Message-ID: %s", s)
	}
	if !strings.Contains(s, "Date:") {
		t.Fatalf("missing Date: %s", s)
	}
}

func TestBuildMessage_htmlOnly(t *testing.T) {
	raw, err := BuildMessage("a@example.com", []string{"b@x.com"}, "sub", "", "<p>hi</p>")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "Content-Type: text/html; charset=utf-8") {
		t.Fatalf("expected text/html: %s", s)
	}
	if !strings.Contains(s, "<p>hi</p>") {
		t.Fatalf("missing html: %s", s)
	}
}

func TestBuildMessage_multipart(t *testing.T) {
	raw, err := BuildMessage("a@example.com", []string{"b@x.com", "c@y.com"}, "sub", "plain", "<b>html</b>")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "multipart/alternative") {
		t.Fatalf("expected multipart: %s", s)
	}
	if !strings.Contains(s, "To: b@x.com, c@y.com\r\n") {
		t.Fatalf("missing multi To: %s", s)
	}
	if !strings.Contains(s, "plain") || !strings.Contains(s, "<b>html</b>") {
		t.Fatalf("missing parts: %s", s)
	}
}

func TestBuildMessage_subjectEncoding(t *testing.T) {
	raw, err := BuildMessage("a@example.com", []string{"b@x.com"}, "你好", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	// non-ASCII subject must be MIME-encoded, not raw
	if strings.Contains(s, "Subject: 你好\r\n") {
		t.Fatalf("subject should be encoded: %s", s)
	}
	if !strings.Contains(s, "Subject: =?") {
		t.Fatalf("expected encoded subject: %s", s)
	}
}

func TestBuildMessage_emptyBody(t *testing.T) {
	_, err := BuildMessage("a@example.com", []string{"b@x.com"}, "s", "", "")
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestBuildMessage_emptyTo(t *testing.T) {
	_, err := BuildMessage("a@example.com", nil, "s", "x", "")
	if err == nil {
		t.Fatal("expected error for empty to")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `go test ./sender/ -v`
Expected: FAIL — `BuildMessage` undefined / package not found.

- [ ] **Step 3: Implement `sender/message.go`**

```go
package sender

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BuildMessage assembles a minimal RFC822 message.
// At least one of text/html must be non-empty; to must be non-empty.
func BuildMessage(from string, to []string, subject, text, html string) ([]byte, error) {
	if len(to) == 0 {
		return nil, fmt.Errorf("to is required")
	}
	if strings.TrimSpace(text) == "" && strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("text or html body is required")
	}

	var buf bytes.Buffer
	domain := "localhost"
	if i := strings.LastIndex(from, "@"); i >= 0 && i+1 < len(from) {
		domain = from[i+1:]
	}
	msgID := fmt.Sprintf("<%s@%s>", uuid.NewString(), domain)

	writeHeader := func(k, v string) {
		buf.WriteString(k)
		buf.WriteString(": ")
		buf.WriteString(v)
		buf.WriteString("\r\n")
	}

	writeHeader("From", from)
	writeHeader("To", strings.Join(to, ", "))
	writeHeader("Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeader("Date", time.Now().Format(time.RFC1123Z))
	writeHeader("Message-ID", msgID)
	writeHeader("MIME-Version", "1.0")

	hasText := strings.TrimSpace(text) != ""
	hasHTML := strings.TrimSpace(html) != ""

	switch {
	case hasText && hasHTML:
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		// text part
		th := textproto.MIMEHeader{}
		th.Set("Content-Type", "text/plain; charset=utf-8")
		th.Set("Content-Transfer-Encoding", "8bit")
		tp, err := w.CreatePart(th)
		if err != nil {
			return nil, err
		}
		if _, err := tp.Write([]byte(text)); err != nil {
			return nil, err
		}
		// html part
		hh := textproto.MIMEHeader{}
		hh.Set("Content-Type", "text/html; charset=utf-8")
		hh.Set("Content-Transfer-Encoding", "8bit")
		hp, err := w.CreatePart(hh)
		if err != nil {
			return nil, err
		}
		if _, err := hp.Write([]byte(html)); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		writeHeader("Content-Type", "multipart/alternative; boundary="+w.Boundary())
		buf.WriteString("\r\n")
		buf.Write(body.Bytes())
	case hasHTML:
		writeHeader("Content-Type", "text/html; charset=utf-8")
		writeHeader("Content-Transfer-Encoding", "8bit")
		buf.WriteString("\r\n")
		buf.WriteString(html)
	default:
		writeHeader("Content-Type", "text/plain; charset=utf-8")
		writeHeader("Content-Transfer-Encoding", "8bit")
		buf.WriteString("\r\n")
		buf.WriteString(text)
	}

	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./sender/ -count=1 -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add sender/message.go sender/message_test.go
git commit -m "feat(sender): add RFC822 message builder"
```

---

### Task 3: Sender interface + relay + direct delivery

**Files:**
- Create: `sender/sender.go`

**Interfaces:**
- Consumes: `BuildMessage` (not required by Sender itself)
- Produces:
  - `type Sender interface { Send(from string, to []string, msg []byte) error }`
  - `func NewSender(host string, port int, user, pass string, startTLS bool) Sender`
  - When `host == ""` → direct; else → relay

- [ ] **Step 1: Implement `sender/sender.go`**

```go
package sender

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"
)

// Sender delivers a pre-built RFC822 message to one or more recipients.
type Sender interface {
	Send(from string, to []string, msg []byte) error
}

// NewSender returns a relay sender when host is non-empty, otherwise direct MX.
func NewSender(host string, port int, user, pass string, startTLS bool) Sender {
	if strings.TrimSpace(host) != "" {
		return &relaySender{
			host:     host,
			port:     port,
			user:     user,
			pass:     pass,
			startTLS: startTLS,
		}
	}
	return &directSender{}
}

type relaySender struct {
	host     string
	port     int
	user     string
	pass     string
	startTLS bool
}

func (s *relaySender) Send(from string, to []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}

	// Implicit TLS (typically port 465).
	if !s.startTLS && s.port == 465 {
		return sendTLS(addr, s.host, auth, from, to, msg)
	}

	// Plain or STARTTLS (typically 587).
	if s.startTLS {
		return sendStartTLS(addr, s.host, auth, from, to, msg)
	}
	return smtp.SendMail(addr, auth, from, to, msg)
}

func sendTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("tls dial %s: %w", addr, err)
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	return smtpClientSend(c, auth, from, to, msg)
}

func sendStartTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	return smtpClientSend(c, auth, from, to, msg)
}

func smtpClientSend(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

type directSender struct{}

func (s *directSender) Send(from string, to []string, msg []byte) error {
	// Group recipients by domain so one MX lookup covers many RCPT.
	byDomain := map[string][]string{}
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		i := strings.LastIndex(addr, "@")
		if i < 0 || i+1 >= len(addr) {
			return fmt.Errorf("invalid recipient: %s", addr)
		}
		dom := strings.ToLower(addr[i+1:])
		byDomain[dom] = append(byDomain[dom], addr)
	}

	var errs []string
	for domain, rcpts := range byDomain {
		if err := deliverToDomain(from, domain, rcpts, msg); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", domain, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("direct delivery failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func deliverToDomain(from, domain string, rcpts []string, msg []byte) error {
	hosts, err := mxHosts(domain)
	if err != nil {
		return err
	}
	var last error
	for _, host := range hosts {
		addr := net.JoinHostPort(host, "25")
		conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
		if err != nil {
			last = err
			continue
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			last = err
			continue
		}
		// Best-effort STARTTLS if offered.
		if ok, _ := c.Extension("STARTTLS"); ok {
			_ = c.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: false})
		}
		if err := smtpClientSend(c, nil, from, rcpts, msg); err != nil {
			last = err
			_ = c.Close()
			continue
		}
		return nil
	}
	if last == nil {
		last = fmt.Errorf("no MX hosts for %s", domain)
	}
	return last
}

func mxHosts(domain string) ([]string, error) {
	mxs, err := net.LookupMX(domain)
	if err == nil && len(mxs) > 0 {
		sort.Slice(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
		out := make([]string, 0, len(mxs))
		for _, mx := range mxs {
			h := strings.TrimSuffix(mx.Host, ".")
			if h != "" {
				out = append(out, h)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	// Fallback A/AAAA via bare domain (RFC 5321 implicit MX).
	return []string{domain}, nil
}
```

- [ ] **Step 2: Compile package**

Run: `go test ./sender/ -count=1`
Expected: PASS (existing message tests still pass; no network tests).

- [ ] **Step 3: Commit**

```bash
git add sender/sender.go
git commit -m "feat(sender): add relay and direct MX delivery"
```

---

### Task 4: SendHandler + request validation tests

**Files:**
- Create: `handlers/send.go`
- Create: `handlers/send_test.go`

**Interfaces:**
- Consumes: `sender.Sender`, `sender.BuildMessage`
- Produces: `handlers.SendHandler{Sender, Domains, DefaultFrom}`, method `Send(c *gin.Context)`

- [ ] **Step 1: Write failing handler tests**

Create `handlers/send_test.go`. Use a fake sender and gin test mode:

```go
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakeSender struct {
	lastFrom string
	lastTo   []string
	lastMsg  []byte
	err      error
}

func (f *fakeSender) Send(from string, to []string, msg []byte) error {
	f.lastFrom = from
	f.lastTo = append([]string(nil), to...)
	f.lastMsg = append([]byte(nil), msg...)
	return f.err
}

func setupSendRouter(h *SendHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/send", h.Send)
	return r
}

func TestSend_success_stringTo(t *testing.T) {
	fs := &fakeSender{}
	h := &SendHandler{Sender: fs, Domains: []string{"example.com"}, DefaultFrom: ""}
	r := setupSendRouter(h)

	body := `{"from":"a@example.com","to":"b@x.com","subject":"hi","text":"hello"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if fs.lastFrom != "a@example.com" {
		t.Fatalf("from=%q", fs.lastFrom)
	}
	if len(fs.lastTo) != 1 || fs.lastTo[0] != "b@x.com" {
		t.Fatalf("to=%v", fs.lastTo)
	}
}

func TestSend_success_arrayTo(t *testing.T) {
	fs := &fakeSender{}
	h := &SendHandler{Sender: fs, Domains: []string{"example.com"}}
	r := setupSendRouter(h)

	body := `{"from":"a@example.com","to":["b@x.com","c@y.com"],"subject":"hi","html":"<p>x</p>"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(fs.lastTo) != 2 {
		t.Fatalf("to=%v", fs.lastTo)
	}
}

func TestSend_rejectsForeignFrom(t *testing.T) {
	h := &SendHandler{Sender: &fakeSender{}, Domains: []string{"example.com"}}
	r := setupSendRouter(h)

	body := `{"from":"a@evil.com","to":"b@x.com","text":"x"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSend_defaultFrom(t *testing.T) {
	fs := &fakeSender{}
	h := &SendHandler{Sender: fs, Domains: []string{"example.com"}, DefaultFrom: "noreply@example.com"}
	r := setupSendRouter(h)

	body := `{"to":"b@x.com","text":"x"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if fs.lastFrom != "noreply@example.com" {
		t.Fatalf("from=%q", fs.lastFrom)
	}
}

func TestSend_emptyBody(t *testing.T) {
	h := &SendHandler{Sender: &fakeSender{}, Domains: []string{"example.com"}}
	r := setupSendRouter(h)

	body := `{"from":"a@example.com","to":"b@x.com"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSend_deliveryFailure(t *testing.T) {
	fs := &fakeSender{err: bytes.ErrTooLarge}
	h := &SendHandler{Sender: fs, Domains: []string{"example.com"}}
	r := setupSendRouter(h)

	body := `{"from":"a@example.com","to":"b@x.com","text":"x"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Fatalf("expected error field: %s", w.Body.String())
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `go test ./handlers/ -run Send -v`
Expected: FAIL — `SendHandler` undefined.

- [ ] **Step 3: Implement `handlers/send.go`**

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"tempmail/sender"
)

// SendHandler handles outbound mail.
type SendHandler struct {
	Sender      sender.Sender
	Domains     []string
	DefaultFrom string
}

// recipients accepts either a JSON string or array of strings.
type recipients []string

func (r *recipients) UnmarshalJSON(data []byte) error {
	// try array first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*r = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*r = nil
		return nil
	}
	*r = []string{s}
	return nil
}

// SendRequest is the body for POST /api/send.
type SendRequest struct {
	From    string     `json:"from"`
	To      recipients `json:"to"`
	Subject string     `json:"subject"`
	Text    string     `json:"text"`
	HTML    string     `json:"html"`
}

// Send delivers an outbound email.
// POST /api/send
func (h *SendHandler) Send(c *gin.Context) {
	var req SendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	from := strings.TrimSpace(req.From)
	if from == "" {
		from = strings.TrimSpace(h.DefaultFrom)
	}
	if from == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from is required"})
		return
	}
	if !domainAllowed(from, h.Domains) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from domain is not allowed"})
		return
	}

	var to []string
	for _, addr := range req.To {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr != "" {
			to = append(to, addr)
		}
	}
	if len(to) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "to is required"})
		return
	}
	if strings.TrimSpace(req.Text) == "" && strings.TrimSpace(req.HTML) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text or html body is required"})
		return
	}

	msg, err := sender.BuildMessage(from, to, req.Subject, req.Text, req.HTML)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.Sender.Send(from, to, msg); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "rejected": to})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "accepted": to})
}

func domainAllowed(addr string, domains []string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	// strip display name if present: "Name" <a@b.com>
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.Index(addr[i:], ">"); j > 0 {
			addr = addr[i+1 : i+j]
		}
	}
	at := strings.LastIndex(addr, "@")
	if at < 0 || at+1 >= len(addr) {
		return false
	}
	dom := addr[at+1:]
	for _, d := range domains {
		if dom == d {
			return true
		}
	}
	return false
}
```

Note: simplified partial-failure map from the design — when `Send` returns error, respond `502` with all `to` listed under `rejected` (stdlib `smtp` does not give per-rcpt results easily for relay path). This still matches the design's all-fail case.

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./handlers/ -run Send -count=1 -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add handlers/send.go handlers/send_test.go
git commit -m "feat(handlers): add POST /api/send endpoint"
```

---

### Task 5: Message-age cleanup + test

**Files:**
- Modify: `handlers/email.go`
- Create: `handlers/email_cleanup_test.go`

**Interfaces:**
- Produces: `func (h *EmailHandler) CleanupOldMessages(maxAge time.Duration) (int64, error)`

- [ ] **Step 1: Write failing test**

Create `handlers/email_cleanup_test.go`:

```go
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
```

- [ ] **Step 2: Run test — expect FAIL**

Run: `go test ./handlers/ -run CleanupOldMessages -v`
Expected: FAIL — method undefined.

- [ ] **Step 3: Implement method on `EmailHandler`**

Append to `handlers/email.go`:

```go
// CleanupOldMessages deletes messages whose received_at is older than maxAge.
func (h *EmailHandler) CleanupOldMessages(maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-maxAge)
	res := h.DB.Where("received_at < ?", cutoff).Delete(&models.Message{})
	if res.Error != nil {
		return 0, fmt.Errorf("cleanup messages: %w", res.Error)
	}
	return res.RowsAffected, nil
}
```

(`fmt` and `time` and `models` are already imported in this file.)

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./handlers/ -count=1`
Expected: PASS (all handler tests including Send and cleanup).

- [ ] **Step 5: Commit**

```bash
git add handlers/email.go handlers/email_cleanup_test.go
git commit -m "feat(handlers): cleanup messages older than MESSAGE_TTL_HOURS"
```

---

### Task 6: Wire main.go — route + cleanup + sender

**Files:**
- Modify: `main.go`

**Interfaces:**
- Consumes: `config.SMTPSend`, `config.MessageTTLHours`, `sender.NewSender`, `handlers.SendHandler`, `CleanupOldMessages`
- Changes: `runCleanup(h, everyMin, msgTTLHours int, stop)`

- [ ] **Step 1: Update imports and handlers wiring**

Add import:

```go
	"tempmail/sender"
```

After `webhookH := ...` (around line 40), add:

```go
	sendH := &handlers.SendHandler{
		Sender: sender.NewSender(
			cfg.SMTPSend.Host,
			cfg.SMTPSend.Port,
			cfg.SMTPSend.User,
			cfg.SMTPSend.Pass,
			cfg.SMTPSend.StartTLS,
		),
		Domains:     cfg.Domains,
		DefaultFrom: cfg.SMTPSend.From,
	}
```

- [ ] **Step 2: Register route**

Inside the `mgmt` group, after existing routes, add:

```go
			mgmt.POST("/send", sendH.Send)
```

- [ ] **Step 3: Pass message TTL into cleanup**

Change the call:

```go
	go runCleanup(emailH, cfg.CleanupIntervalMin, cfg.MessageTTLHours, stop)
```

Replace `runCleanup` with:

```go
func runCleanup(h *handlers.EmailHandler, everyMin, msgTTLHours int, stop <-chan struct{}) {
	if everyMin <= 0 {
		everyMin = 30
	}
	ticker := time.NewTicker(time.Duration(everyMin) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if n, err := h.CleanupExpired(); err != nil {
				log.Printf("cleanup error: %v", err)
			} else if n > 0 {
				log.Printf("cleanup: removed %d expired mailboxes", n)
			}
			if msgTTLHours > 0 {
				if n, err := h.CleanupOldMessages(time.Duration(msgTTLHours) * time.Hour); err != nil {
					log.Printf("cleanup messages error: %v", err)
				} else if n > 0 {
					log.Printf("cleanup: removed %d old messages", n)
				}
			}
		case <-stop:
			return
		}
	}
}
```

Optional log on startup (after SMTP log is fine):

```go
	if cfg.SMTPSend.Host != "" {
		log.Printf("outbound mail: relay %s:%d starttls=%v", cfg.SMTPSend.Host, cfg.SMTPSend.Port, cfg.SMTPSend.StartTLS)
	} else {
		log.Printf("outbound mail: direct MX delivery")
	}
	if cfg.MessageTTLHours > 0 {
		log.Printf("message TTL: %dh", cfg.MessageTTLHours)
	}
```

- [ ] **Step 4: Build and test everything**

```bash
go test ./... -count=1
go vet ./...
go build -o tempmail .
```

Expected: all pass / clean build.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: wire POST /api/send and message-age cleanup"
```

---

### Task 7: Docs touch-up + final verification

**Files:**
- Modify: `CLAUDE.md` (brief mention of send API + MESSAGE_TTL_HOURS) only if needed for accuracy — keep minimal.
- Optional: update README if it lists API endpoints (check first; only edit if an endpoint table already exists).

- [ ] **Step 1: Grep README for API endpoints**

If README has an endpoint list, add one line:

```
POST /api/send — send outbound email (API key)
```

And mention `MESSAGE_TTL_HOURS` / `SMTP_SEND_*` in config section if one exists. Do not rewrite the whole README.

- [ ] **Step 2: Final verification**

```bash
go test ./... -count=1
go vet ./...
go build -o tempmail .
```

Expected: PASS.

Manual smoke (optional, needs running server + real domain):

```bash
curl -sS -X POST http://127.0.0.1:8080/api/send \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"from":"test@YOUR_DOMAIN","to":"you@elsewhere.com","subject":"ping","text":"hi"}'
```

- [ ] **Step 3: Commit docs if changed**

```bash
git add README.md CLAUDE.md  # only if modified
git commit -m "docs: document send API and message TTL"
```

---

## Self-review checklist (done while writing)

1. **Spec coverage**
   - `POST /api/send` JSON + auth → Task 4 + 6  
   - from domain restriction → Task 4  
   - to string|array → Task 4 `recipients`  
   - relay + direct → Task 3  
   - BuildMessage multipart/encoding → Task 2  
   - SMTP_SEND_* / MESSAGE_TTL_HOURS → Task 1  
   - CleanupOldMessages + ticker → Task 5 + 6  
   - no DB for sent, no DKIM → respected  

2. **Placeholders:** none; full code in each step.

3. **Type consistency:**
   - `sender.NewSender(host, port, user, pass, startTLS)` matches main wiring.
   - `SendHandler.Sender sender.Sender` matches interface.
   - `runCleanup(..., msgTTLHours int, ...)` matches call site.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-24-send-api-and-message-cleanup.md`.

**Two execution options:**

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks  
2. **Inline Execution** — execute tasks in this session with checkpoints  

Which approach?
