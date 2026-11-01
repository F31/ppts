#!/usr/bin/env bash
# 讲稿绑定预填的带配音样片（changes/animation-narration-binding tasks 5.1）。
#
# 一条命令从「讲解词」到「逐段核对」：
#   讲解词 → 配音（edge-tts，逐段合成）→ 预填绑定 → 编译计划 → 渲染 → 拼接混音 → 量元素出现时刻
#
# 为什么样片要单独跑一遍产品代码：单元测试能证明「绑定指向 withSegment」，
# 但证明不了「观众听到第 N 段时，第 N 条才出现在画面上」。后者要像素、要音频、
# 要两边的时钟是同一个——只有成片能给。
#
# 用法：
#   bash scripts/fidelity/prefill_sample.sh
#   PPTS_CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe" bash scripts/fidelity/prefill_sample.sh
#
# 依赖：Go、Node（renderer/ 已 npm i）、ffmpeg（完整版：需要 rawvideo 输出）、
#       Chrome/Chromium（Remotion 渲染）、Python 包 edge-tts（仅在重打配音时需要）。
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=${OUT:-tmp/prefill-sample}
SCRIPT=${SCRIPT:-scripts/fidelity/q7-script.json}
FID=${FID:-tmp/q7/fid}
# segments.json 与 timeline.wav 是 narrate.py 的产物；给了就复用，没给就现打。
SEGMENTS=${SEGMENTS:-tmp/q7/segments.json}
AUDIO=${AUDIO:-tmp/q7/audio/timeline.wav}
FFMPEG=${FFMPEG:-ffmpeg}
FFPROBE=${FFPROBE:-ffprobe}
PPTS_CHROME=${PPTS_CHROME:-}

# Remotion 自带的 ffmpeg 是裁剪版：没有 rawvideo 输出，check_alignment 用不了。
# 这里先探一下，省得跑到最后一步才发现量不了。
if ! "$FFMPEG" -v error -f lavfi -i color=c=black:s=16x16:d=0.04 -f rawvideo - 2>/dev/null | head -c 4 >/dev/null; then
  echo "ffmpeg 不支持 rawvideo 输出（Remotion 自带的那份就不支持）。请设置 FFMPEG 指向完整版。" >&2
  exit 1
fi

mkdir -p "$OUT"

# 1) 配音 + 页内时间轴。已经打过就不重打：同一句话重合成一次时长不一定分毫不差，
#    而这里所有判据的参照点都是那份实测时长。
if [ ! -f "$SEGMENTS" ] || [ ! -f "$AUDIO" ]; then
  echo "== 1/4 合成配音"
  python3 scripts/fidelity/narrate.py --script "$SCRIPT" --out "$(dirname "$SEGMENTS")" \
    --ffmpeg "$FFMPEG" --ffprobe "$FFPROBE"
else
  echo "== 1/4 复用配音：$SEGMENTS"
fi

# 2) 预填 → 计划。prefill 模式下轨道由 animation/bind + animdefault 生成（产品代码），
#    segments-prefill.json 里写回的是**实测绑定**，后面量像素读的就是它。
echo "== 2/4 预填绑定并编译计划"
rm -rf "$OUT/job" "$OUT/out"
go run ./scripts/fidelity/plan -in "$FID" -out "$OUT/job" -script "$SEGMENTS" -hard-cut -prefill

# 3) 渲染 + 拼接 + 混音。
#
# 必须走 cmd/renderer，不能直接调 renderer/render.mjs：**底板（plate）是在 Go 侧生成的**。
# 计划里每层只说了「从整页底图的这个框里裁一块」，底图原位置上的本体还在。不生成底板
# 直接渲，淡入的层只是在同一张图上又叠了一遍自己——像素差恒为 0，门禁会把它读成
# 「元素从没出现」。这个坑当场量出来过：直接渲的那版 18 条里 11 条 onset=None。
echo "== 3/4 渲染与封装"
PORT=${PORT:-18092}
rm -rf "$OUT/work" "$OUT/out"
mkdir -p "$OUT/work" "$OUT/out"
go run ./cmd/renderer -addr "127.0.0.1:$PORT" -workdir "$OUT/work" \
  -assets-dir "$PWD/$OUT/job" -backend remotion \
  -renderer-script "$PWD/renderer/render.mjs" -chrome "$PPTS_CHROME" \
  -version "fidelity-remotion" > "$OUT/renderer.log" 2>&1 &
RENDERER_PID=$!
trap 'kill $RENDERER_PID 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do
  curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:$PORT/healthz" && break
  sleep 1
done

JOB=$(curl -s --noproxy '*' -X POST --data-binary @"$OUT/job/plan.json" \
  "http://127.0.0.1:$PORT/v1/render" | python -c 'import sys,json;print(json.load(sys.stdin)["jobId"])')
for _ in $(seq 1 120); do
  STATE=$(curl -s --noproxy '*' "http://127.0.0.1:$PORT/v1/render/$JOB" \
    | python -c 'import sys,json;print(json.load(sys.stdin).get("state"))')
  case "$STATE" in
    succeeded) break ;;
    failed) echo "渲染失败：" >&2; curl -s --noproxy '*' "http://127.0.0.1:$PORT/v1/render/$JOB" >&2; exit 1 ;;
  esac
  sleep 3
done
cp "$OUT/work/$JOB"/segment_*.mp4 "$OUT/out/"
kill $RENDERER_PID 2>/dev/null || true

: > "$OUT/list.txt"
i=0
for f in "$OUT"/out/segment_*.mp4; do
  printf "file 'out/%s'\n" "$(basename "$f")" >> "$OUT/list.txt"
  i=$((i + 1))
done
echo "   拼接 $i 段"
"$FFMPEG" -y -loglevel error -f concat -safe 0 -i "$OUT/list.txt" -i "$AUDIO" \
  -map 0:v:0 -map 1:a:0 -c:v copy -c:a aac -b:a 128k -shortest -movflags +faststart \
  "$OUT/film_prefill_narrated.mp4"

# 4) 逐段核对（判据见 check_speech_gate.py 顶部）。
echo "== 4/4 开口门禁"
PPTS_SEG_DIR="$OUT/out" python3 scripts/fidelity/check_speech_gate.py \
  --film "$OUT/film_prefill_narrated.mp4" \
  --segments "$OUT/job/segments-prefill.json" \
  --elements "$FID/elements.json" \
  --ffmpeg "$FFMPEG" \
  --out "$OUT/speech-gate.json"

echo "== 负对照（证明这把尺子量得出提前）"
PPTS_SEG_DIR="$OUT/out" python3 scripts/fidelity/check_speech_gate.py \
  --film "$OUT/film_prefill_narrated.mp4" \
  --segments "$OUT/job/segments-prefill.json" \
  --elements "$FID/elements.json" \
  --ffmpeg "$FFMPEG" \
  --negative-control

echo
echo "成片：$OUT/film_prefill_narrated.mp4"
echo "报告：$OUT/speech-gate.json"
