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
