#!/usr/bin/env bash
# Emby-go 平铺部署首装脚本：/opt/emby-go/emby-go + /etc/emby-go/config.yaml，
# 以 root 运行，由 systemd 拉起。升级不需要这个脚本——页面里的「更新」会下载、
# 校验并原子替换 /opt/emby-go/emby-go，然后正常退出交给 systemd 重启。
#
# 用法：
#   sudo ./install.sh                  # 安装最新 Release
#   sudo EMBY_GO_VERSION=v2.2.7 ./install.sh   # 指定 Release 标签
#   sudo EMBY_GO_MIRROR=https://mirror.example.com/github.com ./install.sh
#                                      # 无法直连 GitHub 时用镜像下载资产
#
# 本脚本只做首次安装/重装，不保留旧程序、不创建 releases/backups 目录、
# 不迁移旧数据；已存在的配置不会被覆盖。
set -euo pipefail

REPO=${EMBY_GO_REPO:-holll/Emby-go}
VERSION=${EMBY_GO_VERSION:-}
PREFIX=/opt/emby-go
CONF_DIR=/etc/emby-go
UNIT=/etc/systemd/system/emby-go.service
BIN=$PREFIX/emby-go
CONFIG=$CONF_DIR/config.yaml

die() { echo "错误：$*" >&2; exit 1; }
info() { echo "==> $*"; }

[ "$(id -u)" -eq 0 ] || die "请以 root 运行（需要写 /opt、/etc 并操作 systemd）"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构：$(uname -m)（仅提供 linux-amd64 / linux-arm64）" ;;
esac

command -v curl >/dev/null || die "缺少 curl"
command -v sha256sum >/dev/null || die "缺少 sha256sum"
command -v systemctl >/dev/null || die "缺少 systemctl（本脚本只支持 systemd）"

if [ -z "$VERSION" ]; then
  info "查询最新 Release"
  VERSION=$(curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 \
    "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$VERSION" ] || die "无法解析最新 Release 标签，可用 EMBY_GO_VERSION=vX.Y.Z 指定"
fi

ASSET=emby-go-linux-$ARCH
if [ -n "${EMBY_GO_MIRROR:-}" ]; then
  BASE="${EMBY_GO_MIRROR%/}/$REPO/releases/download/$VERSION"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

info "下载 $VERSION 的 $ASSET 与 SHA256SUMS"
curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$WORK/$ASSET" "$BASE/$ASSET"
if ! curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$WORK/SHA256SUMS" "$BASE/SHA256SUMS"; then
  die "该 Release 未提供 SHA256SUMS，无法校验安装包；请改用包含校验和的 Release（EMBY_GO_VERSION=...）"
fi

info "校验 SHA256"
( cd "$WORK" && grep -E "  $ASSET\$" SHA256SUMS | sha256sum -c - ) \
  || die "SHA256SUMS 校验失败，未安装任何文件"

info "安装程序到 $BIN（不保留旧版本）"
install -d -m 0755 "$PREFIX"
install -m 0755 "$WORK/$ASSET" "$PREFIX/.emby-go.installing"
mv -f "$PREFIX/.emby-go.installing" "$BIN"

SRC_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
UNIT_SRC=""
for candidate in "$SRC_DIR/../deploy/emby-go.service" "$SRC_DIR/emby-go.service"; do
  if [ -f "$candidate" ]; then UNIT_SRC=$candidate; break; fi
done
if [ -z "$UNIT_SRC" ]; then
  info "下载 Service 模板"
  UNIT_SRC=$WORK/emby-go.service
  curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$UNIT_SRC" \
    "https://raw.githubusercontent.com/$REPO/$VERSION/deploy/emby-go.service"
fi
install -m 0644 "$UNIT_SRC" "$UNIT"

info "准备配置目录 $CONF_DIR"
install -d -m 0755 "$CONF_DIR"
if [ -e "$CONFIG" ]; then
  info "保留已有配置 $CONFIG（不覆盖）"
else
  SAMPLE=""
  for candidate in "$SRC_DIR/../config.example.yaml" "$SRC_DIR/config.example.yaml"; do
    if [ -f "$candidate" ]; then SAMPLE=$candidate; break; fi
  done
  if [ -z "$SAMPLE" ]; then
    SAMPLE=$WORK/config.example.yaml
    curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$SAMPLE" \
      "https://raw.githubusercontent.com/$REPO/$VERSION/config.example.yaml"
  fi
  install -m 0600 "$SAMPLE" "$CONFIG"
  echo "    已写入默认配置；请编辑 $CONFIG（至少设置 setup_code）后重启服务。"
fi

if [ -d /var/lib/emby-go ] && [ ! -e "$PREFIX/emby-go.db" ]; then
  echo "    注意：检测到旧数据目录 /var/lib/emby-go，本脚本不会迁移数据。"
  echo "    如需沿用旧库，请在首次启动前手动把 emby-go.db（及缓存）移到 $PREFIX。"
fi

info "注册并启动 systemd 服务"
systemctl daemon-reload
systemctl enable emby-go.service >/dev/null
systemctl restart emby-go.service

info "安装完成：$("$BIN" -version 2>/dev/null || echo "$BIN")"
systemctl --no-pager --lines=5 status emby-go.service || true
echo
echo "程序：$BIN（root 运行，Restart=always）"
echo "配置：$CONFIG"
echo "数据：$PREFIX（WorkingDirectory，默认 SQLite 为 $PREFIX/emby-go.db）"
echo "升级：浏览器打开管理页，点左下角版本号；也可重新运行本脚本重装。"
echo "日志：journalctl -u emby-go -f"
