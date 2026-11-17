#!/usr/bin/env bash
# 生产升级：安装新二进制 → 迁移 → 重启 → healthz。
#
# 在目标机以 root 执行，不把数据库 superuser 口令写进仓库：
#   scripts/upgrade-production.sh /path/to/ppts
#
# 迁移 DSN 优先级：
#   1. /etc/ppts/config.env 里的 PPTS_MIGRATE_DATABASE_URL；
#   2. PostgreSQL 且未配置时，用本机 postgres peer 认证（与 packaging/install.sh 同一路径）；
#   3. SQLite 跳过独立迁移（SQLite 迁移由进程启动时执行）。
#
# 失败时恢复旧二进制并重启旧服务；成功启动旧进程不会因为文件已替换而自动换代码。
set -euo pipefail

BIN_SRC="${1:?用法: upgrade-production.sh /path/to/ppts}"
PREFIX=/opt/ppts
CONFIG=/etc/ppts/config.env
SERVICES=(ppts-api ppts-worker)

[[ "$(id -u)" -eq 0 ]] || { echo "必须用 root 执行" >&2; exit 1; }
[[ -x "$BIN_SRC" ]] || { echo "不是可执行文件：$BIN_SRC" >&2; exit 1; }
[[ -f "$CONFIG" ]] || { echo "缺少 $CONFIG" >&2; exit 1; }

TS="$(date +%Y%m%d-%H%M%S)"
BACKUP="$PREFIX/bin/ppts.backup-before-upgrade-$TS"

# 只读取迁移/端口需要的键；systemd 的 EnvironmentFile 也是这套 KEY=VALUE 语法。
set -a
# shellcheck disable=SC1090
. "$CONFIG"
set +a

DB_DRIVER="${PPTS_DB_DRIVER:-postgres}"
MIGRATE_DSN="${PPTS_MIGRATE_DATABASE_URL:-}"
if [[ -z "$MIGRATE_DSN" && "$DB_DRIVER" == "postgres" ]]; then
  DB_URL="${PPTS_DATABASE_URL:?PostgreSQL 模式缺少 PPTS_DATABASE_URL}"
  DB_NAME="${DB_URL#*://}"
  DB_NAME="${DB_NAME#*/}"
  DB_NAME="${DB_NAME%%\?*}"
  DB_NAME="${DB_NAME%%/*}"
  MIGRATE_DSN="postgres:///$DB_NAME?host=/var/run/postgresql"
fi

cp "$PREFIX/bin/ppts" "$BACKUP"
install -m 0755 "$BIN_SRC" "$PREFIX/bin/ppts"
echo "已备份旧二进制：$BACKUP"

if [[ -n "$MIGRATE_DSN" ]]; then
  if ! sudo -u postgres env PPTS_MIGRATE_DATABASE_URL="$MIGRATE_DSN" "$PREFIX/bin/ppts" migrate; then
    echo "迁移失败，恢复旧二进制" >&2
    install -m 0755 "$BACKUP" "$PREFIX/bin/ppts"
    exit 1
  fi
else
  echo "SQLite 模式：跳过独立迁移（进程启动时执行）"
fi

if ! systemctl restart "${SERVICES[@]}"; then
  echo "服务重启失败，恢复旧二进制" >&2
  install -m 0755 "$BACKUP" "$PREFIX/bin/ppts"
  systemctl restart "${SERVICES[@]}" || true
  exit 1
fi

ADDR="${PPTS_HTTP_ADDR:-:8080}"
PORT="${ADDR##*:}"
READY=0
for _ in $(seq 1 15); do
  if curl -fsS --noproxy '*' "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 1
done
if [[ "$READY" -ne 1 ]]; then
  echo "healthz 未就绪，恢复旧二进制并重启" >&2
  install -m 0755 "$BACKUP" "$PREFIX/bin/ppts"
  systemctl restart "${SERVICES[@]}" || true
  exit 1
fi

"$PREFIX/bin/ppts" version
systemctl is-active "${SERVICES[@]}"
