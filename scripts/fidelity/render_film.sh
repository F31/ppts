#!/usr/bin/env bash
# Q7 带配音样片的第 3 步：渲染 → 拼接 → 混音，一次跑完。
#
# scripts/fidelity/README.md 的「五步」里，这一步原先指向 tmp/q7/run_all.sh。那个文件
# 从来没有入库，于是从干净 checkout 复现样片会断在第 3 步：前两步（narrate.py 出配音与
# 逐词边界、plan 编译 withSegment 绑定）都在仓库里，缺的正是这一段。本脚本补上它，
# 并把当时只存在于操作者脑子里的三件事写死：
#
#   1) 片段按**页序**拼，不靠文件名排序。文件名虽然是 segment_%03d 补零，但按目录
#      glob 的结果取决于文件系统，而拼错顺序不会报错——出来是一条页序错乱的片子。
#   2) 视频**不重编码**（-c:v copy）。fidelity 的保真阈值（平均差 ≤2.0、墨量比 ≥0.8）
#      是按「H.264 编码一次」标定的；拼接再编一次会把余量吃掉，测出来的差异混进了
#      第二次编码的噪声。
#   3) 音轨铺满全片，并**显式报 Δ**。每页帧数是 round(durationMs×fps/1000)，取整误差
#      累计几毫秒，视频因此可能比音轨长或短；`-shortest` 会安静地剪掉多出来的那几毫秒。
#      差值小可以接受，但必须看得见，否则「片尾少半句」永远查不出来。
#
# 两种引擎：
#   --engine local   本机起 cmd/renderer（Remotion + 本机 Chrome），提交、轮询、取片。
#                    一条命令端到端，不需要容器，也不需要常驻服务。
#   --engine http    连一个已经在跑的渲染服务（容器或别的机器）。
#
# **不要直调 renderer/render.mjs。** README「两个坑」里第一条讲的就是这个：底板
# （plate）由 Go 侧的 RemotionBackend.stagePages 拆出来——把每个动画元素从底图上抠掉，
# 它的层才有可淡入的东西。缺了这一步，淡入的层只是把整页底图又叠了一遍，像素差恒为 0，
# 成片看起来「所有元素一开始就在」。第一版 Q7 就这么错过（18 条里 11 条 onset=None），
# 这一版的 native 分支又错了一次：片子照样出得来，只是各项判据集体读成「元素从没出现」。
#
# 例：
#   bash scripts/fidelity/render_film.sh                        # 本机起服务跑一遍
#   bash scripts/fidelity/render_film.sh --engine http --renderer-url http://127.0.0.1:8090
#   bash scripts/fidelity/render_film.sh --stage compose         # 只重做拼接与混音

set -euo pipefail

ENGINE=local
STAGE=all
JOB=tmp/q7/job
SEGMENTS=tmp/q7/segments.json
SEGDIR=
OUT=tmp/q7/film_M_narrated.mp4
RENDERER_URL=http://127.0.0.1:8090
ASSETS=
CHROME=${PPTS_CHROME:-}
FFMPEG_BIN=${FFMPEG:-ffmpeg}
POLL_TIMEOUT=${POLL_TIMEOUT:-900}
SRV_PID=
WORK_DIR=

die() { printf 'render_film: %s\n' "$*" >&2; exit 1; }
log() { printf '[film] %s\n' "$*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --engine)       ENGINE=$2; shift 2 ;;
    --stage)        STAGE=$2; shift 2 ;;
    --job)          JOB=$2; shift 2 ;;
    --segments)     SEGMENTS=$2; shift 2 ;;
    --segdir)       SEGDIR=$2; shift 2 ;;
    --out)          OUT=$2; shift 2 ;;
    --renderer-url) RENDERER_URL=$2; shift 2 ;;
    --assets)       ASSETS=$2; shift 2 ;;
    --chrome)       CHROME=$2; shift 2 ;;
    --ffmpeg)       FFMPEG_BIN=$2; shift 2 ;;
    -h|--help)      sed -n '1,40p' "$0"; exit 0 ;;
    *)              die "未知参数 $1" ;;
  esac
done

case "$ENGINE" in local|http) ;; *) die "--engine 只能是 local 或 http" ;; esac
case "$STAGE" in all|render|compose) ;; *) die "--stage 只能是 all / render / compose" ;; esac

PLAN="$JOB/plan.json"
[ -f "$PLAN" ] || die "找不到计划 $PLAN（先跑 scripts/fidelity/plan）"
[ -f "$SEGMENTS" ] || die "找不到 $SEGMENTS（先跑 scripts/fidelity/narrate.py）"

# 一律转绝对路径再往下走：脚本要在 renderer/ 子目录里渲、要经 wsl 传给 ffmpeg，
# 两处都会把相对路径解析到别的基准上。转一次，后面不再猜。
abspath() {
  case "$1" in
    /*|[A-Za-z]:/*) printf '%s' "$1" ;;
    *)              printf '%s/%s' "$PWD" "$1" ;;
  esac
}
PLAN=$(abspath "$PLAN")
SEGMENTS=$(abspath "$SEGMENTS")

# ffmpeg 定位。Windows 侧的 Git Bash 通常没有 ffmpeg，而 WSL 里一般有；此时把调用
# 透传给 wsl，并把路径改写成 /mnt/<盘符>/…。用户显式给了 --ffmpeg 或 $FFMPEG 就不再猜。
if [ "$FFMPEG_BIN" = "ffmpeg" ] && ! command -v ffmpeg >/dev/null 2>&1; then
  if command -v wsl >/dev/null 2>&1; then
    FFMPEG_BIN=wsl
    WSL_FFMPEG=1
  else
    die "找不到 ffmpeg：装一个，或用 --ffmpeg 指路径"
  fi
else
  WSL_FFMPEG=0
fi

# to_wsl 只在 wsl 包装下才改写路径。Git Bash 的 /e/projects/x 与 E:/projects/x
# 都要变成 /mnt/e/projects/x；已经是 /mnt/... 的原样返回。
to_wsl() {
  local p=$1
  # Windows 侧的 python 用 `\` 拼路径，与 MSYS 的 `/e/...` 混在一起；先归一成 `/`，
  # 否则下面两个 case 都匹配不上，路径会原样传给 WSL（然后指向一个不存在的位置）。
  p=${p//\\//}
  [ "$WSL_FFMPEG" = "1" ] || { printf '%s' "$p"; return; }
  case "$p" in
    [A-Za-z]:/*) printf '/mnt/%s%s' "$(printf '%s' "${p:0:1}" | tr 'A-Z' 'a-z')" "${p:2}" ;;
    /[A-Za-z]/*) printf '/mnt/%s%s' "$(printf '%s' "${p:1:1}" | tr 'A-Z' 'a-z')" "${p:2}" ;;
    *)           printf '%s' "$p" ;;
  esac
}

# MSYS_NO_PATHCONV=1 不能省：Git Bash 会把命令行里像 POSIX 路径的参数改写成 Windows
# 路径，于是 ffmpeg 收到的是 `C:Users\...`（反斜杠还被 shell 吃掉），报
# `Protocol not found`——看上去像 ffmpeg 的错，其实是 shell 在改参数。
ffmpeg() {
  if [ "$WSL_FFMPEG" = "1" ]; then MSYS_NO_PATHCONV=1 wsl -- ffmpeg "$@"
  else "$FFMPEG_BIN" "$@"; fi
}
ffprobe_dur() {
  if [ "$WSL_FFMPEG" = "1" ]; then
    MSYS_NO_PATHCONV=1 wsl -- ffprobe -v error -show_entries format=duration -of csv=p=0 "$(to_wsl "$1")"
  else
    ffprobe -v error -show_entries format=duration -of csv=p=0 "$1"
  fi
}
ffprobe_frames() {
  if [ "$WSL_FFMPEG" = "1" ]; then
    MSYS_NO_PATHCONV=1 wsl -- ffprobe -v error -select_streams v:0 -show_entries stream=nb_frames -of csv=p=0 "$(to_wsl "$1")"
  else
    ffprobe -v error -select_streams v:0 -show_entries stream=nb_frames -of csv=p=0 "$1"
  fi
}

# 页数与每页应有帧数从计划里读，不另存一份清单——两份清单一定会漂。
read -r PAGES FPS < <(python3 - "$PLAN" <<'PY'
import json,sys
p=json.load(open(sys.argv[1],encoding='utf-8'))
print(len(p['pages']), p.get('fps') or 30)
PY
)
[ "$PAGES" -gt 0 ] || die "计划里没有页"

# 片段目录默认与 segments.json 同级（tmp/q7/out）——这正是 check_alignment /
# check_appearance 通过 PPTS_SEG_DIR 找片段的默认位置（见 _segtime.py）。放在
# job 目录下会让 verify 拿不到分片，或者更糟：拿到上一次的旧分片去量这一次的成片。
OUTDIR=${SEGDIR:-$(dirname "$SEGMENTS")/out}
OUT=$(abspath "$OUT")
OUTDIR=$(abspath "$OUTDIR")
mkdir -p "$(dirname "$OUT")"

# ---------------------------------------------------------------- 1) 渲染
if [ "$STAGE" = all ] || [ "$STAGE" = render ]; then
  if [ "$ENGINE" = local ]; then
    [ -d renderer/node_modules ] || die "renderer/node_modules 不存在（先 cd renderer && npm ci）"
    if [ -z "$CHROME" ]; then
      for c in "/c/Program Files/Google/Chrome/Application/chrome.exe" \
               "/c/Program Files (x86)/Google/Chrome/Application/chrome.exe" \
               "/usr/bin/google-chrome" "/usr/bin/chromium"; do
        [ -x "$c" ] && { CHROME=$c; break; }
      done
    fi
    [ -n "$CHROME" ] || die "找不到 Chrome：设 PPTS_CHROME 或用 --chrome 指路径"

    # 资产根：plan 里的 baseImage 是相对路径（pages/p000.png），stagePages 要按它
    # 读到原图、抠掉元素、再生出 plate 与 layer 位图。默认取 job 的兄弟目录 fid——
    # 也就是本仓库的布局（tmp/q7/job 与 tmp/q7/fid）。
    [ -n "$ASSETS" ] || ASSETS=$(dirname "$JOB")/fid
    [ -d "$ASSETS" ] || die "找不到资产根 $ASSETS（plan 的 baseImage 相对它解析）"
    ASSETS=$(cd "$ASSETS" && pwd)

    # 服务启动时会 RemoveAll 工作目录（重试语义要求上一轮半成品消失），所以
    # **绝不能**把 plan 所在目录当 workdir —— 那样 plan.json 会被当成残留删掉。
    WORK_DIR=$(dirname "$OUT")/work
    rm -rf "$WORK_DIR" && mkdir -p "$WORK_DIR"
    BIN=$(dirname "$OUT")/ppts-renderer
    case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) BIN="$BIN.exe" ;; esac
    log "构建渲染服务 → $BIN"
    go build -o "$BIN" ./cmd/renderer
    PORT=$(python3 -c 'import socket
s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    RENDERER_URL="http://127.0.0.1:$PORT"
    SRV_LOG=$(dirname "$OUT")/renderer.log
    log "起渲染服务 $RENDERER_URL（日志 $SRV_LOG）"
    export PPTS_CHROME="$CHROME"
    "$BIN" -addr "127.0.0.1:$PORT" -workdir "$WORK_DIR" -assets-dir "$ASSETS" \
      -backend remotion -renderer-script renderer/render.mjs -version "fidelity@local" \
      > "$SRV_LOG" 2>&1 &
    SRV_PID=$!

    # 等 healthz。进程起得快，但后端要在第一页才开始拉 Chrome，所以这里只等 HTTP。
    UP=0
    for _ in $(seq 1 120); do
      if curl -sf --max-time 2 "$RENDERER_URL/healthz" >/dev/null 2>&1; then UP=1; break; fi
      kill -0 "$SRV_PID" 2>/dev/null || break
      sleep 0.5
    done
    if [ "$UP" != 1 ]; then
      tail -20 "$SRV_LOG" >&2 || true
      die "渲染服务没起来（见 $SRV_LOG）"
    fi
  fi

    log "渲染（http，$RENDERER_URL）→ $OUTDIR"
    mkdir -p "$OUTDIR"
    JOB_ID=$(python3 - "$RENDERER_URL" "$PLAN" <<'PY'
import json,sys,urllib.request
url,plan=sys.argv[1],sys.argv[2]
body=open(plan,'rb').read()
req=urllib.request.Request(url.rstrip('/')+'/v1/render',data=body,
                           headers={'Content-Type':'application/json'},method='POST')
with urllib.request.urlopen(req,timeout=60) as r:
    print(json.load(r)['jobId'])
PY
)
    [ -n "$JOB_ID" ] || die "提交渲染失败：没有 jobId"
    log "jobId=$JOB_ID"
    python3 - "$RENDERER_URL" "$JOB_ID" "$OUTDIR" "$PAGES" "$POLL_TIMEOUT" <<'PY'
import json,os,sys,time,urllib.request
base,jid,outdir,pages,timeout=sys.argv[1],sys.argv[2],sys.argv[3],int(sys.argv[4]),int(sys.argv[5])
base=base.rstrip('/')
deadline=time.time()+timeout
last=None
while True:
    with urllib.request.urlopen(f'{base}/v1/render/{jid}',timeout=60) as r:
        st=json.load(r)
    state=st.get('state')
    prog=st.get('progress') or {}
    line=f"{state} {prog.get('done','?')}/{prog.get('total','?')}"
    if line!=last:
        print('[render]',line,flush=True); last=line
    if state=='succeeded':
        break
    if state in ('failed','canceled'):
        raise SystemExit(f"渲染失败：{st.get('error') or state}")
    if time.time()>deadline:
        raise SystemExit(f"渲染超时（{timeout}s），停在 {state}")
    time.sleep(1.0)
if st.get('degraded'):
    print('[render] 警告：服务报了降级', st.get('degradedPages') or '', flush=True)
# 逐页下载，显式比对 Content-Length：截断的分段会被拼接器当成好片，
# 最后表现成「某一页黑屏」，比在这里报错难查得多。
for i in range(pages):
    name=f'segment_{i:03d}.mp4'
    req=urllib.request.Request(f'{base}/v1/render/{jid}/artifacts/{name}')
    with urllib.request.urlopen(req,timeout=120) as r:
        data=r.read()
        want=r.headers.get('Content-Length')
    if want and int(want)!=len(data):
        raise SystemExit(f'{name} 截断：Content-Length={want} 实收 {len(data)}')
    with open(os.path.join(outdir,name),'wb') as fh:
        fh.write(data)
    print(f'[render] 下载 {name} {len(data)} 字节',flush=True)
PY
fi

[ "$STAGE" = compose ] || [ "$STAGE" = all ] || { log "只跑渲染，结束"; exit 0; }

# ------------------------------------------------------- 2) 校验片段并按页序拼接
MISSING=0
for ((i=0; i<PAGES; i++)); do
  seg=$(printf '%s/segment_%03d.mp4' "$OUTDIR" "$i")
  [ -f "$seg" ] || { printf 'render_film: 缺片段 %s\n' "$seg" >&2; MISSING=1; }
done
[ "$MISSING" = 0 ] || die "片段不全（计划 $PAGES 页），不做拼接"

# 帧数对不上就是音画错位的源头，必须在拼接前拦下。
i=0
while [ "$i" -lt "$PAGES" ]; do
  seg=$(printf '%s/segment_%03d.mp4' "$OUTDIR" "$i")
  want=$(python3 - "$PLAN" "$i" "$FPS" <<'PY'
import json,sys
p=json.load(open(sys.argv[1],encoding='utf-8'))
print(max(1, round(p['pages'][int(sys.argv[2])]['durationMs']*int(sys.argv[3])/1000)))
PY
)
  got=$(ffprobe_frames "$seg" | tr -d '\r' | head -1)
  if [ -n "$got" ] && [ "$got" != "N/A" ] && [ "$got" != "$want" ]; then
    die "第 $i 页帧数不符：计划 ${want}，片段 ${got}"
  fi
  i=$((i+1))
done

# concat 清单写在**成片旁边**，不用 mktemp：Git Bash 的 /tmp 是 Windows 的临时目录，
# WSL 里的 ffmpeg 根本看不见它（错误会表现成 "Protocol not found"，与真实原因无关）。
LIST="$(dirname "$OUT")/concat.txt"
cleanup() {
  # 本机起的渲染服务必须收掉：它占着一份 Chrome 和 workdir，留着会让下一次
  # 「端口猜错」或「目录被上一个进程清空」这类问题看起来像新 bug。
  if [ -n "${SRV_PID:-}" ]; then kill "$SRV_PID" 2>/dev/null || true; fi
  rm -f "$LIST"
}
trap cleanup EXIT
for ((i=0; i<PAGES; i++)); do
  printf "file '%s'\n" "$(to_wsl "$(printf '%s/segment_%03d.mp4' "$OUTDIR" "$i")")" >> "$LIST"
done

CONCAT=$(dirname "$OUT")/video_only.mp4
log "拼接 $PAGES 页 → $CONCAT"
ffmpeg -v error -f concat -safe 0 -i "$(to_wsl "$LIST")" -c copy -y "$(to_wsl "$CONCAT")"

# ------------------------------------------------------------------- 3) 混音
# 音轨路径存在 segments.json 里（相对 segments.json 所在目录），不硬编码。
# 只让 python 取**相对名**，拼接交给 bash：Windows 的 python 会用 `\` 拼绝对路径，
# 得到 `/e/projects/...\audio/timeline.wav` 这种两边都不认的混合形式。
AUDIO_REL=$(python3 - "$SEGMENTS" <<'PY'
import json,sys
print(json.load(open(sys.argv[1],encoding='utf-8')).get('timeline') or 'audio/timeline.wav')
PY
)
AUDIO=$(abspath "$(dirname "$SEGMENTS")/$AUDIO_REL")
[ -f "$AUDIO" ] || die "找不到音轨 $AUDIO"

log "混音 $AUDIO → $OUT"
ffmpeg -v error -i "$(to_wsl "$CONCAT")" -i "$(to_wsl "$AUDIO")" \
  -c:v copy -c:a aac -b:a 128k -shortest -y "$(to_wsl "$OUT")"

VD=$(ffprobe_dur "$CONCAT" | tr -d '\r' | head -1)
AD=$(ffprobe_dur "$AUDIO" | tr -d '\r' | head -1)
OD=$(ffprobe_dur "$OUT" | tr -d '\r' | head -1)
python3 - "$VD" "$AD" "$OD" "$CONCAT" "$OUT" <<'PY'
import sys
v,a,o=float(sys.argv[1]),float(sys.argv[2]),float(sys.argv[3])
d=(v-a)*1000
print(f'[film] 视频 {v:.3f}s / 音轨 {a:.3f}s / 成片 {o:.3f}s（Δ{v-a:+.3f}s）')
print(f'[film] 产物：{sys.argv[4]}（无声轨）→ {sys.argv[5]}')
if abs(d)>100:
    print(f'[film] 警告：视频与音轨相差 {d:.0f}ms，-shortest 已按短的一边截断；'
          f'先确认 plan 的 page durationMs 是否等于配音实测值', file=sys.stderr)
PY
