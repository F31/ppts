#!/usr/bin/env bash
# 预填异步通道的真机联调（M1b 验收）。
#
# 这一条脚本跑的是**真实进程**：真实的 ppts server、真实的 worker、真实的 SQLite、
# 真实的本地对象存储、真实的 pptx 解析、真实的 Connect/REST 序列化，模型侧用本机假
# OpenAI 兼容服务替身（协议是真的：base url、json_object、temperature=0、usage 都走一遍）。
#
# 它回答的问题是单元测试回答不了的：
#   1. 服务端把「这一页现在长什么样」钉进快照后，worker 拿到的是不是同一份内容；
#   2. script 档是不是真的把 HTTP 请求发给了模型，回来的绑定是不是真的被采纳；
#   3. 模型挂掉时编辑器手里是不是仍有可编辑的规则底稿（而不是一屏空白）；
#   4. 模型恢复后，同一页再点预填会不会被上一次的失败结果挡住（联调里抓到过，已修）；
#   5. token 预占/结算是不是真的落进了额度账本；
#   6. 计数（route / fallback / 缓存处置）是不是真的被写进了 /metrics。
#
# 用法：
#   bash scripts/e2e/prefill_live.sh
#   PPTS_PORT=38081 LLM_PORT=38190 bash scripts/e2e/prefill_live.sh
#
# 不做的事：不跑前端、不点按钮。像素与交互由 web 的构建期检查与 animation-check 覆盖；
# 这里只保证「后端到模型到落盘再读回」这条路是真的。
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

PPTS_PORT=${PPTS_PORT:-38081}
LLM_PORT=${LLM_PORT:-38190}
# worker 的指标端点在另一个监听上（进程隔离：worker 侧的 expvar 只有本进程能读）。
METRICS_PORT=${METRICS_PORT:-38091}
WORK=${WORK:-tmp/live-e2e}
DECK=${DECK:-internal/animation/animdefault/testdata/sample-customer-deck.pptx}
# 假模型回的是这一页的元素：第 7 页（index 6，slide-262）四张卡片。
SLIDE_INDEX=${SLIDE_INDEX:-6}

log() { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

PY=python
command -v python >/dev/null 2>&1 || PY=python3

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/objects"
# Windows 上 os.path.abspath 给的是反斜杠路径：现代 SQLite 驱动按 URI 解析 DSN，
# 反斜杠会被当成转义，表现为 "unable to open database file (14)"。
WORK_ABS=$($PY -c "import os,sys;print(os.path.abspath(sys.argv[1]).replace(os.sep,'/'))" "$WORK")

SECRET=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")
AES=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")
# 指标端点 fail-closed：没配 token 一律 401。这里必须给一个，否则「指标读不到」
# 会被误当成「指标没写」。
METRICS_TOKEN=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(24)).decode())")

cat > "$WORK/env.sh" <<EOF
PPTS_DB_DRIVER=sqlite
PPTS_DATABASE_URL=$WORK_ABS/data/live.db
PPTS_OBJECT_BACKEND=local
PPTS_OBJECT_ROOT=$WORK_ABS/objects
PPTS_OBJECT_SECRET=$SECRET
PPTS_HTTP_ADDR=127.0.0.1:$PPTS_PORT
PPTS_TTS_PROVIDER=fake
PPTS_ANIMATION_ENABLED=true
PPTS_AUTH_DEV_HEADERS=true
PPTS_GATEWAY_AES_KEY_BASE64=$AES
PPTS_LLM_PROVIDER=siliconflow
PPTS_LLM_API_KEY=fake-key
PPTS_LLM_BASE_URL=http://127.0.0.1:$LLM_PORT/v1
PPTS_LLM_MODEL=fake-model
PPTS_WORKER_METRICS_ADDR=127.0.0.1:$METRICS_PORT
PPTS_METRICS_TOKEN=$METRICS_TOKEN
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

# 假模型只实现 POST /v1/chat/completions，GET 会拿到 501；探活只要能连上就行。
wait_port() {
  local port=$1 i
  for i in $(seq 1 40); do
    curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:$port/" && return 0
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

log "0/8 构建"
go build -o "$WORK/ppts.exe" ./cmd/ppts

log "1/8 起假模型与 server/worker"
$PY scripts/e2e/fake_llm.py "$LLM_PORT" "$WORK/fake-llm.jsonl" &
PIDS+=($!)
wait_port "$LLM_PORT" || fail "假模型没起来"

set -a; . "$WORK/env.sh"; set +a
"$WORK/ppts.exe" server > "$WORK/server.log" 2>&1 &
PIDS+=($!)
wait_http "http://127.0.0.1:$PPTS_PORT/healthz" '"status"' || { tail -20 "$WORK/server.log"; fail "server 没起来"; }

"$WORK/ppts.exe" worker > "$WORK/worker.log" 2>&1 &
PIDS+=($!)
sleep 2

log "2/8 建项目并上传 $DECK"
PID=$(post /ppts.v1.ProjectService/Create '{"title":"live e2e"}' | $PY -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')
echo "project=$PID"

SIZE=$(stat -c%s "$DECK")
HASH=$(sha256sum "$DECK" | cut -d' ' -f1)
UP=$(post /ppts.v1.UploadService/CreateUpload \
  "{\"projectId\":\"$PID\",\"filename\":\"deck.pptx\",\"sizeBytes\":$SIZE,\"contentType\":\"application/vnd.openxmlformats-officedocument.presentationml.presentation\"}")
UPLOAD_ID=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["uploadId"])')
SIGNED=$(echo "$UP" | $PY -c 'import sys,json;print(json.load(sys.stdin)["signedUploadUrls"][0])')
curl -s --noproxy '*' -X PUT "http://127.0.0.1:$PPTS_PORT$SIGNED" --data-binary @"$DECK" -o /dev/null
PARSE_JOB=$(post /ppts.v1.UploadService/CompleteUpload \
  "{\"uploadId\":\"$UPLOAD_ID\",\"expectedHash\":\"$HASH\",\"sizeBytes\":$SIZE}" \
  | $PY -c 'import sys,json;print(json.load(sys.stdin)["jobId"])')

wait_job() { # wait_job <jobId> <期望状态> <秒上限>
  local id=$1 want=$2 limit=${3:-90} i state
  for i in $(seq 1 "$limit"); do
    state=$(post /ppts.v1.JobService/Get "{\"jobId\":\"$id\"}" | $PY -c 'import sys,json;print(json.load(sys.stdin).get("state",""))')
    [ "$state" = "$want" ] && return 0
    case "$state" in JOB_STATE_FAILED|JOB_STATE_CANCELED) fail "任务 $id 落到 $state";; esac
    sleep 1
  done
  fail "任务 $id 在 ${limit}s 内没到 $want"
}

log "3/8 等解析完成"
wait_job "$PARSE_JOB" JOB_STATE_SUCCEEDED
SLIDES=$(post /ppts.v1.ProjectService/GetSlides "{\"projectId\":\"$PID\",\"revisionNo\":1}")
SLIDE_ID=$(echo "$SLIDES" | $PY -c "import sys,json;print(json.load(sys.stdin)['slides'][$SLIDE_INDEX]['slideId'])")
echo "slide=$SLIDE_ID"

log "4/8 播种讲稿并配音"
# 为什么直接写库：产品路径上的讲稿来自「AI 生成草稿」或「从 PPT 备注导入」，
# 两条路都要 LLM 或备注。这一段要验的是预填，不是讲稿生成；先用一行 legacy 脚本
# 让 Update 走真实的 fork 分支，后面的编辑仍然全程走 RPC。
$PY - "$WORK_ABS/data/live.db" "$PID" "$SLIDE_ID" <<'PYEOF'
import sqlite3, sys, uuid
from datetime import datetime, timezone
dbpath, project, slide = sys.argv[1], sys.argv[2], sys.argv[3]
now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (datetime.now(timezone.utc).microsecond // 1000)
db = sqlite3.connect(dbpath)
db.execute("""INSERT INTO narration_scripts
 (id,tenant_id,project_id,slide_id,language,mode,status,revision,audio_revision,source_revision_no,updated_at,created_at)
 VALUES (?,?,?,?,?,'original','draft',0,0,0,?,?)""",
 (str(uuid.uuid4()), "00000000-0000-0000-0000-000000000001", project, slide, "zh-CN", now, now))
db.commit()
PYEOF

SCRIPT_JSON=$($PY - "$PID" "$SLIDE_ID" <<'PYEOF'
import json, sys
project, slide = sys.argv[1], sys.argv[2]
texts = [
    "先看企业培训这块。HR 或讲师做完课件，过去还得请人录一遍，现在把讲稿写进备注就能出配音版。",
    "在线课程也是类似的路子。老师把讲稿准备好，视频就能自动生成，改一版也很快。",
    "销售最头疼的是同一个方案要反复讲。有了配音版，客户可以自己先看一遍，见面只聊真正关心的问题。",
    "最后是多语言和无障碍。同一份课件直接出多个语种的讲解版本，不用为每个市场重做一遍。",
]
segs = [{"segmentId": f"seg-07-0{i+1}", "slideId": slide, "displayText": t, "spokenText": t} for i, t in enumerate(texts)]
print(json.dumps({"projectId": project, "slideId": slide, "expectedRevision": "0", "segments": segs}, ensure_ascii=False))
PYEOF
)
post /ppts.v1.ScriptService/Update "$SCRIPT_JSON" -H 'X-PPTS-Source-Revision: 1' > /dev/null

NARR=$(post /ppts.v1.NarrationService/CreateGeneration \
  "{\"projectId\":\"$PID\",\"slideIds\":[\"$SLIDE_ID\"],\"voiceId\":\"default\"}" \
  -H 'X-PPTS-Source-Revision: 1' -H 'Idempotency-Key: live-narr-1')
wait_job "$(echo "$NARR" | $PY -c 'import sys,json;print(json.load(sys.stdin)["jobId"])')" JOB_STATE_SUCCEEDED 120

# 假模型调用计数：文件在第一次调用之前不存在，直接 wc 会在 stderr 留一行噪音。
count_llm_calls() { if [ -f "$WORK_ABS/fake-llm.jsonl" ]; then wc -l < "$WORK_ABS/fake-llm.jsonl"; else echo 0; fi; }

prefill_post() { post "/api/projects/$PID/slides/$SLIDE_ID/animation/prefill" '{}'; }
prefill_get() {
  curl -s --noproxy '*' "http://127.0.0.1:$PPTS_PORT/api/projects/$PID/slides/$SLIDE_ID/animation/prefill?jobId=$1"
}
field() { $PY -c "import sys,json;d=json.load(sys.stdin);print(d.get('$1') or '')"; }

# metric_value <port> <series 前缀>：取该 series 的值；同一指标的多条 series 求和。
#
# 不看指标就等于「上线后只能靠用户说感觉慢」。这一步同时验证两件事：
# 计数真的被写出来了（不是代码里有这行），以及标签渲染成了 label 而不是指标名的一部分。
metric_value() {
  curl -s --noproxy '*' -H "Authorization: Bearer $METRICS_TOKEN" \
    "http://127.0.0.1:$1/metrics" 2>/dev/null \
    | awk -v want="$2" 'index($0, want) == 1 { total += $2 } END { printf "%d\n", total + 0 }'
}

log "5/8 模型可用：script 档应真的走模型"
BEFORE=$(count_llm_calls)
RESP=$(prefill_post)
CODE_STATUS=$(echo "$RESP" | field status)
[ "$CODE_STATUS" = "pending" ] || fail "期望 202/pending，实际 $CODE_STATUS（说明这一页没被路由给模型）"
JOB=$(echo "$RESP" | field jobId)
wait_job "$JOB" JOB_STATE_SUCCEEDED
GOT=$(prefill_get "$JOB")
ROUTE=$(echo "$GOT" | $PY -c 'import sys,json;print(((json.load(sys.stdin).get("spec") or {}).get("prefill") or {}).get("route",""))')
AFTER=$(count_llm_calls)
[ "$ROUTE" = "model" ] || fail "route=$ROUTE，期望 model"
[ "$AFTER" -gt "$BEFORE" ] || fail "模型一次都没被调用（请求数 $BEFORE -> $AFTER）"
echo "route=model，模型请求 +$((AFTER-BEFORE))"

RUNS=$(metric_value "$METRICS_PORT" 'ppts_animation_prefill_runs_total{route="model",band="low-confidence",reason="script"')
[ "$RUNS" -ge 1 ] || fail "worker 指标里没有 route=model/reason=script 的预填计数（指标没接上，或标签被渲染成了指标名）"
CALLS=$(metric_value "$METRICS_PORT" 'ppts_animation_prefill_model_calls_total')
[ "$CALLS" -ge 1 ] || fail "worker 指标里 model_calls=0：真实调用没有被计数"
echo "worker 指标：runs(model/script)=$RUNS，model_calls=$CALLS"

log "6/8 模型挂掉：必须降级到规则，任务仍然成功"
kill -9 "${PIDS[0]}" 2>/dev/null || true
wait "${PIDS[0]}" 2>/dev/null || true # 收尸：否则 bash 会把 "Killed" 打进结果里，看起来像失败
$PY - "$PID" "$SLIDE_ID" <<'PYEOF' > "$WORK/script2.json"
import json, sys
project, slide = sys.argv[1], sys.argv[2]
texts = [
    "先看企业培训这块。HR 或讲师做完课件，过去还得请人录一遍，现在把讲稿写进备注就能出配音版。",
    "在线课程也是类似的路子。老师把讲稿准备好，视频就能自动生成，改一版也很快，不用重新录。",
    "销售最头疼的是同一个方案要反复讲。有了配音版，客户可以自己先看一遍，见面只聊真正关心的问题。",
    "最后是多语言和无障碍。同一份课件直接出多个语种的讲解版本，不用为每个市场重做一遍。",
]
segs = [{"segmentId": f"seg-07-0{i+1}", "slideId": slide, "displayText": t, "spokenText": t} for i, t in enumerate(texts)]
print(json.dumps({"projectId": project, "slideId": slide, "expectedRevision": "1", "segments": segs}, ensure_ascii=False))
PYEOF
post /ppts.v1.ScriptService/Update "$(cat "$WORK/script2.json")" -H 'X-PPTS-Source-Revision: 1' > /dev/null

RESP=$(prefill_post)
JOB=$(echo "$RESP" | field jobId)
wait_job "$JOB" JOB_STATE_SUCCEEDED
GOT=$(prefill_get "$JOB")
FALLBACK=$(echo "$GOT" | $PY -c 'import sys,json;print(((json.load(sys.stdin).get("spec") or {}).get("prefill") or {}).get("modelFallback",""))')
ROUTE=$(echo "$GOT" | $PY -c 'import sys,json;print(((json.load(sys.stdin).get("spec") or {}).get("prefill") or {}).get("route",""))')
[ "$FALLBACK" = "error" ] || fail "modelFallback=$FALLBACK，期望 error（模型已停）"
[ "$ROUTE" = "rule" ] || fail "route=$ROUTE，期望 rule"
echo "route=rule，fallback=error"

ERRS=$(metric_value "$METRICS_PORT" 'ppts_animation_prefill_model_fallback_total{reason="error"}')
[ "$ERRS" -ge 1 ] || fail "worker 指标里没有 fallback{reason=error}：模型挂了却看不出原因分布"
echo "worker 指标：fallback(error)=$ERRS"

log "7/8 模型恢复：同一内容再点预填必须重试，不能被上次的失败挡住"
$PY scripts/e2e/fake_llm.py "$LLM_PORT" "$WORK/fake-llm.jsonl" &
PIDS[0]=$!
sleep 1
BEFORE=$(count_llm_calls)
RESP=$(prefill_post)
STATUS=$(echo "$RESP" | field status)
JOB=$(echo "$RESP" | field jobId)
[ "$STATUS" = "pending" ] || fail "status=$STATUS，期望 pending（内容没变也必须重排，否则配上模型永远不生效）"
wait_job "$JOB" JOB_STATE_SUCCEEDED
GOT=$(prefill_get "$JOB")
ROUTE=$(echo "$GOT" | $PY -c 'import sys,json;print(((json.load(sys.stdin).get("spec") or {}).get("prefill") or {}).get("route",""))')
AFTER=$(count_llm_calls)
[ "$ROUTE" = "model" ] || fail "route=$ROUTE，期望 model（模型恢复后应重新生效）"
[ "$AFTER" -gt "$BEFORE" ] || fail "模型恢复了却没被调用"
echo "route=model，模型请求 +$((AFTER-BEFORE))"

# 这个数随「模型不可用时被打开的次数」线性增长，是判断要不要加负缓存的唯一依据。
RETRIES=$(metric_value "$PPTS_PORT" 'ppts_animation_prefill_cache_total{result="retry_after_failure"}')
[ "$RETRIES" -ge 1 ] || fail "API 指标里没有 cache{result=retry_after_failure}：失败结果被重算这件事不可观测"
echo "API 指标：cache(retry_after_failure)=$RETRIES"

log "8/8 同内容已成功：再点应当复用缓存，不再问模型"
BEFORE=$(count_llm_calls)
RESP=$(prefill_post)
STATUS=$(echo "$RESP" | field status)
AFTER=$(count_llm_calls)
[ "$STATUS" = "succeeded" ] || fail "status=$STATUS，期望 succeeded（成功结果应直接复用）"
[ "$AFTER" -eq "$BEFORE" ] || fail "命中了成功结果却又问了一次模型（$BEFORE -> $AFTER）"
echo "复用缓存，模型调用 0 次"

HITS=$(metric_value "$PPTS_PORT" 'ppts_animation_prefill_cache_total{result="hit"}')
[ "$HITS" -ge 1 ] || fail "API 指标里没有 cache{result=hit}：复用缓存不可观测"
echo "API 指标：cache(hit)=$HITS"

LEDGER=$($PY - "$WORK_ABS/data/live.db" "$PID" <<'PYEOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
rows = db.execute("""select logical_operation_id, quantity from usage_ledger
                     where usage_kind='llm_tokens' order by rowid desc limit 5""").fetchall()
print(len([r for r in rows if r[0].startswith('prefill:')]))
PYEOF
)
[ "$LEDGER" -ge 1 ] || fail "额度账本里没有 prefill 的 token 记账（预填绕开了额度）"
echo "额度账本里有 $LEDGER 条 prefill token 记账"

printf '\n全部通过。原始产物在 %s/\n' "$WORK"
