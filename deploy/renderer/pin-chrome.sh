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
# 清自己的临时目录要用 `command rm` 绕开 shell 函数包装：在某些加固环境里
# `rm` 被换成了 safe-delete 包装，它不认 mktemp 给出的 Windows 风格路径
# （"embedded drive prefix is not allowed"），于是每次都残留一个 121MB 的 zip。
trap 'command rm -rf -- "$tmp"' EXIT

echo "downloading ${url}"
curl -fsSL --retry 3 --max-time 300 -o "$tmp/chrome.zip" "$url"

# 摘要从 stdin 读，不把路径作为参数传：GNU coreutils 在文件名含反斜杠/非打印字符时
# 会给摘要加前导 "\" 作为转义标记（Git Bash 下 mktemp 返回 "C:\...\Temp/tmp.x"，
# 正好触发）。那样写出来的 chrome.sha256 是 "\<hex>"，构建期 `sha256sum -c` 直接报
# 格式错误。从 stdin 读时文件名是 "-"，不会有转义。
if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum < "$tmp/chrome.zip" | awk '{print $1}')"
else
  digest="$(shasum -a 256 < "$tmp/chrome.zip" | awk '{print $1}')"
fi

# 这个文件是构建期的校验值，不是给人看的日志：格式不对必须在生成时就炸，
# 否则要等到 `docker build` 拉完 121MB 才在网络受限的构建机上失败。
if [[ ! "$digest" =~ ^[0-9a-f]{64}$ ]]; then
  echo "unexpected digest format: '${digest}'" >&2
  exit 1
fi

echo "$digest" > chrome.sha256
echo "pinned chrome ${version} (${digest})"
