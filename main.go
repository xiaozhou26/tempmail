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
	"tempmail/handlers"
	imappoll "tempmail/imap"
	graphpoll "tempmail/graph"
	"tempmail/middleware"
	"tempmail/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("starting tempmail for domain %q on %s", cfg.Domain, cfg.ListenAddr)

	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	emailH := &handlers.EmailHandler{DB: db, Domain: cfg.Domain, TTLHours: cfg.DefaultTTLHours}
	msgH := &handlers.MessageHandler{DB: db}
	webhookH := &handlers.WebhookHandler{DB: db, Domain: cfg.Domain}

	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

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
	}
	// Optional webhook ingress (disabled unless WEBHOOK_SECRET is set).
	api.POST("/webhook/email", middleware.WebhookAuth(cfg.WebhookSecret), webhookH.Receive)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Background tasks: expired-mailbox cleanup + IMAP polling.
	stop := make(chan struct{})
	go runCleanup(emailH, cfg.CleanupIntervalMin, stop)
	if cfg.IMAP.Host != "" {
		poller := &imappoll.Poller{
			DB:       db,
			Domain:   cfg.Domain,
			Host:     cfg.IMAP.Host,
			Port:     cfg.IMAP.Port,
			Username: cfg.IMAP.Username,
			Password: cfg.IMAP.Password,
			Mailbox:  cfg.IMAP.Mailbox,
			UseTLS:   cfg.IMAP.UseTLS,
			Interval: time.Duration(cfg.IMAP.PollIntervalSec) * time.Second,

			AuthMode:     cfg.IMAP.AuthMode,
			ClientID:     cfg.IMAP.ClientID,
			TenantID:     cfg.IMAP.TenantID,
			RefreshToken: cfg.IMAP.RefreshToken,
			TokenScope:   cfg.IMAP.TokenScope,
			OnRotated: func(newRefreshToken string) {
				// MSA rotates the refresh token on every exchange; persist it
				// back to .env so the next restart doesn't hit invalid_grant.
				if err := config.PersistRefreshToken(".env", "IMAP_REFRESH_TOKEN", newRefreshToken); err != nil {
					log.Printf("persist refresh token: %v", err)
				} else {
					log.Printf("refresh token rotated and saved to .env")
				}
			},
		}
		go poller.Run(stop)
		log.Printf("imap poller started: %s@%s:%d every %ds",
			cfg.IMAP.Username, cfg.IMAP.Host, cfg.IMAP.Port, cfg.IMAP.PollIntervalSec)
	} else if cfg.Graph.Enabled {
		gpoller := &graphpoll.Poller{
			DB:           db,
			Domain:       cfg.Domain,
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
		go gpoller.Run(stop)
		log.Printf("graph poller started: %s every %ds",
			cfg.Graph.Account, cfg.Graph.PollIntervalSec)
	} else {
		log.Printf("WARNING: IMAP not configured; running in webhook-only mode")
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func runCleanup(h *handlers.EmailHandler, everyMin int, stop <-chan struct{}) {
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
		case <-stop:
			return
		}
	}
}
