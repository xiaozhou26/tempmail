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

// Config holds all runtime configuration loaded from the environment.
type Config struct {
	// Domain used to build temporary email addresses, e.g. "mail.example.com".
	Domain string
	// HTTP listen address.
	ListenAddr string
	// Path to the SQLite database file.
	DBPath string
	// API key used to authenticate management API requests.
	APIKey string
	// Shared secret used to verify optional webhook calls. Leave empty to
	// disable the webhook endpoint entirely (the IMAP poller is the default).
	WebhookSecret string
	// IMAP settings for the Worker-free ingestion path.
	IMAP IMAPConfig
	// Graph settings: when Enabled, the Graph poller replaces the IMAP poller.
	Graph GraphConfig
	// Default lifetime (in hours) for a newly created temporary mailbox.
	DefaultTTLHours int
	// How often expired mailboxes get purged (in minutes).
	CleanupIntervalMin int
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

	cfg := &Config{
		Domain:             strings.ToLower(strings.TrimPrefix(strings.TrimSpace(get("MAIL_DOMAIN", "example.com")), "@")),
		ListenAddr:         get("LISTEN_ADDR", ":8080"),
		DBPath:             get("DB_PATH", "./data/tempmail.db"),
		APIKey:             get("API_KEY", ""),
		WebhookSecret:      get("WEBHOOK_SECRET", ""),
		DefaultTTLHours:    24,
		CleanupIntervalMin: 30,
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

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API_KEY is required (set it in .env or environment)")
	}
	if cfg.Domain == "" || cfg.Domain == "example.com" {
		return nil, fmt.Errorf("MAIL_DOMAIN must be set to your real domain")
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

	// IMAP is required for the Worker-free path. If neither IMAP nor a webhook
	// secret is configured there is no way to receive mail.
	if cfg.IMAP.Host == "" {
		if cfg.WebhookSecret == "" {
			return nil, fmt.Errorf("IMAP_HOST is required (or set IMAP_PROVIDER=firstmail / WEBHOOK_SECRET)")
		}
		// Webhook-only mode: allowed but warned in main.go.
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
