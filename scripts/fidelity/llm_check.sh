#!/usr/bin/env bash
# 6.2 模型通道对照的一条命令链路。
#
# 没有 key 时它不空转、也不算假数：先跑两项不花钱的自检（校验器有牙 + 规则基线对账），
# 然后明确说「跳过真实对照」并以 0 退出。有 key 时两种模式各跑一遍。
#
#   ./scripts/fidelity/llm_check.sh
#   PPTS_LLM_API_KEY=sk-... ./scripts/fidelity/llm_check.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

PY=${PYTHON:-python3}
SCRIPT=scripts/fidelity/bind_llm.py
OUT=tmp/q8/llm

if [ ! -f tmp/q8/fid/elements.json ] || [ ! -f tmp/q7/fid/elements.json ]; then
  echo "缺夹具：tmp/q8/fid/elements.json 或 tmp/q7/fid/elements.json" >&2
  echo "这两个文件不在版本控制里，需要先跑元素提取。" >&2
  exit 2
fi

echo "== 1/3 校验器与评测链路自测（不花钱）=="
$PY "$SCRIPT" --selftest || exit 1

echo
echo "== 2/3 规则基线锚点对账（不花钱）=="
$PY "$SCRIPT" --rules-baseline --out-dir "$OUT" || exit 1

echo
if [ -z "${PPTS_LLM_API_KEY:-}" ]; then
  echo "== 3/3 跳过真实模型对照：没有 PPTS_LLM_API_KEY =="
  echo "设置后重跑即可："
  echo "  PPTS_LLM_PROVIDER=siliconflow PPTS_LLM_API_KEY=sk-... $0"
  echo
  echo "想先确认链路通，可以用假端点（数字没有意义，只验证流程）："
  echo "  $PY scripts/fidelity/mock_llm_server.py --port 8799 &"
  echo "  $PY $SCRIPT --api-key fake --base-url http://127.0.0.1:8799 --out-dir tmp/llm-mock"
  exit 0
fi

echo "== 3/3 真实模型对照（先 model，再 rule+model）=="
status=0
$PY "$SCRIPT" --mode model --out-dir "$OUT" --cache-dir tmp/llm-cache || status=1
echo
$PY "$SCRIPT" --mode rule+model --out-dir "$OUT" --cache-dir tmp/llm-cache || status=1

echo
if [ "$status" -eq 0 ]; then
  echo "两种模式的门槛都过了。"
else
  echo "有变体未过门槛——报告在 $OUT，先看 delta 再看绝对数。"
fi
exit "$status"
