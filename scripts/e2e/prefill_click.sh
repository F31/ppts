#!/usr/bin/env bash
# 预填的点击级联调（M1b 的最后一段：界面）。
#
# prefill_live.sh 验的是「服务端到模型到落盘再读回」；这条接在它后面，验
# 「人在编辑器里点一下，看到的东西对不对」。两者的分界很清楚：
#
#   prefill_live.sh   接口返回了什么
#   prefill_click.sh  浏览器上屏了什么（202 的底稿先上屏、模型结果替换、按钮不锁死）
#
# 为什么不合并成一条：点击这条依赖一个真浏览器，跑得慢、还要挑环境；
# 服务端那条在任何 CI 上都能跑。混在一起，慢的那半会让快的那半没人跑。
#
# 用法：
#   bash scripts/e2e/prefill_click.sh          # 没有现场会先跑一遍 prefill_live.sh
#   KEEP=1 bash scripts/e2e/prefill_click.sh   # 跑完保留浏览器与服务，供人工接着看
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

WORK=${WORK:-tmp/live-e2e}
CDP_PORT=${CDP_PORT:-9223}
# 模型故意慢回。异步窗口只有几十毫秒时，"底稿先上屏"根本无法观察——
# 快不是优点，这里要的是把窗口撑开。
MODEL_DELAY_MS=${MODEL_DELAY_MS:-5000}

log() { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

PY=python
command -v python >/dev/null 2>&1 || PY=python3

if [ ! -f "$WORK/run.json" ]; then
  log "没有联调现场，先跑 prefill_live.sh（约 1 分钟）"
  WORK="$WORK" bash scripts/e2e/prefill_live.sh
fi
[ -f "$WORK/run.json" ] || fail "prefill_live.sh 没有留下 $WORK/run.json"

read_json() { $PY -c "import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$WORK/run.json" "$1"; }
PPTS_PORT=$(read_json httpPort)
LLM_PORT=$(read_json llmPort)
PROJECT=$(read_json projectId)
SLIDE=$(read_json slideId)
SLIDE_INDEX=$(read_json slideIndex)

log "0/4 复用现场：项目 $PROJECT / 幻灯片 $SLIDE"

# 二进制里嵌的是 web/dist。若改了前端却没重建，这里点的是旧界面——
# 断言全绿也证明不了什么。宁可停下来说清楚，也不要跑一份过期的产物。
if [ -f web/dist/index.html ]; then
  stale=$(find web/src -newer web/dist/index.html -name '*.ts' -o -newer web/dist/index.html -name '*.tsx' 2>/dev/null | head -1)
  [ -z "$stale" ] || fail "web/dist 比源码旧（$stale）：先 cd web && npm run build"
fi
go build -o "$WORK/ppts.exe" ./cmd/ppts

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

set -a; . "$WORK/env.sh"; set +a

log "1/4 起假模型（延时 ${MODEL_DELAY_MS}ms）与 server/worker"
FAKE_LLM_DELAY_MS=$MODEL_DELAY_MS $PY scripts/e2e/fake_llm.py "$LLM_PORT" "$WORK/fake-llm.jsonl" &
PIDS+=($!)

for i in $(seq 1 40); do
  curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:$LLM_PORT/" && break
  sleep 1
done

"$WORK/ppts.exe" server > "$WORK/server-click.log" 2>&1 &
PIDS+=($!)
for i in $(seq 1 40); do
  body=$(curl -s --noproxy '*' -m 2 "http://127.0.0.1:$PPTS_PORT/healthz" 2>/dev/null || true)
  case "$body" in *'"status"'*) break ;; esac
  sleep 1
done

"$WORK/ppts.exe" worker > "$WORK/worker-click.log" 2>&1 &
PIDS+=($!)
sleep 2

# 改内容指纹这一步搬进了 prefill_click.mjs：两个场景各自要一次新的内容，
# 放在这里只能改一次，第二个场景会撞上第一个场景的缓存。

log "2/4 起无头浏览器"
CHROME=${CHROME:-}
if [ -z "$CHROME" ]; then
  for p in \
    "/c/Program Files/Google/Chrome/Application/chrome.exe" \
    "/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe" \
    "$(command -v google-chrome 2>/dev/null || true)" \
    "$(command -v chromium 2>/dev/null || true)"; do
    [ -n "$p" ] && [ -x "$p" ] && CHROME=$p && break
  done
fi
[ -n "$CHROME" ] || fail "没找到 Chrome/Edge，可用 CHROME=/path/to/chrome 指定"

# Chrome 是原生 Windows 程序：相对路径的 --user-data-dir 会被它解析到安装目录（只读），
# 表现是进程一闪而过、日志为空、调试端口永远不监听。给绝对路径，Windows 上再转成反斜杠。
#
# 用 mktemp 新建空目录而不是先 rm -rf 旧的：Chrome 的 profile 里有上千个文件，
# 每跑一次删一次既慢又容易撞上删除保护。让它留在系统临时目录里由系统回收。
CHROME_PROFILE=$(mktemp -d 2>/dev/null || echo "$WORK/chrome-$$")
if command -v cygpath >/dev/null 2>&1; then
  CHROME_PROFILE=$(cygpath -w "$CHROME_PROFILE")
fi
"$CHROME" --headless=new --remote-debugging-port="$CDP_PORT" \
  --user-data-dir="$CHROME_PROFILE" --no-first-run --no-default-browser-check \
  --disable-gpu --disable-extensions --disable-background-networking \
  --enable-logging=stderr --v=0 \
  about:blank > "$WORK/chrome.log" 2>&1 &
PIDS+=($!)

for i in $(seq 1 30); do
  curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version" && break
  sleep 1
done
curl -s --noproxy '*' -m 2 -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version" \
  || { tail -20 "$WORK/chrome.log"; fail "无头浏览器没起来"; }

log "3/4 点下去，看界面"
node scripts/e2e/prefill_click.mjs \
  --base="http://127.0.0.1:$PPTS_PORT" \
  --project="$PROJECT" --slide="$SLIDE" --slide-index="$SLIDE_INDEX" \
  --cdp="$CDP_PORT" --model-delay="$MODEL_DELAY_MS" \
  || fail "点击级断言未通过"

if [ "${KEEP:-0}" = "1" ]; then
  log "KEEP=1：服务与浏览器保持运行，PID 见下"
  echo "server/worker/chrome 仍在跑；Ctrl-C 结束"
  wait
fi

log "4/4 通过"
