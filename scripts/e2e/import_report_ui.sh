#!/usr/bin/env bash
# 一次性人工核验：起真服务、上传样本、打开导入报告页截图。
# 不进仓库门禁（页面逻辑由 web/import-report-check.mjs 兜），这里只为"用眼睛确认版面"。
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
PORT=${PORT:-38084}
WORK=tmp/import-report-ui
# 样本：10 页、每页都有备注，再把第 2 页标成隐藏（presentation.xml 里 sldId 加 show="0"）。
# 这样一屏能同时看到"有备注""隐藏页"两类事实，隐藏行的样式也才有得看。
# 现成样本里没有带隐藏页的，所以现场改一份，而不是把断言放宽。
DECK_SRC=${DECK:-testdata/corpus/generated/s108.pptx}
DECK="$WORK/deck.pptx"

PY=python
command -v python >/dev/null 2>&1 || PY=python3

rm -rf "$WORK"; mkdir -p "$WORK/data" "$WORK/objects"
$PY - "$DECK_SRC" "$DECK" <<'PYEOF'
import sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(src) as zin, zipfile.ZipFile(dst, 'w', zipfile.ZIP_DEFLATED) as zout:
    for item in zin.infolist():
        data = zin.read(item.filename)
        if item.filename == 'ppt/presentation.xml':
            text = data.decode('utf-8')
            first = text.find('<p:sldId ')
            second = text.find('<p:sldId ', first + 1)
            if second < 0:
                raise SystemExit('presentation.xml 里找不到第二张幻灯片，无法准备隐藏页样本')
            text = text[:second] + text[second:].replace('<p:sldId ', '<p:sldId show="0" ', 1)
            data = text.encode('utf-8')
        zout.writestr(item, data)
PYEOF
WORK_ABS=$($PY -c "import os,sys;print(os.path.abspath(sys.argv[1]).replace(os.sep,'/'))" "$WORK")
SECRET=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")

export PPTS_DB_DRIVER=sqlite
export PPTS_DATABASE_URL=$WORK_ABS/data/live.db
export PPTS_OBJECT_BACKEND=local
export PPTS_OBJECT_ROOT=$WORK_ABS/objects
export PPTS_OBJECT_SECRET=$SECRET
export PPTS_HTTP_ADDR=127.0.0.1:$PORT
export PPTS_TTS_PROVIDER=fake
export PPTS_AUTH_DEV_HEADERS=true

PIDS=()
cleanup() { for pid in "${PIDS[@]:-}"; do [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT

go build -o "$WORK/ppts.exe" ./cmd/ppts
"$WORK/ppts.exe" server > "$WORK/server.log" 2>&1 & PIDS+=($!)
for i in $(seq 1 30); do curl -s --noproxy '*' -m 1 "http://127.0.0.1:$PORT/healthz" | grep -q status && break; sleep 1; done
"$WORK/ppts.exe" worker > "$WORK/worker.log" 2>&1 & PIDS+=($!)
sleep 2

post() { local path=$1 body=$2; shift 2; curl -s --noproxy '*' -X POST "http://127.0.0.1:$PORT$path" -H 'Content-Type: application/json' "$@" -d "$body"; }
PID=$(post /ppts.v1.ProjectService/Create '{"title":"季度技术汇报"}' | $PY -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')
SIZE=$(stat -c%s "$DECK"); HASH=$(sha256sum "$DECK" | cut -d' ' -f1)
UP=$(post /ppts.v1.UploadService/CreateUpload "{\"projectId\":\"$PID\",\"filename\":\"季度技术汇报.pptx\",\"sizeBytes\":$SIZE,\"contentType\":\"application/vnd.openxmlformats-officedocument.presentationml.presentation\"}")
UPLOAD_ID=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["uploadId"])')
SIGNED=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["signedUploadUrls"][0])')
curl -s --noproxy '*' -X PUT "http://127.0.0.1:$PORT$SIGNED" --data-binary @"$DECK" -o /dev/null
JOB=$(post /ppts.v1.UploadService/CompleteUpload "{\"uploadId\":\"$UPLOAD_ID\",\"expectedHash\":\"$HASH\",\"sizeBytes\":$SIZE}" | $PY -c 'import sys,json;print(json.load(sys.stdin)["jobId"])')
for i in $(seq 1 90); do
  STATE=$(post /ppts.v1.JobService/Get "{\"jobId\":\"$JOB\"}" | $PY -c 'import sys,json;print(json.load(sys.stdin).get("state",""))')
  [ "$STATE" = JOB_STATE_SUCCEEDED ] && break
  sleep 1
done
echo "project=$PID job=$JOB state=$STATE"

CDP_PORT=${CDP_PORT:-9224}
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
[ -n "$CHROME" ] || { echo "没找到 Chrome/Edge"; exit 1; }
PROFILE=$(mktemp -d 2>/dev/null || echo "$WORK/chrome")
command -v cygpath >/dev/null 2>&1 && PROFILE=$(cygpath -w "$PROFILE")
"$CHROME" --headless=new --remote-debugging-port="$CDP_PORT" --user-data-dir="$PROFILE" \
  --no-first-run --no-default-browser-check --disable-gpu --disable-extensions \
  --disable-background-networking --window-size=1440,1200 about:blank > "$WORK/chrome.log" 2>&1 &
PIDS+=($!)
for i in $(seq 1 30); do curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version" && break; sleep 1; done

node scripts/e2e/import_report_click.mjs --port="$PORT" --cdp="$CDP_PORT" --project="$PID" --rev=1 --hidden=1 --shot="$WORK/shot.png"
