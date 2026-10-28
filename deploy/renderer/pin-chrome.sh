#!/usr/bin/env bash
# 把渲染镜像里的 Chrome 固定到某个版本，并记录它的 SHA-256。
#
# 为什么需要它：Chrome for Testing 的版本化 URL 是不可变的，但构建期仍应校验摘要——
# 下载被截断、走企业镜像源被替换，都会表现成「渲染超时」这种无关的错误。
#
# 用法：
#   ./pin-chrome.sh                 # 固定到 CHROME_VERSION 里的版本，写 chrome.sha256
#   ./pin-chrome.sh 156.0.8078.73   # 换一个版本（同时更新 CHROME_VERSION）
#
# 产物（都要提交）：
#   CHROME_VERSION  版本号单一事实源
#   chrome.sha256   对应 zip 的摘要；版本一变必须重新生成
set -euo pipefail

cd "$(dirname "$0")"

if [[ $# -ge 1 ]]; then
  echo "$1" > CHROME_VERSION
fi

version="$(tr -d '[:space:]' < CHROME_VERSION)"
if [[ -z "$version" ]]; then
  echo "CHROME_VERSION is empty" >&2
  exit 1
fi

url="https://storage.googleapis.com/chrome-for-testing-public/${version}/linux64/chrome-headless-shell-linux64.zip"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading ${url}"
curl -fsSL --retry 3 -o "$tmp/chrome.zip" "$url"

if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "$tmp/chrome.zip" | awk '{print $1}')"
else
  digest="$(shasum -a 256 "$tmp/chrome.zip" | awk '{print $1}')"
fi

echo "$digest" > chrome.sha256
echo "pinned chrome ${version} (${digest})"
