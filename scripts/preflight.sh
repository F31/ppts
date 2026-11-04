#!/usr/bin/env bash
# 提交前的本地预检：只跑「本地能跑、且曾经在 CI 红过」的那几条。
#
# 为什么需要它：这两类失败都是「本地一切正常、CI 一跑就红」，而且都不是逻辑错误：
#
#   1. gofmt。本地 Windows 检出是 CRLF，`gofmt -l .` 会把一大堆没问题的文件也列出来，
#      真信号被噪声盖住，于是习惯性跳过这一步。CI 在 LF 检出上跑，一跑就红——而且
#      lint 是其余多数作业的 needs，它一红，后面全部 skipped，看上去像「全线崩溃」。
#   2. 分享播放器产物。`internal/share/assets/app.js` 是入库的构建产物（go:embed 依赖），
#      改了 `web/src/sharePlayer/**` 而没跑 `npm run build:share`，CI 的 drift check 就红。
#
# 这两条都不需要成片、不需要浏览器、不需要数据库，几十秒能跑完，没有理由留到 CI 去发现。
#
# 用法：
#   bash scripts/preflight.sh          # 快检（gofmt / vet / 产物同步 / 判据自检）
#   bash scripts/preflight.sh --full   # 再加 go test ./...（慢，几分钟）
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2

FULL=0
[ "${1:-}" = "--full" ] && FULL=1

fail=0
note() { printf '%s\n' "$*"; }
bad()  { printf '  ✗ %s\n' "$*"; fail=1; }
ok()   { printf '  ✓ %s\n' "$*"; }

# ---------------------------------------------------------------- 1. gofmt
# 判据与 CI 等价：比较 gofmt 输出与文件内容，但先把两边的 CR 去掉。
# CI 是 LF 检出，直接 `gofmt -l .` 就够；本地必须归一，否则满屏 CRLF 噪声。
note "[1/4] gofmt（忽略换行风格，判据与 CI 等价）"
stale=()
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if ! diff -q <(gofmt "$f" 2>/dev/null | tr -d '\r') <(tr -d '\r' <"$f") >/dev/null 2>&1; then
    stale+=("$f")
  fi
done < <(gofmt -l . 2>/dev/null)
if [ ${#stale[@]} -eq 0 ]; then
  ok "全部已格式化"
else
  for f in "${stale[@]}"; do bad "需要 gofmt -w：$f"; done
  note "    只列「去掉 CR 后仍不一致」的文件——纯 CRLF 的不会出现在这里。"
fi

# ---------------------------------------------------------------- 2. go vet
note "[2/4] go vet ./..."
if out=$(go vet ./... 2>&1); then
  ok "通过"
else
  bad "go vet 失败"
  printf '%s\n' "$out" | tail -20 | sed 's/^/    /'
fi

# ---------------------------------------------------------------- 3. 播放器产物同步
# app.js / app.css 入库，是为了 go:embed 能在没有 Node 的环境里打包。源码改了而产物没改，
# 用户下载到的成品包就是旧播放器，而本地开发一切正常。
note "[3/4] 分享播放器产物是否与源码同步"
if [ -d web/node_modules/vite ]; then
  # 判据是「重新构建会不会改写产物」，不是「产物与 HEAD 有没有差」。
  # 后者会把「已经重新构建、只是还没提交」误报成漂移——预检恰恰是在提交前跑的，
  # 那种误报会逼着人先提交再看，等于把判据绕过去了。
  before=$(cat internal/share/assets/app.js internal/share/assets/app.css 2>/dev/null | sha256sum | cut -d' ' -f1)
  if ( cd web && npm run build:share >/tmp/preflight-share.log 2>&1 ); then
    after=$(cat internal/share/assets/app.js internal/share/assets/app.css 2>/dev/null | sha256sum | cut -d' ' -f1)
    if [ "$before" = "$after" ]; then
      ok "无漂移"
    else
      bad "产物已过期：重新构建改写了 internal/share/assets（源码改了没跑 build:share）"
      git diff --stat -- internal/share/assets | sed 's/^/    /'
      note "    提交前把这两份产物一起带上。"
    fi
  else
    bad "npm run build:share 失败"
    tail -15 /tmp/preflight-share.log | sed 's/^/    /'
  fi
else
  note "  - 跳过：web/node_modules 未安装（先 cd web && npm ci）"
fi

# ---------------------------------------------------------------- 4. 判据自检
note "[4/4] fidelity 判据自检"
py=""
for c in python3 python; do command -v "$c" >/dev/null 2>&1 && { py=$c; break; }; done
if [ -z "$py" ]; then
  note "  - 跳过：没有 python"
else
  for pair in "verify.py:51" "split_chart_bars.py:8" "mask_preview.py:4" \
              "check_mask.py:17" "extract_pptx_elements.py:5"; do
    script=${pair%%:*}; want=${pair##*:}
    line=$("$py" "scripts/fidelity/$script" --self-test 2>&1 | tail -1)
    if printf '%s' "$line" | grep -q "$want/$want"; then
      ok "$script  $line"
    else
      bad "$script  $line（应 $want/$want）"
    fi
  done
fi

# ---------------------------------------------------------------- 可选：全量测试
if [ "$FULL" = "1" ]; then
  note "[+] go test ./..."
  if out=$(go test ./... 2>&1); then
    ok "通过"
  else
    bad "go test 失败"
    printf '%s\n' "$out" | grep -v '^ok\|no test files' | tail -25 | sed 's/^/    /'
  fi
fi

echo
if [ "$fail" = "0" ]; then
  echo "预检通过。跑 --full 可再加上全量测试。"
else
  echo "预检未通过：上面带 ✗ 的项就是 CI 会红的地方。"
fi
exit "$fail"
