#!/usr/bin/env bash
# 本地预览实例：WSL + LibreOffice 渲染，端口 8077，systemd --user 常驻。
# 幂等：可重复执行。数据来自 Windows 现有实例的一次性副本（~/ppts-preview）。
#
# 密钥约定：
# - PPTS_OBJECT_SECRET 是对象存储的信封密钥，必须与源实例一致，绝不重新生成；
# - PPTS_GATEWAY_AES_KEY_BASE64 是模型网关凭据的 AES-256 密钥，缺失/非法时生成，
#   已存在时原样保留——换密钥会让 DB 里既有的 encrypted_creds 永远解不开。
#
# 模型网关配置不会随 DB 复制自动可用：凭据以各实例自己的 AES 密钥加密，AAD 还绑定
# tenant/name/kind。跨实例搬迁必须用 scripts/gateway-import 解密后重新加密。
# 可选参数（同时给全才触发，密钥只走环境变量、不落脚本）：
#   GATEWAY_SRC_DB=/path/other.db GATEWAY_SRC_AES_KEY=<base64> \
#     GATEWAY_SRC_TENANT_ID=<可选，默认开发租户> bash deploy-wsl-preview.sh
# 迁移结果落在平台共享租户，租户页面只读可见。工具需先交叉编译到 $ROOT/bin/gateway-import：
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o <目标> ./scripts/gateway-import
#
# 出站代理（仅 systemd 主实例 /etc/ppts/config.env）：127.0.0.1:7892 由 wsl-proxy-fwd.py
# 提供，是 siliconflow / agnes 等外部模型出网的必要通道；缺省直连会 TLS 握手超时，
# 曾致 TTS/讲稿任务 10 次重试全失败。不要把它当失效残留清掉。
# 交互式 shell 常带另一套代理：探测本机端口必须加 --noproxy '*'（或 curl -x 明确指定），
# 否则回环请求会被代理转成 502，看起来像服务挂了；服务端 NO_PROXY 已含 127.0.0.1，不受影响。
# 主实例用 Postgres、预览实例用 SQLite，两者 AES 密钥不同，本地 TTS 网关要各写一次：
#   DST_DRIVER=postgres PPTS_CONFIG=/etc/ppts/config.env go run ./scripts/gateway-local-tts
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
# 动效渲染服务（docker 容器 ppts-renderer，镜像 ppts/renderer:verify，映射 127.0.0.1:8091）。
# 两者缺一时动效预览/导出会如实置灰；目录必须与容器 -v 挂载的宿主目录是同一个。
PPTS_RENDERER_URL=http://127.0.0.1:8091
PPTS_ANIMATION_ASSET_DIR=$ROOT/assets
# 单页预览预算：纯 CPU 渲染吞吐约 2x 实时（帧并发 4），但 staging/下载也占时间，
# 47s 的页实测全程约 37s。默认 3s 会恒定 504；上限 60s，前端兜底 90s，必须小于它。
PPTS_ANIMATION_PREVIEW_BUDGET=60s
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

# 单元文件刚重写过，先 reload 再 stop，避免 systemd 报 "changed on disk" 警告。
systemctl --user daemon-reload

# 可选：从另一实例导入已保存的模型网关（凭据按本实例密钥重新加密，落为平台共享）。
# 只写 DB，不触碰源实例；密钥全程只经环境变量，不打印、不落盘。
if [[ -n "${GATEWAY_SRC_DB:-}" && -n "${GATEWAY_SRC_AES_KEY:-}" ]]; then
  if [[ ! -x "$ROOT/bin/gateway-import" ]]; then
    echo "缺少 $ROOT/bin/gateway-import：请先交叉编译 scripts/gateway-import" >&2
    exit 1
  fi
  DB_PATH="$(sed -n 's/^PPTS_DATABASE_URL=//p' "$CONFIG" | tail -1)"
  DST_KEY="$(sed -n 's/^PPTS_GATEWAY_AES_KEY_BASE64=//p' "$CONFIG" | tail -1)"
  # 迁移期间让 systemd 单元放手，下面的 restart 会重新拉起。
  systemctl --user stop ppts-preview.service ppts-preview-worker.service || true
  env SRC_DB="$GATEWAY_SRC_DB" DST_DB="$DB_PATH" \
      SRC_AES_KEY="$GATEWAY_SRC_AES_KEY" DST_AES_KEY="$DST_KEY" \
      SRC_TENANT_ID="${GATEWAY_SRC_TENANT_ID:-00000000-0000-0000-0000-000000000001}" \
      "$ROOT/bin/gateway-import"
fi

systemctl --user enable --now ppts-preview.service ppts-preview-worker.service
# enable --now 对已 active 的单元不会重启；改了 EnvironmentFile 必须显式 restart 才生效。
systemctl --user restart ppts-preview.service ppts-preview-worker.service
echo "==> units"
systemctl --user is-active ppts-preview.service ppts-preview-worker.service
# 部署结果必须如实说出预览能力：这个实例没有动画渲染服务时，页面会把按钮置灰并说明原因，
# 而不是让用户点出一个 404 再看到「预览失败」。渲染容器（docker run 手册见 deploy/renderer/README.md）
# 不归本脚本管，但「配了地址、容器却没跑」和「没配」对用户是同一种失败，这里都要点破。
if ! grep -q '^PPTS_RENDERER_URL=' "$CONFIG" || ! grep -q '^PPTS_ANIMATION_ASSET_DIR=' "$CONFIG"; then
  echo "==> 动效预览未启用（缺 PPTS_RENDERER_URL / PPTS_ANIMATION_ASSET_DIR）；页面会置灰并说明原因"
elif ! docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^ppts-renderer$'; then
  echo "!!> PPTS_RENDERER_URL 已配置，但渲染容器 ppts-renderer 没在运行——预览/动画导出会报渲染服务不可用"
  echo "    启动（镜像 ppts/renderer:verify，注意 -concurrency 是帧并发，单页预览建议 ≥4）："
  echo "    docker run -d --name ppts-renderer --restart unless-stopped -p 127.0.0.1:8091:8090 \\"
  echo "      --read-only --tmpfs /work:size=2g,mode=1777 --tmpfs /tmp:size=512m,mode=1777 \\"
  echo "      --mount type=bind,src=$ROOT/assets,dst=/assets,readonly \\"
  echo "      --cpus 4 --memory 6g --pids-limit 2048 --security-opt no-new-privileges:true \\"
  echo "      ppts/renderer:verify -addr :8090 -backend remotion -workdir /work -renderer-script /app/render.mjs \\"
  echo "      -assets-dir /assets -concurrency 4 -page-concurrency 1 -max-queued 8 -job-ttl 30m"
fi
