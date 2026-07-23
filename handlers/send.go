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
