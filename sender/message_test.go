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
