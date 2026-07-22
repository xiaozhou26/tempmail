// Package smtp provides a built-in SMTP server that directly receives mail for
// the configured domain, eliminating the need for a relay inbox and Cloudflare
// Email Routing. Incoming messages are parsed and stored via handlers.StoreMessage.
package smtp

import (
	"context"
	"io"
	"log"
	"net"
	"strings"
	"time"

	goSmtp "github.com/emersion/go-smtp"
	"gorm.io/gorm"
	"tempmail/handlers"
)

// Server wraps an SMTP server that accepts mail for configured domains.
type Server struct {
	Addr     string   // listen address, e.g. ":25"
	Hostname string   // server hostname for EHLO/HELO
	Domains  []string // email domains to accept mail for
	DB       *gorm.DB

	srv *goSmtp.Server
}

// Start begins listening for SMTP connections. It blocks until the context is
// cancelled or an error occurs.
func (s *Server) Start(ctx context.Context) error {
	domains := make([]string, len(s.Domains))
	for i, d := range s.Domains {
		domains[i] = strings.ToLower(d)
	}
	be := &backend{
		db:      s.DB,
		domains: domains,
	}

	s.srv = goSmtp.NewServer(be)
	s.srv.Addr = s.Addr
	s.srv.Domain = s.Hostname
	s.srv.AllowInsecureAuth = true // incoming mail server: no auth required
	s.srv.MaxMessageBytes = 10 * 1024 * 1024
	s.srv.MaxRecipients = 50
	s.srv.ReadTimeout = 60 * time.Second
	s.srv.WriteTimeout = 60 * time.Second

	// Shutdown gracefully when the context is cancelled.
	go func() {
		<-ctx.Done()
		s.srv.Close()
	}()

	log.Printf("smtp server listening on %s (hostname=%s, domains=%v)", s.Addr, s.Hostname, s.Domains)
	if err := s.srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// backend implements go-smtp.Backend.
type backend struct {
	db      *gorm.DB
	domains []string
}

func (b *backend) NewSession(_ *goSmtp.Conn) (goSmtp.Session, error) {
	return &session{db: b.db, domains: b.domains}, nil
}

// session implements go-smtp.Session.
type session struct {
	db      *gorm.DB
	domains []string
	from    string
}

func (s *session) Mail(from string, _ *goSmtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, _ *goSmtp.RcptOptions) error {
	// Only accept recipients on our domains.
	addr := strings.ToLower(strings.TrimSpace(to))
	for _, d := range s.domains {
		if strings.HasSuffix(addr, "@"+d) {
			return nil
		}
	}
	return &goSmtp.SMTPError{
		Code:    550,
		Message: "5.1.1 relaying denied",
	}
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	msg, err := handlers.StoreMessage(s.db, s.domains, string(raw))
	if err != nil {
		if err == handlers.ErrNotForOurDomain {
			// Silently accept — we already validated in Rcpt().
			return nil
		}
		log.Printf("smtp: store message from %s: %v", s.from, err)
		return &goSmtp.SMTPError{
			Code:    450,
			Message: "temporary failure",
		}
	}
	if msg != nil {
		log.Printf("smtp: stored message #%d for %s (from %s, subject: %q)",
			msg.ID, msg.To, msg.From, msg.Subject)
	}
	return nil
}

func (s *session) Reset() {}

func (s *session) Logout() error { return nil }

// addrPort extracts the host from an address like ":25" for logging.
func addrPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return ":" + port
}
