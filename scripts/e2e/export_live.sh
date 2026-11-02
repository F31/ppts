#!/usr/bin/env bash
# 真实导出联调：把 api / worker / renderer 三个真进程跑起来，从一份 PPTX 一路
# 走到一个能播放的 MP4，再用 ffprobe 与抽帧核对成品。
#
# 为什么要有这一条：单元测试替掉的是存储、编码器、渲染器；后端 e2e 只导出 SRT，
# 从没让真正的 soffice → pdftoppm → ffmpeg → libass 这条链跑过。于是"CI 全绿"
# 对成品能否播放零信息——镜像漏文件、字体缺失、字幕滤镜没编进去，全都活得下来。
#
# 这是**真机**脚本，不是 CI 门禁：它需要 docker、可写临时目录，首次还要构建
# worker 镜像（内含 LibreOffice / poppler / ffmpeg / 中文字体，约 1.2GB）。
#
# 环境前提：
#   - Linux 或有 docker 的 WSL；Windows 原生 Git Bash 跑不了（没有 soffice/ffmpeg，
#     且 SQLite 文件不能跨 Windows/WSL 共享）。
#   - 渲染镜像：默认 ppts/renderer:verify（见 deploy/renderer/README.md）。
#   - worker 镜像：先 `docker build -f Dockerfile.worker -t ppts-worker:local .`
#     或让本脚本用 IMAGE 指定已有镜像。
#
# 用法：
#   bash scripts/e2e/export_live.sh
#   PORT=38081 bash scripts/e2e/export_live.sh
#   SLIDE_LIMIT=2 ANIMATE=1 bash scripts/e2e/export_live.sh   # 动画通道（只取前 2 页，短片）
#   KEEP=1 bash scripts/e2e/export_live.sh     # 保留容器与工作目录排障
#
# 产物：$WORK/out/artifact.mp4、frame-*.png、ffprobe.json、时间轴与讲稿快照。
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"

WORK=${WORK:-/tmp/ppts-export-live}
PORT=${PORT:-38081}
# worker 的指标端点是**另一个监听**（进程隔离：worker 侧的 expvar 只有本进程能读）。
# 之前这里只读 api 的 /metrics，于是 ppts_tts_fake_provider_active 永远缺失——
# 不看这个端口，"假 TTS 是否真的在跑"就只能靠猜。
WMETRICS=${WMETRICS:-38091}
NET=${NET:-ppts-export-e2e}
API_NAME=ppts-e2e-api
WORKER_NAME=ppts-e2e-worker
RENDER_NAME=ppts-e2e-renderer
IMAGE=${IMAGE:-ppts-worker:local}
RENDER_IMAGE=${RENDER_IMAGE:-ppts/renderer:verify}
DECK=${DECK:-internal/animation/animdefault/testdata/sample-customer-deck.pptx}
# 页面图与时间轴规模都不小，14 页 1080p 的片子约 2–3 分钟；留足余量。
JOB_TIMEOUT=${JOB_TIMEOUT:-600}
# SLIDE_LIMIT>0 时只对前 N 页生成讲稿/配音/导出：动画通道要把每页真的渲一遍，
# 14 页 × 45s 的片子在本机要十几分钟，验通路没必要跑满。
SLIDE_LIMIT=${SLIDE_LIMIT:-0}
# ANIMATE=1 时走动画通道：先用默认规则（不经模型）为每页生成规格并提交，再导出。
# 这一段的用途是证明渲染服务真的被导出链路调用，而不是只在 selftest 里活着。
ANIMATE=${ANIMATE:-0}
PRESET=${PRESET:-business-calm}

log() { printf '\n== %s\n' "$*"; }
fail() {
  printf '\nFAIL: %s\n' "$*" >&2
  for c in "$API_NAME" "$WORKER_NAME" "$RENDER_NAME"; do
    printf -- '--- %s (last 40 lines)\n' "$c" >&2
    docker logs --tail 40 "$c" 2>&1 >&2 || true
  done
  exit 1
}

PY=python3
command -v "$PY" >/dev/null 2>&1 || PY=python

json() { "$PY" -c "import json,sys;d=json.load(sys.stdin);$1"; }

post() { # post <path> <json> [extra curl args...]
  local path=$1 body=$2
  shift 2
  curl -sS --noproxy '*' -X POST "http://127.0.0.1:$PORT$path" \
    -H 'Content-Type: application/json' "$@" -d "$body"
}

wait_http() {
  local url=$1 i
  for i in $(seq 1 60); do
    curl -sS --noproxy '*' -m 2 "$url" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

wait_job() { # wait_job <jobId> <秒上限> [标签]
  local id=$1 limit=${2:-$JOB_TIMEOUT} label=${3:-job} i state
  for i in $(seq 1 "$limit"); do
    state=$(post /ppts.v1.JobService/Get "{\"jobId\":\"$id\"}" | json 'print(d.get("state",""))')
    case "$state" in
      JOB_STATE_SUCCEEDED) return 0 ;;
      JOB_STATE_FAILED | JOB_STATE_CANCELED)
        printf '%s 落到 %s\n' "$label" "$state" >&2
        post /ppts.v1.JobService/Get "{\"jobId\":\"$id\"}" >&2
        return 1
        ;;
    esac
    sleep 1
  done
  printf '%s 在 %ss 内没到成功态（最后状态 %s）\n' "$label" "$limit" "$state" >&2
  return 1
}

cleanup() {
  if [ "${KEEP:-0}" = "1" ]; then
    printf '\nKEEP=1：容器与 %s 都留着\n' "$WORK"
    return
  fi
  docker rm -f "$API_NAME" "$WORKER_NAME" "$RENDER_NAME" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "0/8 前置检查"
docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "缺镜像 $IMAGE：先 docker build -f Dockerfile.worker -t $IMAGE ."
docker image inspect "$RENDER_IMAGE" >/dev/null 2>&1 || fail "缺渲染镜像 $RENDER_IMAGE：见 deploy/renderer/README.md"
[ -f "$DECK" ] || fail "样本 deck 不存在：$DECK"
docker rm -f "$API_NAME" "$WORKER_NAME" "$RENDER_NAME" >/dev/null 2>&1 || true
docker network rm "$NET" >/dev/null 2>&1 || true
docker network create "$NET" >/dev/null

# 清工作目录不能直接 rm：容器以 UID 10001 写出的子目录是 0755/10001，
# 宿主机用户（通常 1000）连父目录都进得去却删不掉文件里的目录项。
# 用一个临时的 root 容器来删，才是唯一不依赖 sudo 的做法。
if [ -d "$WORK" ]; then
  docker run --rm --user root -v "$WORK":/w --entrypoint sh "$IMAGE" \
    -c 'rm -rf /w/* /w/.[!.]* 2>/dev/null || true' >/dev/null
fi
rm -rf "$WORK"
mkdir -p "$WORK"/{db,objects,assets,out}
# 镜像以 UID 10001 运行，bind mount 的宿主目录默认 root:root 0755。
chmod -R 0777 "$WORK"

SECRET=$("$PY" -c 'import base64,os;print(base64.b64encode(os.urandom(32)).decode())')
METRICS_TOKEN=$("$PY" -c 'import base64,os;print(base64.b64encode(os.urandom(24)).decode())')

# 三个进程共用的环境。api 与 worker 必须看到同一份 DB、对象目录与资产目录；
# 少任何一条，产物就会"看着成功、其实缺内容"。
COMMON_ENV=(
  -e PPTS_DB_DRIVER=sqlite
  -e "PPTS_DATABASE_URL=/data/db/live.db"
  -e PPTS_OBJECT_BACKEND=local
  -e PPTS_OBJECT_ROOT=/data/objects
  -e "PPTS_OBJECT_SECRET=$SECRET"
  -e PPTS_AUTH_DEV_HEADERS=true
  -e PPTS_TTS_PROVIDER=fake
  -e PPTS_ANIMATION_ENABLED=true
  -e "PPTS_RENDERER_URL=http://$RENDER_NAME:8090"
  -e PPTS_ANIMATION_ASSET_DIR=/data/assets
  # 渲染依赖缺失时必须失败，而不是静默产出缺页的片子。这条是本次验证的前提。
  -e PPTS_REQUIRE_RENDERER=true
  -e "PPTS_METRICS_TOKEN=$METRICS_TOKEN"
  -e TZ=Asia/Shanghai
)

log "1/8 起渲染服务（$RENDER_IMAGE）"
# 与 deploy/renderer/compose.yml 同构：只读 rootfs + tmpfs 工作目录 + no-new-privileges。
# 这里用自定义 bridge 而不是 compose 里的 network_mode:none——三个容器要在同一张网上
# 互相解析；隔离的是"渲染器不出网"，由下面的 --network 与镜像本身保证，不靠 none。
docker run -d --name "$RENDER_NAME" --network "$NET" \
  --read-only \
  --tmpfs /work:size=2g,mode=1777 \
  --tmpfs /tmp:size=512m,mode=1777 \
  --security-opt no-new-privileges:true \
  -v "$WORK/assets":/assets:ro \
  "$RENDER_IMAGE" \
  -addr :8090 -backend remotion -workdir /work \
  -renderer-script /app/render.mjs -assets-dir /assets \
  -concurrency 1 -page-concurrency 4 -version e2e >/dev/null

for i in $(seq 1 60); do
  docker exec "$RENDER_NAME" curl -fsS -o /dev/null http://127.0.0.1:8090/healthz 2>/dev/null && break
  sleep 1
done
docker exec "$RENDER_NAME" curl -fsS -o /dev/null http://127.0.0.1:8090/healthz 2>/dev/null \
  || fail "渲染服务没起来"

log "2/8 起 api 与 worker"
docker run -d --name "$API_NAME" --network "$NET" \
  -p "127.0.0.1:$PORT:8080" \
  -e PPTS_HTTP_ADDR=:8080 \
  "${COMMON_ENV[@]}" \
  -v "$WORK/db":/data/db -v "$WORK/objects":/data/objects -v "$WORK/assets":/data/assets \
  "$IMAGE" server >/dev/null

wait_http "http://127.0.0.1:$PORT/healthz" || fail "api 没起来"
HEALTH=$(curl -sS --noproxy '*' "http://127.0.0.1:$PORT/healthz")
echo "$HEALTH" | grep -q '"status":"ok"\|"status": "ok"' || fail "healthz 不是 ok：$HEALTH"

docker run -d --name "$WORKER_NAME" --network "$NET" \
  -p "127.0.0.1:$WMETRICS:8091" \
  -e PPTS_WORKER_METRICS_ADDR=:8091 \
  "${COMMON_ENV[@]}" \
  -v "$WORK/db":/data/db -v "$WORK/objects":/data/objects -v "$WORK/assets":/data/assets \
  "$IMAGE" worker >/dev/null
sleep 3
docker ps --format '{{.Names}}' | grep -qx "$WORKER_NAME" || fail "worker 退出了"
# 端点起不来就等于"看不见降级"，这里必须先确认它真的能读。
for i in $(seq 1 20); do
  curl -sS --noproxy '*' -H "Authorization: Bearer $METRICS_TOKEN" \
    "http://127.0.0.1:$WMETRICS/metrics" -o "$WORK/worker-metrics.txt" 2>/dev/null && break
  sleep 1
done
[ -s "$WORK/worker-metrics.txt" ] || fail "worker 指标端点读不到（$WMETRICS）"

log "3/8 建项目并上传 $(basename "$DECK")"
PID=$(post /ppts.v1.ProjectService/Create '{"title":"真实导出联调"}' | json 'print(d["project"]["id"])')
[ -n "$PID" ] || fail "建项目失败"
SIZE=$(stat -c%s "$DECK")
HASH=$(sha256sum "$DECK" | cut -d' ' -f1)
UP=$(post /ppts.v1.UploadService/CreateUpload \
  "{\"projectId\":\"$PID\",\"filename\":\"deck.pptx\",\"sizeBytes\":$SIZE,\"contentType\":\"application/vnd.openxmlformats-officedocument.presentationml.presentation\"}")
UPLOAD_ID=$(echo "$UP" | json 'print(d["uploadId"])')
SIGNED=$(echo "$UP" | json 'print(d["signedUploadUrls"][0])')
curl -sS --noproxy '*' -X PUT "http://127.0.0.1:$PORT$SIGNED" --data-binary @"$DECK" -o /dev/null
PARSE_JOB=$(post /ppts.v1.UploadService/CompleteUpload \
  "{\"uploadId\":\"$UPLOAD_ID\",\"expectedHash\":\"$HASH\",\"sizeBytes\":$SIZE}" | json 'print(d["jobId"])')
wait_job "$PARSE_JOB" 300 "解析任务" || fail "解析没完成"
echo "project=$PID"

log "4/8 核对解析产物：页图必须真的落在对象存储里"
post /ppts.v1.ProjectService/GetSlides "{\"projectId\":\"$PID\",\"revisionNo\":1}" > "$WORK/slides.json"
PAGES=$(json 'print(len(d.get("slides") or []))' < "$WORK/slides.json")
[ "$PAGES" -gt 3 ] || fail "解析页数异常：$PAGES"
PNG_COUNT=$(find "$WORK/objects" -type f -name '*.png' | wc -l)
[ "$PNG_COUNT" -ge "$PAGES" ] || fail "对象存储里只有 $PNG_COUNT 张 PNG，少于页数 $PAGES（soffice/pdftoppm 没真正跑）"
echo "页数=$PAGES 页图=$PNG_COUNT"
# 降级信号：渲染被禁用时导出仍会成功，只是所有页都是占位图——必须在这里拦下。
# 这两条都在 worker 进程里（渲染与 TTS 都是 worker 干的），api 的 /metrics 里没有。
curl -sS --noproxy '*' -H "Authorization: Bearer $METRICS_TOKEN" \
  "http://127.0.0.1:$WMETRICS/metrics" > "$WORK/worker-metrics.txt"
grep -qE '^ppts_render_pages_disabled 1' "$WORK/worker-metrics.txt" && fail "渲染处于禁用态（ppts_render_pages_disabled=1）"
grep -qE '^ppts_tts_fake_provider_active 1' "$WORK/worker-metrics.txt" || fail "假 TTS 没启用，本次配音来源与预期不符"

log "5/8 生成讲稿（原文朗读，取每页备注）"
SLIDE_IDS=$(json 'print(json.dumps([s["slideId"] for s in d["slides"]]))' < "$WORK/slides.json")
if [ "$SLIDE_LIMIT" -gt 0 ]; then
  SLIDE_IDS=$(echo "$SLIDE_IDS" | json "print(json.dumps(d[:$SLIDE_LIMIT]))")
fi
TIMELINE_PAGES=$(echo "$SLIDE_IDS" | json 'print(len(d))')
echo "本次参与成片页数=$TIMELINE_PAGES（解析页数 $PAGES）"
SCRIPT_JOB=$(curl -sS --noproxy '*' -X POST "http://127.0.0.1:$PORT/projects/$PID/script-draft" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: e2e-script-$PID" \
    -d "{\"slideIds\":$SLIDE_IDS,\"mode\":\"SCRIPT_MODE_ORIGINAL\",\"sourceMode\":\"notes_first\",\"revisionNo\":1,\"overwrite\":true}" \
    | json 'print(d["jobId"])')
wait_job "$SCRIPT_JOB" 180 "讲稿任务" || fail "讲稿没生成"

# Get 必须带源版本头：不带时服务端按 sourceRevision=0 查，与落库的 revision=1 不匹配，
# 返回 not_found。前端总会带，所以裸调 API 才踩得到。
post /ppts.v1.ScriptService/Get \
  "{\"projectId\":\"$PID\",\"slideId\":\"$(echo "$SLIDE_IDS" | json 'print(d[0])')\"}" \
  -H "X-PPTS-Source-Revision: 1" > "$WORK/script.json"
SEGMENTS=$(json 'print(len(d.get("segments") or []))' < "$WORK/script.json")
[ "$SEGMENTS" -gt 0 ] || fail "首屏讲稿没有分段：$WORK/script.json"
TEXT_CHARS=$(json 'print(sum(len(s.get("displayText") or "") for s in d.get("segments") or []))' < "$WORK/script.json")
[ "$TEXT_CHARS" -gt 10 ] || fail "首屏讲稿几乎为空（$TEXT_CHARS 字）"
echo "首屏分段=$SEGMENTS 字数=$TEXT_CHARS"

log "6/8 生成配音与时间轴"
GEN=$(post /ppts.v1.NarrationService/CreateGeneration \
  "{\"projectId\":\"$PID\",\"slideIds\":$SLIDE_IDS,\"voiceId\":\"e2e-voice\",\"ratePercent\":100}" \
  -H "X-PPTS-Source-Revision: 1" \
  -H "Idempotency-Key: e2e-narr-$PID")
NARR_JOB=$(echo "$GEN" | json 'print(d.get("jobId") or "")')
[ -n "$NARR_JOB" ] || fail "创建配音任务失败：$GEN"
wait_job "$NARR_JOB" 300 "配音任务" || fail "配音没完成"

for i in $(seq 1 60); do
  post /ppts.v1.PlaybackService/GetNarration "{\"projectId\":\"$PID\"}" > "$WORK/narration.json"
  if json 'print(1 if d.get("timelineKey") else 0)' < "$WORK/narration.json" | grep -qx 1; then break; fi
  sleep 1
done
TIMELINE_KEY=$(json 'print(d.get("timelineKey") or "")' < "$WORK/narration.json")
[ -n "$TIMELINE_KEY" ] || fail "配音成功但没产出 timelineKey"
json 'print(json.dumps(d.get("pagePngKeys") or []))' < "$WORK/narration.json" > "$WORK/pagekeys.json"
PAGE_KEYS=$(cat "$WORK/pagekeys.json")
PAGE_KEY_N=$(json 'print(len(d))' < "$WORK/pagekeys.json")
[ "$PAGE_KEY_N" -eq "$TIMELINE_PAGES" ] || fail "时间轴页图 $PAGE_KEY_N 张，与本次成片页数 $TIMELINE_PAGES 不符"
AUDIO_N=$(find "$WORK/objects" -type f \( -name '*.wav' -o -name '*.mp3' -o -name '*.m4a' \) | wc -l)
[ "$AUDIO_N" -gt 0 ] || fail "对象存储里没有任何音频片段"
echo "timeline=$TIMELINE_KEY 片段音频=$AUDIO_N"

ANIM_ARGS=""
if [ "$ANIMATE" = "1" ]; then
  log "6.5/8 提交逐页动画规格（默认规则，不经模型）"
  # 默认规格由服务端规则算出（GET ?default=true），不需要模型；把它原样 PUT 回去，
  # 就得到一份「真实存在库里」的规格。视频通道只认库里这一版，不认请求里的草稿。
  echo "$SLIDE_IDS" > "$WORK/slide-ids.json"
  "$PY" - "$PORT" "$PID" "$WORK/slide-ids.json" "$PRESET" "$WORK/specs.json" <<'PY'
import json, sys, urllib.request

port, pid, ids_path, preset, out_path = sys.argv[1:6]
slide_ids = json.load(open(ids_path, encoding='utf-8'))
specs = []
for sid in slide_ids:
    base = f"http://127.0.0.1:{port}/api/projects/{pid}/slides/{sid}/animation"
    get = urllib.request.Request(
        f"{base}?default=true&sourceRevisionNo=1&preset={preset}",
        headers={"X-PPTS-Source-Revision": "1"})
    with urllib.request.urlopen(get, timeout=30) as r:
        dto = json.load(r)
    if "tracks" not in dto:
        raise SystemExit(f"默认规格不可用（{sid}）：{dto}")
    body = json.dumps({
        "expectedRevision": 0,
        "presetId": dto.get("presetId") or "",
        "intensity": dto.get("intensity") or 1.0,
        "tracks": dto.get("tracks") or [],
    }).encode()
    put = urllib.request.Request(
        f"{base}?sourceRevisionNo=1", data=body, method="PUT",
        headers={"Content-Type": "application/json", "X-PPTS-Source-Revision": "1"})
    with urllib.request.urlopen(put, timeout=30) as r:
        saved = json.load(r)
    rev = saved.get("revision")
    if rev is None:
        raise SystemExit(f"提交规格失败（{sid}）：{saved}")
    specs.append({"slideId": sid, "sourceRevisionNo": 1, "revision": rev})
json.dump(specs, open(out_path, "w", encoding="utf-8"))
print(f"已提交 {len(specs)} 页规格（预设 {preset}）")
PY
  SPECS=$(cat "$WORK/specs.json")
  ANIM_ARGS=",\"animation\":{\"specs\":$SPECS}"
fi

log "7/8 导出 MP4（烧字幕）"
if [ -n "$ANIM_ARGS" ]; then
  # 动画只能走 REST 入口：proto 的 CreateExportRequest 没有 animation 字段
  # （见 internal/api/export_bundle.go 文件头）。用 Connect 入口发 specs 会被 protojson
  # 直接丢掉，导出静默退回静态——成片看起来"成功"，动画一帧都没有。
  EXPORT=$(curl -sS --noproxy '*' -X POST "http://127.0.0.1:$PORT/api/projects/$PID/exports" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: e2e-export-$PID" \
    -d "{\"format\":\"mp4\",\"timelineKey\":\"$TIMELINE_KEY\",\"pagePngKeys\":$PAGE_KEYS,\"width\":1920,\"height\":1080,\"fps\":30,\"burnSubtitles\":true,\"includeNotes\":false$ANIM_ARGS}")
else
  EXPORT=$(post /ppts.v1.ExportService/CreateExport \
    "{\"projectId\":\"$PID\",\"format\":\"ARTIFACT_FORMAT_MP4\",\"burnSubtitles\":true,\"includeNotes\":false,\"timelineKey\":\"$TIMELINE_KEY\",\"pagePngKeys\":$PAGE_KEYS}" \
    -H "Idempotency-Key: e2e-export-$PID")
fi
EXPORT_JOB=$(echo "$EXPORT" | json 'print(d.get("jobId") or "")')
[ -n "$EXPORT_JOB" ] || fail "创建导出任务失败：$EXPORT"
wait_job "$EXPORT_JOB" "$JOB_TIMEOUT" "导出任务" || fail "导出没完成"

curl -sS --noproxy '*' "http://127.0.0.1:$PORT/projects/$PID/artifacts" > "$WORK/artifacts.json"
ART_ID=$(json 'print([a["id"] for a in d["artifacts"] if a["format"]=="mp4"][0])' < "$WORK/artifacts.json")
[ -n "$ART_ID" ] || fail "成品列表里没有 mp4：$(cat "$WORK/artifacts.json")"
DL=$(post /ppts.v1.ExportService/CreateDownload "{\"artifactId\":\"$ART_ID\",\"ttlSeconds\":600}")
URL=$(echo "$DL" | json 'print(d["signedUrl"])')
case "$URL" in http*) ;; *) URL="http://127.0.0.1:$PORT$URL" ;; esac
curl -sS --noproxy '*' -L "$URL" -o "$WORK/out/artifact.mp4"
MP4_BYTES=$(stat -c%s "$WORK/out/artifact.mp4")
[ "$MP4_BYTES" -gt 102400 ] || fail "下载到的 mp4 只有 $MP4_BYTES 字节（不是成品）"
echo "artifact=$ART_ID bytes=$MP4_BYTES"

log "8/8 校验成品：容器/编码/分辨率/时长/音轨，并抽帧"
docker run --rm -v "$WORK/out":/out --entrypoint ffprobe "$IMAGE" \
  -v error -show_format -show_streams -of json /out/artifact.mp4 > "$WORK/out/ffprobe.json"
"$PY" - "$WORK/out/ffprobe.json" "$WORK/narration.json" <<'PY'
import json, sys
info = json.load(open(sys.argv[1], encoding='utf-8'))
streams = info.get('streams') or []
video = [s for s in streams if s.get('codec_type') == 'video']
audio = [s for s in streams if s.get('codec_type') == 'audio']
errs = []
if len(video) != 1:
    errs.append(f'视频流数量 {len(video)}，期望 1')
if not audio:
    errs.append('没有音频流——配音没进成片')
if video:
    v = video[0]
    if v.get('codec_name') != 'h264':
        errs.append(f"编码 {v.get('codec_name')}，期望 h264")
    if (v.get('width'), v.get('height')) != (1920, 1080):
        errs.append(f"分辨率 {v.get('width')}x{v.get('height')}，期望 1920x1080")
    if v.get('pix_fmt') != 'yuv420p':
        errs.append(f"像素格式 {v.get('pix_fmt')}，期望 yuv420p（不兼容的播放器会黑屏）")
fmt = info.get('format') or {}
duration = float(fmt.get('duration') or 0)
if duration < 5:
    errs.append(f'时长 {duration:.2f}s，过短')
if errs:
    print('成品校验失败：', file=sys.stderr)
    for e in errs:
        print('  -', e, file=sys.stderr)
    sys.exit(1)
print(f"视频 h264 {video[0]['width']}x{video[0]['height']} {video[0].get('pix_fmt')} "
      f"{video[0].get('avg_frame_rate')}；音频 {audio[0].get('codec_name')} "
      f"{audio[0].get('sample_rate')}Hz/{audio[0].get('channels')}ch；时长 {duration:.2f}s")
PY

# 抽三帧：起势/中段/收尾。字幕是否真的烧进画面只能看像素，这一条不交给脚本猜。
DURATION=$(json 'print(d["format"]["duration"])' < "$WORK/out/ffprobe.json")
for f in 1 2 3; do
  ts=$("$PY" -c "print(max(0.5, $DURATION * $f / 4))")
  docker run --rm -v "$WORK/out":/out --entrypoint ffmpeg "$IMAGE" \
    -v error -ss "$ts" -i /out/artifact.mp4 -frames:v 1 -y "/out/frame-$f.png"
done
for f in 1 2 3; do
  [ -s "$WORK/out/frame-$f.png" ] || fail "第 $f 帧抽不出来"
done

if [ -n "$ANIM_ARGS" ]; then
  # 动画通道走成功时，worker 会结算本片渲染的分钟数——这是「渲染真的发生了」的
  # 内部凭据。只看导出成功无法区分「动画渲了」和「静默退回静态」：两者的成片都能播，
  # 后者只是每一页都不动。degraded_pages>0 同样是失败：那表示有页没拿到动画段。
  ANIM_LOG=$(docker logs "$WORKER_NAME" 2>&1 | grep "settled render minutes" || true)
  [ -n "$ANIM_LOG" ] || fail "worker 没有结算动画渲染分钟数（动画通道未生效，可能静默退回静态）"
  echo "$ANIM_LOG" | grep -q '"degraded_pages":0' || fail "有页在动画渲染中退化为静态：$ANIM_LOG"
  echo "动画通道已生效：$ANIM_LOG"
fi

printf '\nOK: 真实导出联调通过\n'
printf '  工作目录：%s\n' "$WORK"
printf '  成片：%s/out/artifact.mp4（%s 字节）\n' "$WORK" "$MP4_BYTES"
printf '  抽帧：%s/out/frame-{1,2,3}.png\n' "$WORK"
