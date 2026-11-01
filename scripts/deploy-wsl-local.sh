#!/usr/bin/env bash
#
# 在 WSL（Ubuntu）里以当前工作区的代码跑一套本地 ppts 实例。
#
# 与 packaging/install.sh 的区别：
#   - install.sh 是**系统级**生产安装（/opt/ppts、/etc/ppts、postgres、root systemd）；
#   - 本脚本是**用户级**开发实例：装到 $HOME，用 SQLite，systemd --user 托管，不需要 sudo。
#
# 幂等：可重复执行。二进制每次重建；config.env 与 unit 文件已存在时保留（除非 --force-units）。
#
# 用法：
#   bash scripts/deploy-wsl-local.sh                 # 构建并重启
#   bash scripts/deploy-wsl-local.sh --port 9000     # 指定端口（首次写入 config.env）
#   bash scripts/deploy-wsl-local.sh --force-units   # 重写 unit 文件（改了模板后用）
set -euo pipefail

RUN_DIR="${HOME}/ppts-run"
DATA_DIR="${HOME}/ppts-data"
UNIT_DIR="${HOME}/.config/systemd/user"
PORT="${PPTS_WSL_PORT:-9000}"
FORCE_UNITS=0

while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="${2:?--port 需要端口号}"; shift 2 ;;
    --force-units) FORCE_UNITS=1; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

# systemd --user 是本脚本的前提：没有它就没有自动重启和开机自启。
if ! systemctl --user is-system-running >/dev/null 2>&1; then
  echo "错误：systemctl --user 不可用。请在 WSL 内、且 /etc/wsl.conf 里 systemd=true 的环境执行。" >&2
  exit 1
fi

# Go：优先用脚本自带的用户级工具链，其次用 PATH 里的。
GO_BIN=""
for cand in "${HOME}/.local/go/bin/go" "$(command -v go 2>/dev/null || true)"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  echo "错误：找不到 go。装到用户目录即可，无需 sudo：" >&2
  echo '  curl -sL https://mirrors.aliyun.com/golang/go1.27.3.linux-amd64.tar.gz | tar -C "$HOME/.local" -xz' >&2
  exit 1
fi

REPO="$(cd "$(dirname "$0")/.." && pwd)"

# 前端产物是 go:embed 打进二进制的；缺了会编译失败，所以先给出可执行的补救命令。
if [ ! -f "$REPO/web/dist/index.html" ]; then
  echo "错误：缺少 web/dist（前端产物要内嵌进二进制）。先构建：" >&2
  echo "  cd $REPO/web && npm ci && npm run build" >&2
  exit 1
fi

mkdir -p "$RUN_DIR/bin" "$DATA_DIR/objects" "$UNIT_DIR"

echo "==> 构建（$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo no-git)）"
# CGO_ENABLED=0：SQLite 用 modernc 纯 Go 实现，静态二进制在 WSL 里更好搬。
(cd "$REPO" && CGO_ENABLED=0 GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  "$GO_BIN" build -o "$RUN_DIR/bin/ppts" ./cmd/ppts)
echo "    $RUN_DIR/bin/ppts ($(du -h "$RUN_DIR/bin/ppts" | cut -f1))"

if [ ! -f "$RUN_DIR/config.env" ]; then
  cat > "$RUN_DIR/config.env" <<EOF
# 本地开发实例：SQLite 单租户、本地对象存储、假 TTS。
# PPTS_AUTH_DEV_HEADERS 只允许用于本机开发——它会接受未签名的租户/用户头。
PPTS_DB_DRIVER=sqlite
PPTS_DATABASE_URL=$DATA_DIR/ppts.db
PPTS_OBJECT_BACKEND=local
PPTS_OBJECT_ROOT=$DATA_DIR/objects
PPTS_OBJECT_SECRET=$(openssl rand -base64 32)
PPTS_AUTH_DEV_HEADERS=true
PPTS_TTS_PROVIDER=fake
PPTS_HTTP_ADDR=:$PORT
TZ=Asia/Shanghai
EOF
  chmod 600 "$RUN_DIR/config.env"
  echo "==> 已生成 $RUN_DIR/config.env（端口 :$PORT）"
else
  echo "==> 保留既有 $RUN_DIR/config.env"
fi

write_unit() {
  local name="$1" desc="$2" exec_args="$3" after="$4"
  local path="$UNIT_DIR/$name.service"
  if [ -f "$path" ] && [ "$FORCE_UNITS" -eq 0 ]; then
    echo "    保留 $name.service"
    return
  fi
  cat > "$path" <<EOF
[Unit]
Description=$desc
After=network-online.target $after

[Service]
Type=simple
EnvironmentFile=$RUN_DIR/config.env
ExecStart=$RUN_DIR/bin/ppts $exec_args
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
EOF
  echo "    写入 $name.service"
}

echo "==> systemd 用户单元"
write_unit "ppts-local"        "PPTS local server (WSL, SQLite, single tenant)" "server" ""
write_unit "ppts-worker-local" "PPTS local worker (WSL, SQLite, fake TTS)"      "worker" "ppts-local.service"

systemctl --user daemon-reload
systemctl --user enable ppts-local.service ppts-worker-local.service >/dev/null
systemctl --user restart ppts-local.service ppts-worker-local.service

# 只等健康检查，不猜"应该起来了"：端口被占/迁移失败都会在这里暴露。
ADDR_LINE=$(grep -E '^PPTS_HTTP_ADDR=' "$RUN_DIR/config.env" | tail -1 | cut -d= -f2)
for _ in $(seq 1 20); do
  if curl -sf --max-time 2 "http://127.0.0.1${ADDR_LINE}/healthz" >/dev/null 2>&1; then
    echo "==> 就绪：http://localhost${ADDR_LINE}"
    echo "    数据：$DATA_DIR   日志：journalctl --user -u ppts-local -f"
    exit 0
  fi
  sleep 1
done

echo "错误：20 秒内 /healthz 未就绪，查看日志：" >&2
systemctl --user --no-pager status ppts-local.service >&2 || true
exit 1
