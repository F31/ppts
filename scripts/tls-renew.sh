#!/usr/bin/env bash
# 每日由 systemd timer 调用：证书不足 30 天时才停 nginx 做 TLS-ALPN-01。
#
# 依赖：/usr/local/bin/lego、/etc/lego 已由首次签发初始化。
# 停 nginx 的原因是 TLS-ALPN-01 必须独占 443；重启后 nginx 会重新读取证书文件。
set -euo pipefail

DOMAIN="${1:-ppts.mindbase.fun}"
LEGO="${LEGO_BIN:-/usr/local/bin/lego}"
LEGO_PATH="${LEGO_PATH:-/etc/lego}"
CERT="$LEGO_PATH/certificates/$DOMAIN.crt"

exec 9>/run/ppts-tls-renew.lock
flock -n 9 || exit 0

if [[ -f "$CERT" ]] && openssl x509 -checkend $((30 * 86400)) -noout -in "$CERT"; then
  echo "证书仍有 30 天以上，跳过 $DOMAIN"
  exit 0
fi

systemctl stop nginx
restore() { systemctl start nginx; }
trap restore EXIT

"$LEGO" --tls --domains "$DOMAIN" --path "$LEGO_PATH" renew --days 30
