#!/usr/bin/env bash
# 交叉编译 tempmail 的 Linux amd64 二进制。
#
# 使用纯 Go 的 SQLite 驱动 (modernc.org/sqlite via glebarez/sqlite)，
# 因此 CGO_ENABLED=0 即可，不需要任何 C 交叉编译器，编出的是静态二进制。
#
# 用法:
#   ./build.sh              # 输出 dist/tempmail-linux-amd64
#   OUT=./tempmail ./build.sh

set -euo pipefail

OUT="${OUT:-dist/tempmail-linux-amd64}"

echo "==> 交叉编译 tempmail -> Linux amd64 (纯 Go, 静态二进制)"

mkdir -p "$(dirname "$OUT")"

CGO_ENABLED=0 \
GOOS=linux \
GOARCH=amd64 \
go build -trimpath -ldflags="-s -w" -o "$OUT" .

echo "==> 完成: $OUT"
ls -lh "$OUT"
file "$OUT" 2>/dev/null || true
echo "==> 部署: 上传 $OUT 到服务器，配合 .env 运行: ./$OUT"
