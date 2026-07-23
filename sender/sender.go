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
		// Prefer IPv4 for better rDNS/PTR compatibility on most servers.
		conn, err := net.DialTimeout("tcp4", addr, 30*time.Second)
		if err != nil {
			conn, err = net.DialTimeout("tcp6", addr, 30*time.Second)
		}
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
