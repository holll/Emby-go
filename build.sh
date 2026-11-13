#!/usr/bin/env bash
# 构建 linux/amd64 版本的 Emby-go 二进制：dist/emby-go_linux_amd64
# 与 build.bat 保持同一套版本注入（-X main.version/commit/buildTime）。
set -euo pipefail

cd "$(dirname "$0")"

VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDTIME=$(TZ=Asia/Shanghai date '+%Y-%m-%d_%H-%M-%S')

OUTDIR=dist
OUTFILE=$OUTDIR/emby-go_linux_amd64
mkdir -p "$OUTDIR"

cat > "$OUTDIR/version.txt" <<EOF
Version=$VERSION
Commit=$COMMIT
BuildTime=$BUILDTIME
EOF

echo "Version: $VERSION"
echo "Commit:  $COMMIT"
echo "Time:    $BUILDTIME"
echo

echo "---------------------------------------------------"
echo "Building $OUTFILE"
echo "---------------------------------------------------"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "$OUTFILE" \
  -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildTime=$BUILDTIME" \
  ./cmd/metatube

if command -v upx >/dev/null 2>&1; then
  echo "Compressing with UPX..."
  upx --best --lzma "$OUTFILE"
fi

echo
echo "Done: $OUTFILE"
ls -lh "$OUTFILE"