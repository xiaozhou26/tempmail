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
