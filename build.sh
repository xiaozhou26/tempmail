#!/usr/bin/env bash
# Cross-compile static tempmail binaries (pure-Go SQLite, no CGO).
#
# Usage:
#   ./build.sh                              # default: linux/amd64 -> dist/tempmail-linux-amd64
#   PLATFORMS="linux/amd64 linux/arm64" ./build.sh
#   VERSION=v1.0.0 ./build.sh
#   OUT=./tempmail GOOS=linux GOARCH=amd64 ./build.sh   # single custom output

set -euo pipefail

VERSION="${VERSION:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

# Single-output mode when OUT is set (or GOOS/GOARCH pair without PLATFORMS).
if [[ -n "${OUT:-}" ]]; then
  GOOS="${GOOS:-linux}"
  GOARCH="${GOARCH:-amd64}"
  echo "==> build ${GOOS}/${GOARCH} -> ${OUT} (version=${VERSION})"
  mkdir -p "$(dirname "$OUT")"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o "$OUT" .
  ls -lh "$OUT"
  file "$OUT" 2>/dev/null || true
  exit 0
fi

PLATFORMS="${PLATFORMS:-linux/amd64}"

echo "==> building tempmail (version=${VERSION})"
mkdir -p dist

for platform in $PLATFORMS; do
  GOOS="${platform%/*}"
  GOARCH="${platform#*/}"
  EXT=""
  if [[ "$GOOS" == "windows" ]]; then
    EXT=".exe"
  fi
  OUT="dist/tempmail-${GOOS}-${GOARCH}${EXT}"
  echo "--> ${GOOS}/${GOARCH} -> ${OUT}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o "$OUT" .
  ls -lh "$OUT"
  file "$OUT" 2>/dev/null || true
done

echo "==> done. Deploy: upload binary + .env, then run it."
