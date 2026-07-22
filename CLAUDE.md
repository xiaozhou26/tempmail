# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`tempmail` is a Go 1.26 backend for temporary domain mailboxes. It includes a **built-in SMTP server** that directly receives mail for the configured domain, parses messages, stores them in SQLite, and exposes a Gin REST API. No external relay inbox or Cloudflare Email Routing is required.

Optionally, it can also pull mail from an external relay inbox via IMAP or Microsoft Graph (legacy mode), or accept webhook pushes.

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
gofmt -w main.go config/*.go handlers/*.go imap/*.go graph/*.go middleware/*.go models/*.go storage/*.go smtp/*.go

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

`config.Load` optionally loads `.env`, then reads environment variables. The required settings are `MAIL_DOMAIN` and `API_KEY`. At least one ingestion method must be enabled:

- **SMTP（推荐）**：`SMTP_ENABLED=true` — 内置 SMTP 服务器直接收信
- **IMAP**：完整的 IMAP 配置 — 从中转邮箱拉取
- **Graph**：`GRAPH_ENABLED=true` + 完整配置 — 从 Outlook 拉取
- **Webhook**：`WEBHOOK_SECRET` — 外部推送

Defaults include `:8080` for `LISTEN_ADDR`, `:25` for `SMTP_ADDR`, `./data/tempmail.db` for `DB_PATH` (Docker image defaults to `/data/tempmail.db`), 24 hours for mailbox TTL, 30 minutes for cleanup. Start from `.env.example`; `.env` and `data/` are intentionally ignored by Git.

## Architecture

The application is assembled in `main.go`:

- `config/` parses and validates environment-based runtime settings.
- `storage/` opens SQLite, creates the database directory, and runs GORM `AutoMigrate` for the models.
- `models/` defines the `Mailbox` and `Message` records. A mailbox owns its messages through a GORM foreign key with cascade deletion.
- `middleware/` protects management routes with either `Authorization: Bearer ...` or `X-API-Key`, and protects the optional webhook with `X-Webhook-Secret`.
- `smtp/` provides the built-in SMTP server that directly receives mail for the configured domain.
- `handlers/email.go` implements mailbox creation, listing, lookup, deletion, and periodic expiry cleanup.
- `handlers/message.go` implements mailbox-scoped message listing and message lookup/deletion. Raw RFC822 content is omitted from normal JSON serialization and included only by the message detail endpoint.
- `handlers/store.go` contains the shared ingestion business logic for RFC822. It parses with `enmime`, resolves a recipient on the configured domain, lazily creates a mailbox when necessary, and persists the parsed message plus the raw source.
- `handlers/graph_store.go` stores Graph JSON messages into the same models.
- `handlers/webhook.go` validates and accepts pushed raw messages, then delegates to `StoreMessage`.
- `imap/poller.go` / `graph/poller.go` provide FetchOnce for on-demand relay reads (legacy mode); `ingest.OnDemand` is triggered by GET message endpoints.
- `main.version` is injected at link time (`-X main.version=...`) and exposed via `/healthz`.

The normal mail flow (SMTP mode) is:

```text
发件人 ──SMTP──> tempmail SMTP 服务器(:25)
                       │
                       ▼  handlers.StoreMessage
              GORM/SQLite
                       ▲
                       │
              Gin REST API(:8080) ──> 客户端
```

Legacy mail flow (relay mode):

```text
Cloudflare Email Routing -> relay inbox; client GET messages -> ingest.OnDemand -> graph/imap FetchOnce -> handlers store -> GORM/SQLite
                                                   ^
Optional authenticated webhook -> handlers.StoreMessage
```

The HTTP server exposes `/healthz` without authentication, management routes under `/api` behind API-key middleware, and `/api/webhook/email` behind webhook-secret middleware. `main.go` starts the SMTP server (when enabled), the expiry-cleanup ticker, wires Graph or IMAP as an optional on-demand fetcher, and performs a five-second graceful HTTP shutdown on SIGINT/SIGTERM.

## Implementation notes

- Keep both RFC822 ingestion paths using `handlers.StoreMessage`; it is the single place that parses, routes, auto-creates, and persists an inbound message. Graph path uses `handlers` Graph store helpers.
- Recipient resolution checks `To`, then `Delivered-To`, `X-Forwarded-To`, and `X-Original-To`, selecting the first address matching `@MAIL_DOMAIN`. Non-domain mail is represented by `handlers.ErrNotForOurDomain` and should not be stored.
- The SMTP server validates recipients in `Rcpt()` — only `@MAIL_DOMAIN` addresses are accepted. `StoreMessage` is called in `Data()` for the actual parsing/storage.
- Mailboxes created by inbound mail are given a one-year expiry; explicitly created mailboxes use the configured default or request TTL. Expired mailboxes are removed by the background cleanup job.
- Database access is currently performed directly through `*gorm.DB` in handlers and ingestion code; preserve that style unless introducing a deliberate repository/service boundary.
- Because SQLite is file-backed, local runtime data is created under `data/`; do not commit the database or local `.env`.
- Docker image runs as non-root user `tempmail`, persists DB under `/data`, and expects secrets via env / `--env-file`. Note: SMTP port 25 may need root or `setcap` privileges.
- The recommended production shape is the built binary or container behind an HTTPS reverse proxy. SMTP port 25 should be directly exposed (not proxied).
- IMAP/Graph modes are legacy and optional — they enable pulling mail from an external relay inbox. They can coexist with the built-in SMTP server.
