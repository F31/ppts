#!/usr/bin/env bash
# 6.2 模型通道对照的一条命令链路。
#
# 没有 key 时它不空转、也不算假数：先跑三项不花钱的自检（校验器有牙 + 规则基线对账 +
# 分档工具对账），然后明确说「跳过真实对照」并以 0 退出。有 key 时跑完整链路：
# 多轮采样 → 分档 → 按页路由。
#
#   ./scripts/fidelity/llm_check.sh
#   PPTS_LLM_API_KEY=sk-... ./scripts/fidelity/llm_check.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

PY=${PYTHON:-python3}
SCRIPT=scripts/fidelity/bind_llm.py
OUT=tmp/q8/llm-rep
CACHE=tmp/llm-cache-rep
REPEAT=${PPTS_LLM_REPEAT:-5}

if [ ! -f tmp/q8/fid/elements.json ] || [ ! -f tmp/q7/fid/elements.json ]; then
  echo "缺夹具：tmp/q8/fid/elements.json 或 tmp/q7/fid/elements.json" >&2
  echo "这两个文件不在版本控制里，需要先跑元素提取。" >&2
  exit 2
fi

echo "== 1/5 校验器与评测链路自测（不花钱）=="
$PY "$SCRIPT" --selftest || exit 1

echo
echo "== 2/5 规则基线锚点对账（不花钱）=="
$PY "$SCRIPT" --rules-baseline --out-dir "$OUT" || exit 1

echo
echo "== 3/5 分档工具对账（不花钱，走产品代码）=="
# 分档只有一份实现（internal/animation/bind.classifyBand）。这里把它的输出与 Go 金标
# 对一遍：5 页、53 条绑定、全部 structured。对不上说明分档或绑定漂了，
# 后面的路由实验就是在错的输入上做的。
go run ./scripts/fidelity/bandcheck \
  -elements tmp/q8/fid/elements.json \
  -segments scripts/fidelity/q8-script.json \
  -out tmp/q8/band-orig.json || exit 1
$PY - tmp/q8/band-orig.json <<'PYEOF' || exit 1
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
pages = d["pages"]
total = sum(p["bound"] for p in pages)
bad = [p["index"] for p in pages if p["band"] != "structured"]
if len(pages) != 5 or total != 53 or bad:
    print("分档对账失败：页数 %d、绑定 %d、非结构化页 %s（期望 5 / 53 / 无）"
          % (len(pages), total, bad))
    sys.exit(1)
print("5 页全部 structured、绑定 53 条——与 Go 金标一致")
PYEOF

echo
echo "== 4/5 产品侧模型通道对账（不花钱，走 bind.RunRouted）=="
# 路由、六类拒绝、降级、结构收尾、系统提示词与离线脚本逐字一致，这几条都在单测里。
# 尤其是结构收尾：Python 侧 fill_structure 补 4 个底板，Go 侧必须补同样 4 个，
# 差一条就说明两边的「模型召回」量的不是同一件事。
go test ./internal/animation/bind/ -run 'TestRoute|TestModel|TestValidate|TestSystemPrompt' || exit 1
# 异步通道：降级三条（超时/网关错误/额度不足）、token 按真实用量结算、快照指纹随内容变化。
# 这几条是「预填不会因为模型而失败」与「预填不许偷偷烧额度」的唯一证据。
go test ./internal/app/ -run 'TestPrefill' || exit 1

echo
if [ -z "${PPTS_LLM_API_KEY:-}" ]; then
  echo "== 5/5 跳过真实模型对照：没有 PPTS_LLM_API_KEY =="
  echo "设置后重跑即可："
  echo "  PPTS_LLM_API_KEY=... $0"
  echo
  echo "想先确认链路通，可以用假端点（数字没有意义，只验证流程）："
  echo "  $PY scripts/fidelity/mock_llm_server.py --port 8799 &"
  echo "  $PY $SCRIPT --api-key fake --base-url http://127.0.0.1:8799 --out-dir tmp/llm-mock"
  exit 0
fi

# 混合讲稿：一半页保留结构词、一半消融。用脚本自己的构造器生成，保证与 --mix-pages 同源。
$PY - <<'PYEOF' || exit 1
import json, sys
sys.path.insert(0, "scripts/fidelity")
import bind_llm as L
a = L.load_assets()
mixed, _ = L.build_mixed(a["q8_seg"]["pages"], L.parse_mix("1,4"))
open("tmp/q8/mixed-script.json", "w", encoding="utf-8").write(
    json.dumps(mixed, ensure_ascii=False, indent=1))
print("混合讲稿：tmp/q8/mixed-script.json")
PYEOF

echo
echo "== 5/5 真实模型对照（$REPEAT 轮采样）=="
# 单轮不可信：同一 prompt 三次输出 6/11/7 条，召回能在 0.694 与 0.972 之间跳。
status=0
$PY "$SCRIPT" --mode model --variants baseline no-structure mixed \
  --repeat "$REPEAT" --out-dir "$OUT" --cache-dir "$CACHE" || status=1

echo
echo "-- 混合样本分档 --"
go run ./scripts/fidelity/bandcheck \
  -elements tmp/q8/fid/elements.json \
  -segments tmp/q8/mixed-script.json \
  -out tmp/q8/band-mixed.json || status=1

echo
echo "-- 按页路由（与全模型共用缓存，不额外花钱）--"
$PY "$SCRIPT" --mode route --variants mixed --repeat "$REPEAT" \
  --band-file tmp/q8/band-mixed.json --out-dir "$OUT" --cache-dir "$CACHE" || status=1

echo
if [ "$status" -eq 0 ]; then
  echo "路由判据（逐轮配对，取最差轮）与各档门槛都过了。"
else
  echo "有变体未过判据——报告在 $OUT。先看 spread 再看 delta：单轮数字不算结论。"
fi
exit "$status"
