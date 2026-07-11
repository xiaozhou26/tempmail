# syntax=docker/dockerfile:1

# Pure-Go SQLite (glebarez/sqlite / modernc.org/sqlite) — no CGO, static binary.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/tempmail .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 1000 tempmail \
    && mkdir -p /data \
    && chown tempmail:tempmail /data

COPY --from=builder /out/tempmail /usr/local/bin/tempmail

USER tempmail
WORKDIR /app

ENV LISTEN_ADDR=:8080 \
    DB_PATH=/data/tempmail.db

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["tempmail"]
