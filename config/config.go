package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// IMAPConfig holds connection details for the catch-all forwarding inbox that
// Cloudflare Email Routing forwards *@yourdomain mail into.
type IMAPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	Mailbox  string // typically "INBOX"
	UseTLS   bool   // true = implicit TLS (port 993 DialTLS)
	// StartTLS upgrades a plain connection with STARTTLS (port 143). Ignored when UseTLS is true.
	StartTLS bool
	// InsecureSkipVerify disables TLS certificate verification (not recommended).
	InsecureSkipVerify bool
	// How often continuous polling would check for new mail (on-demand uses MinInterval).
	PollIntervalSec int

	// Outlook OAuth2 / XOAUTH2 fields.
	AuthMode     string // plain | oauth2
	ClientID     string
	TenantID     string
	RefreshToken string
	TokenScope   string // OAuth2 scope for refresh; defaults to https://outlook.office365.com/.default offline_access
}

// GraphConfig uses the Microsoft Graph API to read mail from the relay inbox
// instead of IMAP. More reliable than IMAP OAuth2 for personal Outlook accounts.
type GraphConfig struct {
	Enabled         bool
	ClientID        string
	ClientSecret    string
	TenantID        string // common | consumers | <tenant-id>
	RefreshToken    string
	TokenScope      string // defaults to https://graph.microsoft.com/.default
	Account         string // relay inbox address, used as the XOAUTH2-style "user" / mailbox owner
	MailFolder      string // folder to read; empty = default inbox
	PollIntervalSec int
}

// SMTPConfig holds settings for the built-in SMTP server that directly receives
// mail for the configured domain, removing the need for a relay inbox.
type SMTPConfig struct {
	Enabled  bool   // true = start built-in SMTP server
	Addr     string // listen address, e.g. ":25"
	Hostname string // server hostname used in EHLO/HELO greeting
}

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

// Config holds all runtime configuration loaded from the environment.
type Config struct {
	// Primary domain (first in the list).
	Domain string
	// All configured domains. MAIL_DOMAIN accepts comma-separated values.
	Domains []string
	// HTTP listen address.
	ListenAddr string
	// Path to the SQLite database file.
	DBPath string
	// API key used to authenticate management API requests.
	APIKey string
	// Shared secret used to verify optional webhook calls. Leave empty to
	// disable the webhook endpoint entirely (the IMAP poller is the default).
	WebhookSecret string
	// Built-in SMTP server for direct mail reception.
	SMTP SMTPConfig
	// Outbound SMTP (optional relay). Empty Host => direct MX delivery.
	SMTPSend SMTPSendConfig
	// IMAP settings for the Worker-free ingestion path.
	IMAP IMAPConfig
	// Graph settings: when Enabled, the Graph poller replaces the IMAP poller.
	Graph GraphConfig
	// Default lifetime (in hours) for a newly created temporary mailbox.
	DefaultTTLHours int
	// How often expired mailboxes get purged (in minutes).
	CleanupIntervalMin int
	// Max age of stored inbound messages in hours; 0 disables age cleanup.
	MessageTTLHours int
	// AllowSubdomains enables creating mailboxes under random subdomains,
	// e.g. user@abc123.muskqq.com. When enabled, the SMTP server accepts
	// *@*.domain and the CreateMailbox API supports a "subdomain" field.
	AllowSubdomains bool
}

// knownIMAPProviders fills host/port/tls defaults for common providers when
// IMAP_PROVIDER is set. Firstmail: imap.firstmail.ltd:993 SSL + plain LOGIN.
var knownIMAPProviders = map[string]struct {
	Host     string
	Port     int
	UseTLS   bool
	StartTLS bool
}{
	"firstmail": {Host: "imap.firstmail.ltd", Port: 993, UseTLS: true},
	"gmail":     {Host: "imap.gmail.com", Port: 993, UseTLS: true},
	"outlook":   {Host: "outlook.office365.com", Port: 993, UseTLS: true},
}

// Load reads .env (if present) and environment variables, applying sensible defaults.
func Load() (*Config, error) {
	// .env is optional; ignore error when the file is missing.
	_ = godotenv.Overload()

	get := func(key, def string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return def
	}
	getInt := func(key string, def int) (int, error) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("invalid integer for %s: %w", key, err)
			}
			return n, nil
		}
		return def, nil
	}
	getBool := func(key string, def bool) bool {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		switch v {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		return def
	}

	// Parse multiple domains from MAIL_DOMAIN (comma-separated).
	domainRaw := get("MAIL_DOMAIN", "example.com")
	var domains []string
	for _, d := range strings.Split(domainRaw, ",") {
		d := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d != "" && d != "example.com" {
			domains = append(domains, d)
		}
	}

	cfg := &Config{
		Domain:             "", // set below
		Domains:            domains,
		ListenAddr:         get("LISTEN_ADDR", ":8080"),
		DBPath:             get("DB_PATH", "./data/tempmail.db"),
		APIKey:             get("API_KEY", ""),
		WebhookSecret:      get("WEBHOOK_SECRET", ""),
		DefaultTTLHours:    24,
		CleanupIntervalMin: 30,
		MessageTTLHours:    24,
		AllowSubdomains:    getBool("SUBDOMAIN_ENABLED", false),
		SMTP: SMTPConfig{
			Enabled:  getBool("SMTP_ENABLED", false),
			Addr:     get("SMTP_ADDR", ":25"),
			Hostname: get("SMTP_HOSTNAME", ""),
		},
		SMTPSend: SMTPSendConfig{
			Host:     get("SMTP_SEND_HOST", ""),
			Port:     587,
			User:     get("SMTP_SEND_USER", ""),
			Pass:     get("SMTP_SEND_PASS", ""),
			StartTLS: getBool("SMTP_SEND_STARTTLS", true),
			From:     get("SMTP_SEND_FROM", ""),
		},
		IMAP: IMAPConfig{
			Host:               get("IMAP_HOST", ""),
			Port:               993,
			Username:           get("IMAP_USER", ""),
			Password:           get("IMAP_PASS", ""),
			Mailbox:            get("IMAP_MAILBOX", "INBOX"),
			UseTLS:             getBool("IMAP_TLS", true),
			StartTLS:           getBool("IMAP_STARTTLS", false),
			InsecureSkipVerify: getBool("IMAP_TLS_INSECURE", false),
			PollIntervalSec:    1,
			AuthMode:           strings.ToLower(get("IMAP_AUTH_MODE", "plain")),
			ClientID:           get("IMAP_CLIENT_ID", ""),
			TenantID:           get("IMAP_TENANT_ID", "consumers"),
			RefreshToken:       get("IMAP_REFRESH_TOKEN", ""),
			TokenScope:         get("IMAP_TOKEN_SCOPE", ""),
		},
		Graph: GraphConfig{
			Enabled:         get("GRAPH_ENABLED", "") == "true" || getBool("GRAPH_ENABLED", false),
			ClientID:        get("GRAPH_CLIENT_ID", ""),
			ClientSecret:    get("GRAPH_CLIENT_SECRET", ""),
			TenantID:        get("GRAPH_TENANT_ID", "common"),
			RefreshToken:    get("GRAPH_REFRESH_TOKEN", ""),
			TokenScope:      get("GRAPH_TOKEN_SCOPE", ""),
			Account:         get("GRAPH_ACCOUNT", ""),
			MailFolder:      get("GRAPH_MAIL_FOLDER", ""),
			PollIntervalSec: 1,
		},
	}

	// Apply IMAP_PROVIDER presets (firstmail / gmail / outlook) when fields are empty.
	if prov := strings.ToLower(get("IMAP_PROVIDER", "")); prov != "" {
		if preset, ok := knownIMAPProviders[prov]; ok {
			if cfg.IMAP.Host == "" {
				cfg.IMAP.Host = preset.Host
			}
			if strings.TrimSpace(os.Getenv("IMAP_PORT")) == "" {
				cfg.IMAP.Port = preset.Port
			}
			if strings.TrimSpace(os.Getenv("IMAP_TLS")) == "" {
				cfg.IMAP.UseTLS = preset.UseTLS
			}
			if strings.TrimSpace(os.Getenv("IMAP_STARTTLS")) == "" {
				cfg.IMAP.StartTLS = preset.StartTLS
			}
			if cfg.IMAP.AuthMode == "" {
				cfg.IMAP.AuthMode = "plain"
			}
		} else {
			return nil, fmt.Errorf("unknown IMAP_PROVIDER %q (supported: firstmail, gmail, outlook)", prov)
		}
	}

	if n, err := getInt("DEFAULT_TTL_HOURS", cfg.DefaultTTLHours); err == nil {
		cfg.DefaultTTLHours = n
	} else {
		return nil, err
	}
	if n, err := getInt("CLEANUP_INTERVAL_MIN", cfg.CleanupIntervalMin); err == nil {
		cfg.CleanupIntervalMin = n
	} else {
		return nil, err
	}
	if n, err := getInt("IMAP_PORT", cfg.IMAP.Port); err == nil {
		cfg.IMAP.Port = n
	} else {
		return nil, err
	}
	if n, err := getInt("IMAP_POLL_INTERVAL_SEC", cfg.IMAP.PollIntervalSec); err == nil {
		cfg.IMAP.PollIntervalSec = n
	} else {
		return nil, err
	}
	if n, err := getInt("GRAPH_POLL_INTERVAL_SEC", cfg.Graph.PollIntervalSec); err == nil {
		cfg.Graph.PollIntervalSec = n
	} else {
		return nil, err
	}
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

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API_KEY is required (set it in .env or environment)")
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("MAIL_DOMAIN must be set to your real domain(s)")
	}
	cfg.Domain = domains[0] // primary domain

	// SMTP mode: the built-in SMTP server receives mail directly. IMAP/Graph
	// are optional (for legacy relay setups or dual-mode operation).
	if cfg.SMTP.Enabled {
		if cfg.SMTP.Hostname == "" {
			cfg.SMTP.Hostname = cfg.Domain
		}
		// SMTP-only: skip IMAP/Graph validation entirely.
		if !cfg.Graph.Enabled && cfg.IMAP.Host == "" {
			return cfg, nil
		}
	}

	// Graph mode: validate Graph credentials and skip the IMAP requirement.
	if cfg.Graph.Enabled {
		if cfg.Graph.ClientID == "" || cfg.Graph.RefreshToken == "" {
			return nil, fmt.Errorf("GRAPH_CLIENT_ID and GRAPH_REFRESH_TOKEN are required when GRAPH_ENABLED=true")
		}
		if cfg.Graph.Account == "" {
			return nil, fmt.Errorf("GRAPH_ACCOUNT is required (the relay inbox address)")
		}
		return cfg, nil
	}

	// IMAP is required only when neither SMTP nor Graph nor webhook is configured.
	if cfg.IMAP.Host == "" {
		if cfg.WebhookSecret == "" && !cfg.SMTP.Enabled {
			return nil, fmt.Errorf("IMAP_HOST is required (or set IMAP_PROVIDER=firstmail / SMTP_ENABLED=true / WEBHOOK_SECRET)")
		}
		// SMTP-only or webhook-only mode: allowed.
	} else {
		if cfg.IMAP.AuthMode == "oauth2" {
			if cfg.IMAP.ClientID == "" || cfg.IMAP.RefreshToken == "" {
				return nil, fmt.Errorf("IMAP_CLIENT_ID and IMAP_REFRESH_TOKEN are required when IMAP_AUTH_MODE=oauth2")
			}
		} else {
			if cfg.IMAP.Username == "" || cfg.IMAP.Password == "" {
				return nil, fmt.Errorf("IMAP_USER and IMAP_PASS are required when IMAP_HOST is set (plain LOGIN, e.g. Firstmail)")
			}
		}
	}
	return cfg, nil
}

// PersistRefreshToken rewrites the IMAP_REFRESH_TOKEN line in the .env file
// with the rotated MSA refresh token. MSA refresh tokens are single-use: each
// exchange returns a new one and invalidates the old, so without persisting it
// the next process start fails with invalid_grant. Safe to call repeatedly.
// PersistRefreshToken rewrites a refresh-token line in the .env file with a
// rotated value. Microsoft refresh tokens rotate on every exchange (the old
// one is invalidated), so without persisting the new one the next process start
// fails with invalid_grant. keyEnv is the env var name, e.g.
// "IMAP_REFRESH_TOKEN" or "GRAPH_REFRESH_TOKEN". Safe to call repeatedly.
func PersistRefreshToken(path, keyEnv, newToken string) error {
	prefix := keyEnv + "="
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = prefix + newToken
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, prefix+newToken)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
