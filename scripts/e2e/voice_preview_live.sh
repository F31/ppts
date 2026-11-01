#!/usr/bin/env bash
# 音色试听的真机联调：从 HTTP 入口一路走到假 TTS 上游，再验回包。
#
# 为什么不是单测：单测里注入的是 fake provider，跨不过三件最容易错的事——
#   1) 网关里的 API key 能不能解出来（解错 → 上游 401，界面只会说"供应商故障"）；
#   2) 用户选的音色/语速有没有真的进请求体（漏传 → 试听和成片不是同一个声音）；
#   3) 回包时长是解码 WAV 得来的，还是按字数估的（估算值会让界面上标注错时长）。
# 这三件事都发生在序列化那一层，必须在真进程 + 真 HTTP 下过一遍。
#
# 用法：bash scripts/e2e/voice_preview_live.sh
# 退出码非 0 时打印失败原因；任何断言失败都视为阻断。
set -euo pipefail

PPTS_PORT=${PPTS_PORT:-38095}
TTS_PORT=${TTS_PORT:-38096}
WORK=${WORK:-tmp/voice-preview-e2e}

log() { printf '\n== %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

PY=python
command -v python >/dev/null 2>&1 || PY=python3

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/objects"
WORK_ABS=$($PY -c "import os,sys;print(os.path.abspath(sys.argv[1]).replace(os.sep,'/'))" "$WORK")

SECRET=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")
AES=$($PY -c "import base64,os;print(base64.b64encode(os.urandom(32)).decode())")

cat > "$WORK/env.sh" <<EOF
PPTS_DB_DRIVER=sqlite
PPTS_DATABASE_URL=$WORK_ABS/data/live.db
PPTS_OBJECT_BACKEND=local
PPTS_OBJECT_ROOT=$WORK_ABS/objects
PPTS_OBJECT_SECRET=$SECRET
PPTS_HTTP_ADDR=127.0.0.1:$PPTS_PORT
PPTS_AUTH_DEV_HEADERS=true
PPTS_GATEWAY_AES_KEY_BASE64=$AES
EOF

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

post() { # post <path> <json> [extra curl args...]
  local path=$1 body=$2
  shift 2
  curl -s --noproxy '*' -X POST "http://127.0.0.1:$PPTS_PORT$path" \
    -H 'Content-Type: application/json' "$@" -d "$body"
}

status_of() { # status_of <path> <json> <outfile> [extra curl args...]
  local path=$1 body=$2 out=$3
  shift 3
  curl -s --noproxy '*' -o "$out" -w '%{http_code}' -X POST "http://127.0.0.1:$PPTS_PORT$path" \
    -H 'Content-Type: application/json' "$@" -d "$body"
}

log "0/6 构建"
go build -o "$WORK/ppts.exe" ./cmd/ppts

log "1/6 起假 TTS 与 server"
$PY scripts/e2e/fake_tts.py "$TTS_PORT" "$WORK/tts-calls.jsonl" > "$WORK/tts.log" 2>&1 &
PIDS+=($!)
for i in $(seq 1 30); do
  grep -q "fake tts on" "$WORK/tts.log" 2>/dev/null && break
  sleep 1
done
grep -q "fake tts on" "$WORK/tts.log" || { cat "$WORK/tts.log"; fail "假 TTS 没起来"; }

set -a; . "$WORK/env.sh"; set +a
"$WORK/ppts.exe" server > "$WORK/server.log" 2>&1 &
PIDS+=($!)
for i in $(seq 1 40); do
  curl -s --noproxy '*' -m 2 "http://127.0.0.1:$PPTS_PORT/healthz" 2>/dev/null | grep -q '"status"' && break
  sleep 1
done
curl -s --noproxy '*' -m 2 "http://127.0.0.1:$PPTS_PORT/healthz" | grep -q '"status"' || { tail -20 "$WORK/server.log"; fail "server 没起来"; }

log "2/6 建项目"
PROJECT_ID=$($PY - "$(post /ppts.v1.ProjectService/Create '{"title":"音色试听联调"}')" <<'PY'
import json,sys
raw = sys.argv[1]
try:
    data = json.loads(raw)
except Exception:
    print("PARSE_FAIL:" + raw); raise SystemExit(0)
proj = data.get("project") or {}
print(proj.get("id") or "MISSING:" + raw)
PY
)
case "$PROJECT_ID" in
  MISSING*|PARSE_FAIL*|"") fail "建项目失败：$PROJECT_ID" ;;
esac
log "project_id=$PROJECT_ID"

log "3/6 建 TTS 网关（指向假上游）"
GW=$(post /api/model-gateways "{\"kind\":\"tts\",\"name\":\"fake-tts\",\"baseUrl\":\"http://127.0.0.1:$TTS_PORT\",\"model\":\"fake-tts-model\",\"apiKey\":\"fake-key\",\"isDefault\":true}")
echo "$GW" | grep -q '"gateway"' || fail "建网关失败：$GW"

log "4/6 试听一次（音色 + 130% 语速）"
CODE=$(status_of "/projects/$PROJECT_ID/voice-preview" '{"voice":"FunAudioLLM/CosyVoice2-0.5B:alex","ratePercent":130}' "$WORK/preview.wav")
[ "$CODE" = "200" ] || { cat "$WORK/preview.wav"; fail "试听 status=$CODE want 200"; }
head -c 4 "$WORK/preview.wav" | grep -q "RIFF" || fail "回包不是 WAV（前 4 字节不是 RIFF）"

DURATION=$(curl -s --noproxy '*' -D - -o /dev/null -X POST "http://127.0.0.1:$PPTS_PORT/projects/$PROJECT_ID/voice-preview" \
  -H 'Content-Type: application/json' -d '{"voice":"FunAudioLLM/CosyVoice2-0.5B:alex","ratePercent":130}' \
  | tr -d '\r' | awk -F': ' 'tolower($1)=="x-ppts-audio-duration-ms"{print $2}')
[ "$DURATION" = "1200" ] || fail "时长头=$DURATION want 1200（应从上游 WAV 解出，不是按字数估）"

MODEL=$(curl -s --noproxy '*' -D - -o /dev/null -X POST "http://127.0.0.1:$PPTS_PORT/projects/$PROJECT_ID/voice-preview" \
  -H 'Content-Type: application/json' -d '{"voice":"FunAudioLLM/CosyVoice2-0.5B:alex"}' \
  | tr -d '\r' | awk -F': ' 'tolower($1)=="x-ppts-preview-model"{print $2}')
[ "$MODEL" = "fake-tts-model" ] || fail "模型头=$MODEL want fake-tts-model（应为网关配置的模型）"

$PY - "$WORK/tts-calls.jsonl" <<'PY'
import json,sys
calls=[json.loads(l) for l in open(sys.argv[1],encoding='utf-8') if l.strip()]
assert calls, "上游一次都没被调用"
first=calls[0]
b=first["body"]
checks = {
  "path": (first["path"], "/v1/audio/speech"),
  # 密钥解错会在上游表现为 401，但界面只会说"供应商故障" —— 必须在这里看见真值。
  "auth": (first["auth"], "Bearer fake-key"),
  "model": (b.get("model"), "fake-tts-model"),
  "voice": (b.get("voice"), "FunAudioLLM/CosyVoice2-0.5B:alex"),
  "speed": (b.get("speed"), 1.3),
}
bad=[f"{k}={got!r} want {want!r}" for k,(got,want) in checks.items() if got != want]
assert not bad, "上游请求不符：" + "; ".join(bad)
assert b.get("input"), "input 为空"
print(f"上游调用 {len(calls)} 次，首条输入：{b['input']}")
PY

log "5/6 非法入参不得到上游（超长 / 缺音色）"
BEFORE=$(wc -l < "$WORK/tts-calls.jsonl")
LONG=$($PY -c "print('字'*121)")
CODE=$(status_of "/projects/$PROJECT_ID/voice-preview" "{\"voice\":\"v\",\"text\":\"$LONG\"}" "$WORK/too-long.json")
[ "$CODE" = "400" ] || fail "超长文本 status=$CODE want 400"
grep -q '"code":"text_too_long"' "$WORK/too-long.json" || fail "超长文本错误码不是 text_too_long：$(cat "$WORK/too-long.json")"
CODE=$(status_of "/projects/$PROJECT_ID/voice-preview" '{"voice":"  "}' "$WORK/no-voice.json")
[ "$CODE" = "400" ] || fail "缺音色 status=$CODE want 400"
AFTER=$(wc -l < "$WORK/tts-calls.jsonl")
[ "$BEFORE" = "$AFTER" ] || fail "非法入参仍打到了上游（$BEFORE → $AFTER）"

log "6/6 英文界面用英文示例句"
CODE=$(status_of "/projects/$PROJECT_ID/voice-preview" '{"voice":"v"}' "$WORK/en.json" -H 'X-PPTS-Language: en-US')
[ "$CODE" = "200" ] || fail "英文试听 status=$CODE want 200"
$PY - "$WORK/tts-calls.jsonl" <<'PY'
import json,sys
calls=[json.loads(l) for l in open(sys.argv[1],encoding='utf-8') if l.strip()]
last=calls[-1]["body"]["input"]
assert last.startswith("This is a short sample"), f"英文界面示例句 = {last!r}"
print("英文示例句 OK")
PY

printf '\nPASS：音色试听端到端通过（真实网关解析 / 真实 HTTP / 真实 WAV 解码）\n'
