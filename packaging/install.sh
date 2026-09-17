#!/usr/bin/env bash
# ppts 单机安装脚本（二进制分发形态）
#
# 用法：sudo ./install.sh
# 幂等：可重复执行；已存在的配置/账号/数据库不会被覆盖。
#
# 布局：
#   /opt/ppts/bin/          ppts（server/worker/migrate 子命令）+ ffmpeg ffprobe
#   /etc/ppts/config.env    两个服务共用的环境配置（systemd EnvironmentFile）
#   /var/lib/ppts/          本地对象存储 + LibreOffice profile
#   systemd: ppts-api.service ppts-worker.service
#
# 环境变量开关：
#   SKIP_DEPS=1    跳过系统依赖安装（离线/已装好依赖时）
#   SKIP_START=1   只安装文件与配置，不启动服务

set -euo pipefail

PREFIX=/opt/ppts
CONFIG_DIR=/etc/ppts
DATA_DIR=/var/lib/ppts
APP_USER=ppts
DB_NAME=ppts
DB_USER=ppts_app
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { printf '[install] %s\n' "$*"; }
die() { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "请以 root 运行（sudo ./install.sh）"
[[ -x "$SRC_DIR/bin/ppts" ]] || die "未找到 bin/ppts，请在解压后的发行目录内运行"

# ---------- 1. 系统依赖 ----------
# LibreOffice（页面渲染）、poppler（PDF→PNG）、中文字体（字幕烧录）、PostgreSQL。
install_deps() {
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y --no-install-recommends \
      libreoffice-impress-nogui poppler-utils fonts-wqy-zenhei postgresql
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y libreoffice-impress poppler-utils wqy-zenhei-fonts postgresql-server
    # RHEL 系首次安装需初始化数据目录
    if [[ ! -d /var/lib/pgsql/data/base ]]; then
      postgresql-setup --initdb || /usr/bin/postgresql-setup --initdb
    fi
  else
    log "未识别的包管理器，跳过依赖安装；请自行确保 libreoffice-impress / poppler-utils / 中文字体 / postgresql 可用"
  fi
}
if [[ "${SKIP_DEPS:-0}" != "1" ]]; then
  log "安装系统依赖（LibreOffice / poppler / 中文字体 / PostgreSQL）…"
  install_deps
else
  log "SKIP_DEPS=1，跳过系统依赖安装"
fi

# ---------- 2. 账号与目录 ----------
if ! id -u "$APP_USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$APP_USER"
  log "创建系统账号 $APP_USER"
fi
install -d -m 0755 "$PREFIX/bin"
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$DATA_DIR" "$DATA_DIR/objects"

# ---------- 3. 安装二进制 ----------
install -m 0755 "$SRC_DIR/bin/ppts" "$PREFIX/bin/"
# ffmpeg/ffprobe 为随包附带的静态构建（带 libass 字幕滤镜）
install -m 0755 "$SRC_DIR/bin/ffmpeg" "$SRC_DIR/bin/ffprobe" "$PREFIX/bin/"
log "二进制安装到 $PREFIX/bin"

# ---------- 4. 配置（已存在则不覆盖） ----------
if [[ ! -f "$CONFIG_DIR/config.env" ]]; then
  install -d -m 0750 "$CONFIG_DIR"
  DB_PW="$(openssl rand -base64 24 | tr -d '/+=' | head -c 32)"
  OBJ_SECRET="$(openssl rand -base64 32)"
  JWT_SECRET="$(openssl rand -base64 32)"
  PEPPER="$(openssl rand -base64 32)"
  sed -e "s|PPTS_DATABASE_URL=.*|PPTS_DATABASE_URL=postgres://$DB_USER:$DB_PW@127.0.0.1:5432/$DB_NAME?sslmode=disable|" \
      -e "s|PPTS_OBJECT_SECRET=.*|PPTS_OBJECT_SECRET=$OBJ_SECRET|" \
      -e "s|PPTS_JWT_SECRET=.*|PPTS_JWT_SECRET=$JWT_SECRET|" \
      -e "s|PPTS_PASSWORD_PEPPER=.*|PPTS_PASSWORD_PEPPER=$PEPPER|" \
      "$SRC_DIR/config.env.example" > "$CONFIG_DIR/config.env"
  chmod 0640 "$CONFIG_DIR/config.env"
  chown root:"$APP_USER" "$CONFIG_DIR/config.env"
  log "生成配置 $CONFIG_DIR/config.env（数据库口令与密钥已随机化）"
else
  DB_PW="$(sed -n 's|^PPTS_DATABASE_URL=postgres://'"$DB_USER"':\([^@]*\)@.*|\1|p' "$CONFIG_DIR/config.env")"
  log "保留既有配置 $CONFIG_DIR/config.env"
fi

# ---------- 5. PostgreSQL：建库建号 + 迁移 ----------
systemctl enable --now postgresql >/dev/null 2>&1 || service postgresql start || true

pg_exec() { sudo -u postgres psql -v ON_ERROR_STOP=1 -qAt -c "$1"; }

if ! sudo -u postgres psql -lqtA | grep -q "^$DB_NAME|"; then
  sudo -u postgres createdb "$DB_NAME"
  log "创建数据库 $DB_NAME"
fi

# 迁移（含建角色 ppts_app/ppts_migrator 与 pgcrypto 扩展）需要 superuser，走本地 socket peer 认证。
sudo -u postgres env PPTS_MIGRATE_DATABASE_URL="postgres:///$DB_NAME?host=/var/run/postgresql" \
  "$PREFIX/bin/ppts" migrate
# 统一运行账号口令（角色可能由迁移创建，也可能由旧配置保留）
pg_exec "ALTER ROLE $DB_USER LOGIN PASSWORD '$DB_PW';"
log "数据库迁移完成，运行账号 $DB_USER 口令已同步"

# ---------- 6. systemd ----------
install -m 0644 "$SRC_DIR/systemd/ppts-api.service" "$SRC_DIR/systemd/ppts-worker.service" /etc/systemd/system/
systemctl daemon-reload
if [[ "${SKIP_START:-0}" != "1" ]]; then
  systemctl enable --now ppts-api ppts-worker
  log "服务已启动：systemctl status ppts-api ppts-worker"
else
  log "SKIP_START=1，未启动服务；手动执行 systemctl enable --now ppts-api ppts-worker"
fi

ADDR="$(sed -n 's|^PPTS_HTTP_ADDR=||p' "$CONFIG_DIR/config.env")"
PORT="${ADDR##*:}"
log "安装完成。控制台: http://<本机地址>:${PORT:-8080}/  健康检查: http://127.0.0.1:${PORT:-8080}/healthz"
