#!/usr/bin/env bash
# 焦点建议的点击级验收（真浏览器 + 真实 React 组件）。
#
# 和 prefill_click.sh / import_report_ui.sh 一样，验的是「用户点下去以后看到了什么」：
# vite dev 提供真实的 ScriptEditor（只 mock 网络边界），无头 Chrome 通过 CDP 点按钮、
# 展开菜单、读回保存通道真正收到的 segments。
#
# 夹具不入库：由本脚本临时生成到 web/ 根（vite dev 的服务范围），结束时删除。
# 入库的是生成过程与判据，不是一次性产物。
#
# 用法：bash scripts/e2e/focus-suggest-ui.sh
# 可覆盖：VITE_PORT=5179 CDP_PORT=9337 CHROME=/path/to/chrome KEEP=1
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 2
REPO=$(pwd)
VITE_PORT=${VITE_PORT:-5179}
CDP_PORT=${CDP_PORT:-9337}
BASE="http://127.0.0.1:${VITE_PORT}"
HTML="$REPO/web/focus-suggest-harness.html"
TSX="$REPO/web/focus-suggest-harness.tsx"
WORK=${WORK:-"$REPO/tmp/focusui"}
SHOT="$WORK/suggest-shot.png"
PIDS=()
fail=0

mkdir -p "$WORK"

cleanup() {
  rm -f "$HTML" "$TSX"
  if [ "${KEEP:-0}" != 1 ]; then
    for pid in "${PIDS[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  fi
}
trap cleanup EXIT

log() { printf '== %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- 夹具
cat > "$HTML" <<'EOF'
<!doctype html>
<html lang="zh">
<head><meta charset="utf-8"><title>焦点建议点击级验收夹具</title></head>
<body><div id="root"></div><script type="module" src="/focus-suggest-harness.tsx"></script></body>
</html>
EOF

cat > "$TSX" <<'EOF'
// 一次性夹具（由 focus-suggest-ui.sh 生成，不入库）。
// 挂载的是产品自己的 ScriptEditor：只有 commit 与建议 GET 被替成内存实现，
// 其余（i18n、焦点三态、防抖自动保存、菜单渲染）全是线上代码。
import { createRoot } from 'react-dom/client';
import { useState } from 'react';
import './src/styles.css';
import { I18nProvider } from './src/i18n';
import { ScriptEditor } from './src/ScriptEditor';
import type { FocusSuggestion, FocusTarget, ScriptRevision, ScriptSegment } from './src/types';

localStorage.setItem('ppts.lang', 'zh');

const targets: FocusTarget[] = [
  { shapeId: 's1', barId: '', kind: 'text', label: '标题：本季度回顾', box: [0, 0, 1, 1] },
  { shapeId: 's2', barId: '', kind: 'text', label: '第一条：华东 174 秒', box: [0, 0, 1, 1] },
  { shapeId: 'chart-1', barId: 'p1-s2-b0', kind: 'bar', label: 'Q1 · 174', box: [0, 0, 1, 1] }
];

// 四段各代表一种真实情形：唯一候选 / 多候选 / 人工已指 / 候选清单外的脏目标。
const suggestionData: FocusSuggestion[] = [
  { segmentId: 'seg-a', candidates: [{ shapeId: 's1', source: 'literal' }], reason: '规则唯一命中' },
  { segmentId: 'seg-b', candidates: [{ shapeId: 's1', source: 'literal' }, { shapeId: 's2', source: 'keyword' }], reason: '命中 2 个' },
  { segmentId: 'seg-c', candidates: [{ shapeId: 's2', source: 'literal' }], reason: '规则唯一命中' },
  { segmentId: 'seg-d', candidates: [{ shapeId: 'ghost', source: 'literal' }], reason: '命中不可选目标' }
];

const baseSegments: ScriptSegment[] = [
  { segmentId: 'seg-a', slideId: 'slide-1', displayText: '本季度华东区平均耗时 174 秒。', spokenText: '本季度华东区平均耗时 174 秒。', status: 'draft' },
  { segmentId: 'seg-b', slideId: 'slide-1', displayText: '三地差距主要来自审批环节。', spokenText: '三地差距主要来自审批环节。', status: 'draft' },
  { segmentId: 'seg-c', slideId: 'slide-1', displayText: '华南区表现最稳定。', spokenText: '华南区表现最稳定。', status: 'draft', focus: { shapeId: 's1' } },
  { segmentId: 'seg-d', slideId: 'slide-1', displayText: '下一步压缩审批链路。', spokenText: '下一步压缩审批链路。', status: 'draft' }
];

type Harness = { requests: number; commits: ScriptSegment[][]; band: string };
const H: Harness = {
  requests: 0,
  commits: [],
  band: new URLSearchParams(location.search).get('band') || 'structured'
};
(window as unknown as { __H: Harness }).__H = H;

function App() {
  const [suggestions, setSuggestions] = useState<FocusSuggestion[] | null>(null);
  const [status, setStatus] = useState<'idle' | 'loading' | 'ready' | 'failed'>('idle');
  const [script, setScript] = useState<ScriptRevision>({
    projectId: 'p1', slideId: 'slide-1', language: 'zh-CN', revision: 1, status: 'draft',
    segments: baseSegments
  });
  const onNeed = () => {
    H.requests += 1;
    setStatus('loading');
    window.setTimeout(() => {
      setSuggestions(H.band === 'structured' ? suggestionData : []);
      setStatus('ready');
    }, 60);
  };
  const commit = async (slideId: string, segments: ScriptSegment[], expectedRevision: number) => {
    // 存的是「真正发出去的 JSON」：focus:null 只有在序列化之后才看得出来。
    H.commits.push(JSON.parse(JSON.stringify(segments)) as ScriptSegment[]);
    const saved: ScriptRevision = { ...script, slideId, revision: expectedRevision + 1, segments };
    setScript(saved);
    return saved;
  };
  return (
    <I18nProvider>
      <ScriptEditor
        script={script}
        slideTitle="第 1 页 · 季度回顾"
        onChange={setScript}
        commit={commit}
        canEdit={true}
        focusTargets={targets}
        focusTargetsStatus="ready"
        focusSuggestions={suggestions}
        focusSuggestionsStatus={status}
        focusSuggestionsBand={H.band}
        onNeedFocusSuggestions={onNeed}
      />
    </I18nProvider>
  );
}

createRoot(document.getElementById('root')!).render(<App />);
EOF

# ---------------------------------------------------------------- 服务
log "1/3 起 vite dev（端口 ${VITE_PORT}）"
( cd "$REPO/web" && npx vite --port "$VITE_PORT" --strictPort --host 127.0.0.1 ) \
  > "$WORK/vite.log" 2>&1 &
PIDS+=($!)
ready=0
for i in $(seq 1 60); do
  # -f 不能省：curl 对 404 也返回 0，端口被别的进程占着时会误判成「vite 已就绪」。
  if curl -fsS --noproxy '*' -m 1 -o /dev/null "$BASE/focus-suggest-harness.html"; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { tail -20 "$WORK/vite.log"; die "vite dev 没起来"; }

log "2/3 起无头浏览器（CDP ${CDP_PORT}）"
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
[ -n "$CHROME" ] || die "没找到 Chrome/Edge，可用 CHROME=/path/to/chrome 指定"

# Chrome 是原生 Windows 程序：--user-data-dir 必须是绝对的 Windows 路径，
# 否则进程一闪而过、调试端口永远不监听（见 prefill_click.sh 的同一处注释）。
PROFILE=$(mktemp -d 2>/dev/null || echo "$WORK/chrome-$$")
command -v cygpath >/dev/null 2>&1 && PROFILE=$(cygpath -w "$PROFILE")
"$CHROME" --headless=new --remote-debugging-port="$CDP_PORT" \
  --user-data-dir="$PROFILE" --no-first-run --no-default-browser-check \
  --disable-gpu --disable-extensions --disable-background-networking \
  --window-size=1280,1400 about:blank > "$WORK/chrome.log" 2>&1 &
PIDS+=($!)
ready=0
for i in $(seq 1 30); do
  if curl -s --noproxy '*' -m 1 -o /dev/null "http://127.0.0.1:${CDP_PORT}/json/version"; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { tail -20 "$WORK/chrome.log"; die "无头浏览器没起来"; }

log "3/3 真浏览器里点下去，核对界面与保存通道"
BASE="$BASE" CDP_PORT="$CDP_PORT" SHOT="$SHOT" \
  node "$REPO/scripts/e2e/focus_suggest_ui_check.mjs" || fail=1

if [ "${KEEP:-0}" = 1 ]; then
  log "KEEP=1：服务与浏览器保持运行"
  wait
fi
[ "$fail" = 0 ] || die "点击级断言未通过（截图：$SHOT）"
log "通过"
