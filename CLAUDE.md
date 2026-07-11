# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`tempmail` is a Go 1.26 backend for temporary domain mailboxes. Cloudflare Email Routing forwards catch-all mail to a real relay inbox; this service either polls that inbox over Microsoft Graph / IMAP or accepts an optional authenticated webhook, parses messages, stores them in SQLite, and exposes a Gin REST API.

There is no frontend or external database service. SQLite is embedded through GORM with the pure-Go driver `glebarez/sqlite` (modernc.org/sqlite), so **CGO is not required**.

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
gofmt -w main.go config/*.go handlers/*.go imap/*.go graph/*.go middleware/*.go models/*.go storage/*.go

# Build the server; use tempmail.exe as the output name on Windows if preferred
go build -o tempmail .

# Cross-compile static Linux binary (and other platforms via PLATFORMS=...)
bash build.sh
VERSION=v1.0.0 PLATFORMS="linux/amd64 linux/arm64" bash build.sh

# Local Docker image
docker build -t tempmail:local --build-arg VERSION=dev .

# Prepare local configuration and run directly
cp .env.example .env
go run .
```

`go test ./...` is the current verified baseline. Release automation lives in `.github/workflows/release.yml` (tag `v*` → multi-platform binaries + GHCR multi-arch image + GitHub Release).

## Runtime configuration

`config.Load` optionally loads `.env`, then reads environment variables. The required settings are `MAIL_DOMAIN`, `API_KEY`, and either a complete Graph/IMAP configuration or `WEBHOOK_SECRET` for webhook-only mode. Defaults include `:8080` for `LISTEN_ADDR`, `./data/tempmail.db` for `DB_PATH` (Docker image defaults to `/data/tempmail.db`), 24 hours for mailbox TTL, 30 minutes for cleanup, and 60 seconds for polling. Start from `.env.example`; `.env` and `data/` are intentionally ignored by Git.

## Architecture

The application is assembled in `main.go`:

- `config/` parses and validates environment-based runtime settings.
- `storage/` opens SQLite, creates the database directory, and runs GORM `AutoMigrate` for the models.
- `models/` defines the `Mailbox` and `Message` records. A mailbox owns its messages through a GORM foreign key with cascade deletion.
- `middleware/` protects management routes with either `Authorization: Bearer ...` or `X-API-Key`, and protects the optional webhook with `X-Webhook-Secret`.
- `handlers/email.go` implements mailbox creation, listing, lookup, deletion, and periodic expiry cleanup.
- `handlers/message.go` implements mailbox-scoped message listing and message lookup/deletion. Raw RFC822 content is omitted from normal JSON serialization and included only by the message detail endpoint.
- `handlers/store.go` contains the shared ingestion business logic for RFC822 (IMAP/webhook). It parses with `enmime`, resolves a recipient on the configured domain, lazily creates a mailbox when necessary, and persists the parsed message plus the raw source.
- `handlers/graph_store.go` stores Graph JSON messages into the same models.
- `handlers/webhook.go` validates and accepts pushed raw messages, then delegates to `StoreMessage`.
- `imap/poller.go` / `graph/poller.go` poll the relay inbox and mark processed messages to avoid reprocessing.
- `main.version` is injected at link time (`-X main.version=...`) and exposed via `/healthz`.

The normal mail flow is:

```text
Cloudflare Email Routing -> relay inbox -> graph/imap Poller -> handlers store -> GORM/SQLite
                                                   ^
Optional authenticated webhook -> handlers.StoreMessage
```

The HTTP server exposes `/healthz` without authentication, management routes under `/api` behind API-key middleware, and `/api/webhook/email` behind webhook-secret middleware. `main.go` also starts the expiry-cleanup ticker, starts Graph or IMAP poller when configured, and performs a five-second graceful HTTP shutdown on SIGINT/SIGTERM.

## Implementation notes

- Keep both RFC822 ingestion paths using `handlers.StoreMessage`; it is the single place that parses, routes, auto-creates, and persists an inbound message. Graph path uses `handlers` Graph store helpers.
- Recipient resolution checks `To`, then `Delivered-To`, `X-Forwarded-To`, and `X-Original-To`, selecting the first address matching `@MAIL_DOMAIN`. Non-domain mail is represented by `handlers.ErrNotForOurDomain` and should not be stored.
- Mailboxes created by inbound mail are given a one-year expiry; explicitly created mailboxes use the configured default or request TTL. Expired mailboxes are removed by the background cleanup job.
- Database access is currently performed directly through `*gorm.DB` in handlers and ingestion code; preserve that style unless introducing a deliberate repository/service boundary.
- Because SQLite is file-backed, local runtime data is created under `data/`; do not commit the database or local `.env`.
- Docker image runs as non-root user `tempmail`, persists DB under `/data`, and expects secrets via env / `--env-file`.
- The recommended production shape is the built binary or container behind an HTTPS reverse proxy. Graph mode is preferred for personal Outlook; IMAP for Gmail; webhook mode is optional.
