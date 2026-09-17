#!/usr/bin/env bash
# 构建单机二进制发行包：dist/ppts-<version>-linux-amd64.tar.gz（+ .sha256）
#
# 用法：
#   scripts/package.sh [version]      # version 缺省取 git describe，再缺省 dev
# 开关：
#   SKIP_WEB=1     跳过前端构建（要求 web/dist/index.html 已存在）
#   SKIP_FFMPEG=1  跳过 ffmpeg 下载（要求 build/ffmpeg/ffmpeg 与 ffprobe 已存在）
set -euo pipefail

# 环境里的 http_proxy 可能指向已失效的代理（本机 docker 配置会注入），下载一律直连
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY no_proxy NO_PROXY || true

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-${VERSION:-}}"
if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
fi
PKG="ppts-${VERSION}-linux-amd64"
STAGE="dist/$PKG"
FFMPEG_URL="https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz"

log() { printf '[package] %s\n' "$*"; }

# ---------- 1. 前端 ----------
if [[ "${SKIP_WEB:-0}" != "1" || ! -f web/dist/index.html ]]; then
  log "构建前端 web/dist …"
  (cd web && npm ci && npm run build)
  # vite build 会清空 dist（含占位文件）；补回 .gitkeep 保证 go:embed 始终可编译
  touch web/dist/.gitkeep
else
  log "SKIP_WEB=1，沿用既有 web/dist"
fi

# ---------- 2. Go 二进制（纯静态，单入口子命令，内嵌前端与迁移） ----------
LDFLAGS="-s -w -X main.version=${VERSION}"
log "构建 ppts（version=$VERSION）…"
CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags "$LDFLAGS" -o "$STAGE/bin/ppts" ./cmd/ppts

# ---------- 3. 静态 ffmpeg（带 libass，字幕烧录必需） ----------
mkdir -p build
if [[ "${SKIP_FFMPEG:-0}" != "1" || ! -x build/ffmpeg/ffmpeg ]]; then
  log "下载静态 ffmpeg …"
  curl -fSL --retry 3 "$FFMPEG_URL" -o build/ffmpeg-static.tar.xz
  rm -rf build/ffmpeg-*-static
  tar -xJf build/ffmpeg-static.tar.xz -C build
  mkdir -p build/ffmpeg
  install -m 0755 build/ffmpeg-*-static/ffmpeg build/ffmpeg/ffmpeg
  install -m 0755 build/ffmpeg-*-static/ffprobe build/ffmpeg/ffprobe
else
  log "SKIP_FFMPEG=1，沿用 build/ffmpeg 缓存"
fi
# 与 Dockerfile.worker 一致的护栏：缺 subtitles 滤镜 = 字幕烧录必然失败，打包直接报错
# 注意先落变量再 grep：管道 + pipefail + grep -q 提前退出会让 ffmpeg 吃 SIGPIPE 造成误判
ffmpeg_filters="$(build/ffmpeg/ffmpeg -hide_banner -filters 2>/dev/null)"
grep -q "subtitles" <<<"$ffmpeg_filters" \
  || { echo "[package] ERROR: ffmpeg 缺 subtitles 滤镜（libass）" >&2; exit 1; }
install -m 0755 build/ffmpeg/ffmpeg build/ffmpeg/ffprobe "$STAGE/bin/"

# ---------- 4. 打包资产 ----------
install -m 0755 packaging/install.sh "$STAGE/install.sh"
install -m 0644 packaging/config.env.example "$STAGE/config.env.example"
mkdir -p "$STAGE/systemd" "$STAGE/migrations"
install -m 0644 packaging/ppts-api.service packaging/ppts-worker.service "$STAGE/systemd/"
# 迁移已内嵌进二进制（ppts-api migrate）；随包附 SQL 仅供 DBA 审阅/手工排查
cp migrations/*.sql "$STAGE/migrations/"

# ---------- 5. 归档与校验 ----------
tar -czf "dist/$PKG.tar.gz" -C dist "$PKG"
(cd dist && sha256sum "$PKG.tar.gz" > "$PKG.tar.gz.sha256")
log "完成：dist/$PKG.tar.gz"
ls -lh "dist/$PKG.tar.gz"
