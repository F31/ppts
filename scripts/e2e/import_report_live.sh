#!/usr/bin/env bash
# 导入报告的真机联调。
#
# 单测验的是"函数怎么算"，这里验的是"真实进程里这条链能不能走通"：
#   上传 → 解析 worker 落 document/extracted.json → 回写 page_count → 报告接口读回。
# 报告与 GetSlides 必须描述同一份解析产物，否则用户在报告页看到的是另一份文件的结论。
#
# 用法：bash scripts/e2e/import_report_live.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

PPTS_PORT=${PPTS_PORT:-38083}
WORK=${WORK:-tmp/import-report-e2e}
DECK=${DECK:-internal/animation/animdefault/testdata/sample-customer-deck.pptx}

log() { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

PY=python
command -v python >/dev/null 2>&1 || PY=python3

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/objects"
WORK_ABS=$($PY -c "import os,sys;print(os.path.abspath(sys.argv[1]).replace(os.sep,'/'))" "$WORK")

SECRET=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")

cat > "$WORK/env.sh" <<EOF
PPTS_DB_DRIVER=sqlite
PPTS_DATABASE_URL=$WORK_ABS/data/live.db
PPTS_OBJECT_BACKEND=local
PPTS_OBJECT_ROOT=$WORK_ABS/objects
PPTS_OBJECT_SECRET=$SECRET
PPTS_HTTP_ADDR=127.0.0.1:$PPTS_PORT
PPTS_TTS_PROVIDER=fake
PPTS_AUTH_DEV_HEADERS=true
EOF

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

wait_http() {
  local url=$1 want=$2 i
  for i in $(seq 1 40); do
    local body
    body=$(curl -s --noproxy '*' -m 2 "$url" 2>/dev/null || true)
    case "$body" in *"$want"*) return 0 ;; esac
    sleep 1
  done
  return 1
}

post() { # post <path> <json> [extra curl args...]
  local path=$1 body=$2
  shift 2
  curl -s --noproxy '*' -X POST "http://127.0.0.1:$PPTS_PORT$path" \
    -H 'Content-Type: application/json' "$@" -d "$body"
}

get_status() { # get_status <path> <outfile>
  curl -s --noproxy '*' -o "$2" -w '%{http_code}' "http://127.0.0.1:$PPTS_PORT$1"
}

log "0/5 构建"
go build -o "$WORK/ppts.exe" ./cmd/ppts

log "1/5 起 server/worker"
set -a; . "$WORK/env.sh"; set +a
"$WORK/ppts.exe" server > "$WORK/server.log" 2>&1 &
PIDS+=($!)
wait_http "http://127.0.0.1:$PPTS_PORT/healthz" '"status"' || { tail -20 "$WORK/server.log"; fail "server 没起来"; }
"$WORK/ppts.exe" worker > "$WORK/worker.log" 2>&1 &
PIDS+=($!)
sleep 2

log "2/5 建项目并上传 $(basename "$DECK")"
PID=$(post /ppts.v1.ProjectService/Create '{"title":"import report e2e"}' | $PY -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')
SIZE=$(stat -c%s "$DECK")
HASH=$(sha256sum "$DECK" | cut -d' ' -f1)
UP=$(post /ppts.v1.UploadService/CreateUpload \
  "{\"projectId\":\"$PID\",\"filename\":\"deck.pptx\",\"sizeBytes\":$SIZE,\"contentType\":\"application/vnd.openxmlformats-officedocument.presentationml.presentation\"}")
UPLOAD_ID=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["uploadId"])')
SIGNED=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["signedUploadUrls"][0])')
curl -s --noproxy '*' -X PUT "http://127.0.0.1:$PPTS_PORT$SIGNED" --data-binary @"$DECK" -o /dev/null
JOB=$(post /ppts.v1.UploadService/CompleteUpload \
  "{\"uploadId\":\"$UPLOAD_ID\",\"expectedHash\":\"$HASH\",\"sizeBytes\":$SIZE}" \
  | $PY -c 'import sys,json;print(json.load(sys.stdin)["jobId"])')

wait_job() { # wait_job <jobId> <期望状态> <秒上限>
  local id=$1 want=$2 limit=${3:-120} i state
  for i in $(seq 1 "$limit"); do
    state=$(post /ppts.v1.JobService/Get "{\"jobId\":\"$id\"}" | $PY -c 'import sys,json;print(json.load(sys.stdin).get("state",""))')
    [ "$state" = "$want" ] && return 0
    case "$state" in JOB_STATE_FAILED|JOB_STATE_CANCELED) fail "任务 $id 落到 $state";; esac
    sleep 1
  done
  fail "任务 $id 在 ${limit}s 内没到 $want"
}

log "3/5 等解析完成"
wait_job "$JOB" JOB_STATE_SUCCEEDED

log "4/5 读报告并与 GetSlides 对账"
CODE=$(get_status "/projects/$PID/revisions/1/import-report" "$WORK/report.json")
[ "$CODE" = "200" ] || { cat "$WORK/report.json"; fail "报告接口返回 $CODE"; }
post /ppts.v1.ProjectService/GetSlides "{\"projectId\":\"$PID\",\"revisionNo\":1}" > "$WORK/slides.json"

$PY - "$WORK/report.json" "$WORK/slides.json" <<'PYEOF'
import json, sys

report_doc = json.load(open(sys.argv[1], encoding='utf-8'))
slides_doc = json.load(open(sys.argv[2], encoding='utf-8'))
rev = report_doc['revision']
rep = report_doc['report']
pages = rep['pages']
slides = slides_doc['slides']
errors = []

def check(ok, msg):
    if not ok:
        errors.append(msg)

# 版本元信息：报告必须说清"哪一版、什么名字"。
check(rev['revisionNo'] == 1, f"revisionNo = {rev['revisionNo']}")
check(rev['displayName'] == 'deck.pptx', f"displayName = {rev['displayName']!r}")

# 页数以解析产物为准，且必须与回写的 revision.page_count 一致（回写链路真的跑了）。
check(rep['pageCount'] == len(pages), f"pageCount {rep['pageCount']} != pages {len(pages)}")
check(rev['pageCount'] == rep['pageCount'], f"回写页数 {rev['pageCount']} != 报告页数 {rep['pageCount']}")
check(len(slides) == len(pages), f"slides {len(slides)} != report pages {len(pages)}")
check(rep['pageCount'] > 3, f"样本页数异常：{rep['pageCount']}")

# 同一份产物：页序、slideId、逐页标记必须与 GetSlides 完全对齐。
if len(slides) == len(pages):
    for src, row in zip(slides, pages):
        if src['slideId'] != row['slideId']:
            errors.append(f"第 {row['index']} 页 slideId 不一致：{src['slideId']} vs {row['slideId']}")
        if sorted(src.get('featureFlags') or []) != sorted(row.get('flags') or []):
            errors.append(f"第 {row['index']} 页标记不一致：{src.get('featureFlags')} vs {row.get('flags')}")

# 汇总自洽。
hidden = sum(1 for p in pages if p['hidden'])
check(rep['hiddenPages'] == hidden, f"hiddenPages {rep['hiddenPages']} != 逐页统计 {hidden}")
check(rep['pagesWithNotes'] + rep['pagesWithoutNotes'] == rep['pageCount'],
      f"备注计数 {rep['pagesWithNotes']}+{rep['pagesWithoutNotes']} != {rep['pageCount']}")
with_notes = sum(1 for p in pages if p['notesChars'] > 0)
check(rep['pagesWithNotes'] == with_notes, f"pagesWithNotes {rep['pagesWithNotes']} != 逐页 {with_notes}")

# 条目与事实一致：有隐藏页就必须有 hidden_pages 条目，反之也不能凭空出现。
by_code = {f['code']: f for f in rep['findings']}
check(('hidden_pages' in by_code) == (hidden > 0), "hidden_pages 条目与隐藏页数不一致")
check(('missing_notes' in by_code) == (rep['pagesWithoutNotes'] > 0), "missing_notes 条目与无备注页数不一致")
check(('animations' in by_code) == any('timing' in (p.get('flags') or []) for p in pages),
      "animations 条目与 timing 标记不一致")
for f in rep['findings']:
    check(f['severity'] in ('warn', 'info'), f"{f['code']} 的 severity 非法：{f['severity']}")
    check(f['count'] > 0, f"{f['code']} 的 count = {f['count']}")

# 排序契约：warn 全在 info 之前。
seen_info = False
for f in rep['findings']:
    if f['severity'] == 'info':
        seen_info = True
    elif seen_info:
        errors.append(f"warn 条目 {f['code']} 排在 info 之后")

if errors:
    print("报告对账失败：", file=sys.stderr)
    for e in errors:
        print("  -", e, file=sys.stderr)
    sys.exit(1)

print(f"页数 {rep['pageCount']}（隐藏 {rep['hiddenPages']}），有备注 {rep['pagesWithNotes']} 页，"
      f"条目 {len(rep['findings'])} 条：{', '.join(f['code'] for f in rep['findings']) or '无'}")
PYEOF

log "5/5 错误分支：版本不存在 / 版本号非法"
CODE=$(get_status "/projects/$PID/revisions/9/import-report" "$WORK/notfound.json")
[ "$CODE" = "404" ] || { cat "$WORK/notfound.json"; fail "不存在的版本返回 $CODE，期望 404"; }
grep -q '"revision_not_found"' "$WORK/notfound.json" || fail "404 缺少 code=revision_not_found"

CODE=$(get_status "/projects/$PID/revisions/0/import-report" "$WORK/invalid.json")
[ "$CODE" = "400" ] || { cat "$WORK/invalid.json"; fail "非法版本号返回 $CODE，期望 400"; }
grep -q '"invalid_revision"' "$WORK/invalid.json" || fail "400 缺少 code=invalid_revision"

printf '\nOK: 导入报告真机联调通过\n'
