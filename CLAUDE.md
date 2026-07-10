# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`tempmail` is a Go 1.25 backend for temporary domain mailboxes. Cloudflare Email Routing forwards catch-all mail to a real relay inbox; this service either polls that inbox over IMAP or accepts an optional authenticated webhook, parses RFC822 messages, stores them in SQLite, and exposes a Gin REST API.

There is no frontend or external database service. SQLite is embedded through GORM and requires CGO/a working C compiler because the driver uses `go-sqlite3`.

## Development commands

Run these from the repository root:

```bash
# Install/update dependencies recorded in go.mod/go.sum
go mod download

# Run all package tests (currently there are no test files, but this also compiles every package)
go test ./...

# Run one test by exact name in one package
go test ./handlers -run '^TestName$' -v

# Re-run without the test cache
go test -count=1 ./...

# Static checks available without an extra linter
go vet ./...

# Format Go files
gofmt -w main.go config/*.go handlers/*.go imap/*.go middleware/*.go models/*.go storage/*.go

# Build the server; use tempmail.exe as the output name on Windows if preferred
go build -o tempmail .

# Prepare local configuration and run directly
cp .env.example .env
go run .
```

`go test ./...` is the current verified baseline. No dedicated lint configuration, Makefile, Docker setup, or test suite is present. On Windows, install a GCC toolchain and ensure CGO is enabled before building or testing SQLite-dependent code.

## Runtime configuration

`config.Load` optionally loads `.env`, then reads environment variables. The required settings are `MAIL_DOMAIN`, `API_KEY`, and either a complete IMAP configuration or `WEBHOOK_SECRET` for webhook-only mode. Defaults include `:8080` for `LISTEN_ADDR`, `./data/tempmail.db` for `DB_PATH`, 24 hours for mailbox TTL, 30 minutes for cleanup, and 60 seconds for IMAP polling. Start from `.env.example`; `.env` and `data/` are intentionally ignored by Git.

## Architecture

The application is assembled in `main.go`:

- `config/` parses and validates environment-based runtime settings.
- `storage/` opens SQLite, creates the database directory, and runs GORM `AutoMigrate` for the models.
- `models/` defines the `Mailbox` and `Message` records. A mailbox owns its messages through a GORM foreign key with cascade deletion.
- `middleware/` protects management routes with either `Authorization: Bearer ...` or `X-API-Key`, and protects the optional webhook with `X-Webhook-Secret`.
- `handlers/email.go` implements mailbox creation, listing, lookup, deletion, and periodic expiry cleanup.
- `handlers/message.go` implements mailbox-scoped message listing and message lookup/deletion. Raw RFC822 content is omitted from normal JSON serialization and included only by the message detail endpoint.
- `handlers/store.go` contains the shared ingestion business logic. It parses RFC822 with `enmime`, resolves a recipient on the configured domain, lazily creates a mailbox when necessary, and persists the parsed message plus the raw source.
- `handlers/webhook.go` validates and accepts pushed raw messages, then delegates to `StoreMessage`.
- `imap/poller.go` connects to the configured IMAP relay inbox, searches for unread messages, fetches their full RFC822 bodies, delegates to `StoreMessage`, and marks fetched messages as `\\Seen` to avoid reprocessing.

The normal mail flow is:

```text
Cloudflare Email Routing -> relay inbox -> imap.Poller -> handlers.StoreMessage -> GORM/SQLite
                                                   ^
Optional authenticated webhook -> handlers.StoreMessage
```

The HTTP server exposes `/healthz` without authentication, management routes under `/api` behind API-key middleware, and `/api/webhook/email` behind webhook-secret middleware. `main.go` also starts the expiry-cleanup ticker, starts the IMAP poller when configured, and performs a five-second graceful HTTP shutdown on SIGINT/SIGTERM.

## Implementation notes

- Keep both ingestion paths using `handlers.StoreMessage`; it is the single place that parses, routes, auto-creates, and persists an inbound message.
- Recipient resolution checks `To`, then `Delivered-To`, `X-Forwarded-To`, and `X-Original-To`, selecting the first address matching `@MAIL_DOMAIN`. Non-domain mail is represented by `handlers.ErrNotForOurDomain` and should not be stored.
- Mailboxes created by inbound mail are given a one-year expiry; explicitly created mailboxes use the configured default or request TTL. Expired mailboxes are removed by the background cleanup job.
- Database access is currently performed directly through `*gorm.DB` in handlers and ingestion code; preserve that style unless introducing a deliberate repository/service boundary.
- Because SQLite is file-backed, local runtime data is created under `data/`; do not commit the database or local `.env`.
- The recommended production shape is the built binary with an environment file behind an HTTPS reverse proxy. IMAP mode is the default Worker-free ingestion path; webhook mode is optional.
