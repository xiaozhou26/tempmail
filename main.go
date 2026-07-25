package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"tempmail/config"
	graphpoll "tempmail/graph"
	"tempmail/handlers"
	imappoll "tempmail/imap"
	"tempmail/ingest"
	"tempmail/middleware"
	"tempmail/sender"
	smtpServer "tempmail/smtp"
	"tempmail/storage"
)

// version is injected at link time: -ldflags "-X main.version=v1.0.0"
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("starting tempmail %s for domains %v on %s", version, cfg.Domains, cfg.ListenAddr)

	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	emailH := &handlers.EmailHandler{DB: db, Domain: cfg.Domain, Domains: cfg.Domains, TTLHours: cfg.DefaultTTLHours, AllowSubdomains: cfg.AllowSubdomains}
	msgH := &handlers.MessageHandler{DB: db}
	webhookH := &handlers.WebhookHandler{DB: db, Domains: cfg.Domains}

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

	// On-demand ingestion: only fetch from Graph/IMAP when a client asks for mail.
	var onDemand *ingest.OnDemand

	// Background cleanup only. Mail is fetched on demand when clients hit
	// message-related endpoints (see ingest.OnDemand).
	stop := make(chan struct{})
	go runCleanup(emailH, cfg.CleanupIntervalMin, cfg.MessageTTLHours, stop)

	if cfg.SMTPSend.Host != "" {
		log.Printf("outbound mail: relay %s:%d starttls=%v", cfg.SMTPSend.Host, cfg.SMTPSend.Port, cfg.SMTPSend.StartTLS)
	} else {
		log.Printf("outbound mail: direct MX delivery")
	}
	if cfg.MessageTTLHours > 0 {
		log.Printf("message TTL: %dh", cfg.MessageTTLHours)
	}

	// Graph takes priority over IMAP when enabled (matches README / config.Load).
	if cfg.Graph.Enabled {
		gpoller := &graphpoll.Poller{
			DB:           db,
			Domains:      cfg.Domains,
			ClientID:     cfg.Graph.ClientID,
			ClientSecret: cfg.Graph.ClientSecret,
			TenantID:     cfg.Graph.TenantID,
			RefreshToken: cfg.Graph.RefreshToken,
			TokenScope:   cfg.Graph.TokenScope,
			Account:      cfg.Graph.Account,
			MailFolder:   cfg.Graph.MailFolder,
			Interval:     time.Duration(cfg.Graph.PollIntervalSec) * time.Second,
			OnRotated: func(newRefreshToken string) {
				if err := config.PersistRefreshToken(".env", "GRAPH_REFRESH_TOKEN", newRefreshToken); err != nil {
					log.Printf("persist graph refresh token: %v", err)
				} else {
					log.Printf("graph refresh token rotated and saved to .env")
				}
			},
		}
		onDemand = &ingest.OnDemand{
			Fetcher:     gpoller,
			MinInterval: time.Second, // coalesce bursty client polls
		}
		log.Printf("graph on-demand fetch ready: %s (triggered by GET messages)",
			cfg.Graph.Account)
	} else if cfg.IMAP.Host != "" {
		poller := &imappoll.Poller{
			DB:                 db,
			Domains:            cfg.Domains,
			Host:               cfg.IMAP.Host,
			Port:               cfg.IMAP.Port,
			Username:           cfg.IMAP.Username,
			Password:           cfg.IMAP.Password,
			Mailbox:            cfg.IMAP.Mailbox,
			UseTLS:             cfg.IMAP.UseTLS,
			StartTLS:           cfg.IMAP.StartTLS,
			InsecureSkipVerify: cfg.IMAP.InsecureSkipVerify,
			Interval:           time.Duration(cfg.IMAP.PollIntervalSec) * time.Second,

			AuthMode:     cfg.IMAP.AuthMode,
			ClientID:     cfg.IMAP.ClientID,
			TenantID:     cfg.IMAP.TenantID,
			RefreshToken: cfg.IMAP.RefreshToken,
			TokenScope:   cfg.IMAP.TokenScope,
			OnRotated: func(newRefreshToken string) {
				if err := config.PersistRefreshToken(".env", "IMAP_REFRESH_TOKEN", newRefreshToken); err != nil {
					log.Printf("persist refresh token: %v", err)
				} else {
					log.Printf("refresh token rotated and saved to .env")
				}
			},
		}
		onDemand = &ingest.OnDemand{
			Fetcher:     poller,
			MinInterval: time.Second,
		}
		log.Printf("imap on-demand fetch ready: %s@%s:%d tls=%v (triggered by GET messages)",
			cfg.IMAP.Username, cfg.IMAP.Host, cfg.IMAP.Port, cfg.IMAP.UseTLS)
	} else if !cfg.SMTP.Enabled {
		log.Printf("WARNING: no SMTP/Graph/IMAP configured; running in webhook-only mode")
	}
	emailH.Ingest = onDemand
	msgH.Ingest = onDemand

	// Built-in SMTP server: directly receives mail for the configured domain.
	var smtpSrv *smtpServer.Server
	smtpCtx, smtpCancel := context.WithCancel(context.Background())
	if cfg.SMTP.Enabled {
		smtpSrv = &smtpServer.Server{
			Addr:            cfg.SMTP.Addr,
			Hostname:        cfg.SMTP.Hostname,
			Domains:         cfg.Domains,
			DB:              db,
			AllowSubdomains: cfg.AllowSubdomains,
		}
		go func() {
			if err := smtpSrv.Start(smtpCtx); err != nil {
				log.Printf("smtp server stopped: %v", err)
			}
		}()
		log.Printf("smtp server enabled on %s (hostname=%s, accepts mail for %v)",
			cfg.SMTP.Addr, cfg.SMTP.Hostname, cfg.Domains)
	}

	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "version": version})
	})

	api := r.Group("/api")
	// Management endpoints require the API key.
	mgmt := api.Group("", middleware.APIKeyAuth(cfg.APIKey))
	{
		mgmt.POST("/mailboxes", emailH.CreateMailbox)
		mgmt.GET("/mailboxes", emailH.ListMailboxes)
		mgmt.GET("/mailboxes/:address", emailH.GetMailbox)
		mgmt.DELETE("/mailboxes/:address", emailH.DeleteMailbox)
		mgmt.GET("/mailboxes/:address/messages", msgH.ListMessages)
		mgmt.GET("/messages/:id", msgH.GetMessage)
		mgmt.DELETE("/messages/:id", msgH.DeleteMessage)
		mgmt.POST("/send", sendH.Send)
	}
	// Optional webhook ingress (disabled unless WEBHOOK_SECRET is set).
	api.POST("/webhook/email", middleware.WebhookAuth(cfg.WebhookSecret), webhookH.Receive)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	close(stop)
	smtpCancel() // stop the SMTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	if onDemand != nil {
		onDemand.Close()
	}
}

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
