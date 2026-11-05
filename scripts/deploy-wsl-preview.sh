#!/usr/bin/env bash
# 本地预览实例：WSL + LibreOffice 渲染，端口 8077，systemd --user 常驻。
# 幂等：可重复执行。数据来自 Windows 现有实例的一次性副本（~/ppts-preview）。
#
# 密钥约定：
# - PPTS_OBJECT_SECRET 是对象存储的信封密钥，必须与源实例一致，绝不重新生成；
# - PPTS_GATEWAY_AES_KEY_BASE64 是模型网关凭据的 AES-256 密钥，缺失/非法时生成，
#   已存在时原样保留——换密钥会让 DB 里既有的 encrypted_creds 永远解不开。
set -euo pipefail

ROOT="$HOME/ppts-preview"
UNIT_DIR="$HOME/.config/systemd/user"
CONFIG="$ROOT/config.env"
mkdir -p "$UNIT_DIR"

export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}"

# 首次部署：config.env 不存在时才创建。对象信封密钥必须显式提供，避免与源数据不匹配。
if [[ ! -f "$CONFIG" ]]; then
  : "${PPTS_OBJECT_SECRET:?首次部署需提供 PPTS_OBJECT_SECRET（必须与源实例 / 既有对象数据一致）}"
  cat > "$CONFIG" <<ENV
PPTS_DB_DRIVER=sqlite
PPTS_DATABASE_URL=$ROOT/data/ppts.db
PPTS_OBJECT_BACKEND=local
PPTS_OBJECT_ROOT=$ROOT/objects
PPTS_OBJECT_SECRET=$PPTS_OBJECT_SECRET
PPTS_AUTH_DEV_HEADERS=true
PPTS_TTS_PROVIDER=fake
PPTS_WEB_ROOT=/mnt/e/projects/ppts/web/dist
PPTS_HTTP_ADDR=:8077
TZ=Asia/Shanghai
ENV
  echo "==> 已创建 $CONFIG"
fi
chmod 600 "$CONFIG"

random_key() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr -d '\n'
  else
    head -c 32 /dev/urandom | base64 | tr -d '\n'
  fi
}

key_ok() {
  local raw len
  raw="$(sed -n 's/^PPTS_GATEWAY_AES_KEY_BASE64=//p' "$CONFIG" | tail -1)"
  [[ -n "$raw" ]] || return 1
  len="$(printf '%s' "$raw" | base64 -d 2>/dev/null | wc -c)" || return 1
  [[ "$len" -eq 32 ]]
}

# 缺密钥时 /api/model-gateways 全部 503，模型服务页报「功能未启用」。
if ! key_ok; then
  GW_KEY="$(random_key)"
  if grep -q '^PPTS_GATEWAY_AES_KEY_BASE64=' "$CONFIG"; then
    sed -i "s|^PPTS_GATEWAY_AES_KEY_BASE64=.*|PPTS_GATEWAY_AES_KEY_BASE64=$GW_KEY|" "$CONFIG"
  else
    printf 'PPTS_GATEWAY_AES_KEY_BASE64=%s\n' "$GW_KEY" >> "$CONFIG"
  fi
  echo "==> 已生成 PPTS_GATEWAY_AES_KEY_BASE64（模型网关启用）"
else
  echo "==> 保留既有 PPTS_GATEWAY_AES_KEY_BASE64（避免已有凭据失效）"
fi

cat > "$UNIT_DIR/ppts-preview.service" <<UNIT
[Unit]
Description=PPTS preview server (WSL, LibreOffice pages, :8077)
After=network-online.target

[Service]
Type=simple
EnvironmentFile=$CONFIG
ExecStart=$ROOT/bin/ppts server
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
UNIT

cat > "$UNIT_DIR/ppts-preview-worker.service" <<UNIT
[Unit]
Description=PPTS preview worker (WSL, LibreOffice page renderer)
After=network-online.target ppts-preview.service

[Service]
Type=simple
EnvironmentFile=$CONFIG
ExecStart=$ROOT/bin/ppts worker
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
UNIT

# 让出由交互会话托管的旧进程交给 systemd；systemd 自己的单元留给下面的 restart，
# 否则每次执行都会先误杀再被 Restart=always 拉起，白白抖一次。
for pid in $(pgrep -f 'bin/ppts (server|worker)' || true); do
  if ! grep -q 'ppts-preview' "/proc/$pid/cgroup" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
  fi
done
sleep 2

systemctl --user daemon-reload
systemctl --user enable --now ppts-preview.service ppts-preview-worker.service
# enable --now 对已 active 的单元不会重启；改了 EnvironmentFile 必须显式 restart 才生效。
systemctl --user restart ppts-preview.service ppts-preview-worker.service
echo "==> units"
systemctl --user is-active ppts-preview.service ppts-preview-worker.service
