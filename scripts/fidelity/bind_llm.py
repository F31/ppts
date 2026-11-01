#!/usr/bin/env python3
"""6.2 模型通道对照：模型绑定的离线评测（不碰产品代码）。

## 这个脚本在回答什么

规则通道已经测完了：真实讲义 0.972，删掉 13 处结构词后 0.667，合成口语样本 0.533。
现在要回答的是「值不值得接模型通道」——**模型能不能在规则失灵的那一档上补回来**。

对照必须同表、同口径、同 golden：

| 变体 | 规则现值 | 模型合格线 |
|---|---|---|
| q8 去全部结构词（主战场） | 0.667 | 内容召回 ≥ 0.75 且精确 ≥ 0.90 |
| q7 合成口语样本 | 0.533 → 现 0.353（见下） | 内容召回 ≥ 0.70 |
| q8 原始讲稿（不能倒退） | 0.972 | ≥ 0.922（不低 0.05） |

0.533 是 19 元素旧夹具的数字；当前 `tmp/q7` 是 23 元素，`--rules-baseline` 实测 0.353。
所以口语样本的门槛只做「软锚点」：只打印差值、不判红——硬校验一个已失效的数字，
只会训练人「看到红就改阈值」。q8 的两档是真锚点，Go 侧金标测试也钉着它们。

数字不是这个脚本自己算的：`compare` / `summarize` 直接从 `bind_elements.py`
import，与规则实验逐字同口径。自己再写一遍指标，两边就不是同一把尺子了。

## 三条设计约束（写进代码，不只是写进文档）

**一、模型只输出 id 映射。** 不许它输出时间、坐标、改写后的文本。输出形状只有一种：

    {"bindings":[{"elementId":"p2-s5","segmentId":"p2-q1","confidence":0.8}]}

输出越窄，校验器越能做成纯函数。产品侧的 `evaluation` 也就不必信模型的自我描述。

**二、确定性只能靠缓存。** 规格要求「同一 (page, segments) 同一结果」。温度 0 +
固定模型版本仍然不保证，唯一能兑现的是缓存：key = sha256(model|promptVersion|输入)。
命中即同结果；未命中就请求。所以这个脚本重跑不花钱，也不会给出两个不同的数
（除非 `--no-cache`，那时报告里会标 `cacheHits=0`）。

**三、模型的 confidence 只用来排序提示，不参与任何门槛。** 它是自我评价，
与真实正确率没有校准关系（design §10.5 的同一条原则）。门槛只看金标对照出来的
召回与精确。

## 无 key 时怎么办

不伪造数字。三个不需要 key 的出口：

    --selftest         校验器 + 评测链路 + 门槛判定的断言（证明「门禁有牙」）
    --rules-baseline   用同一套评测路径重算规则基线，与 0.972 / 0.667 对账
    --dry-run          打印将要发送的 prompt 与 token 估算，不请求
    （无参数）         缺 key 时 exit 2，并列出需要的环境变量

## 用法

    # 有 key：跑全部三个变体，出表
    PPTS_LLM_PROVIDER=siliconflow PPTS_LLM_API_KEY=sk-... \
    python3 scripts/fidelity/bind_llm.py --out-dir tmp/q8/llm

    # 只跑主战场；或走「规则先绑、模型只补空位」
    python3 scripts/fidelity/bind_llm.py --variants no-structure
    python3 scripts/fidelity/bind_llm.py --mode rule+model

    # 不花钱的三件事
    python3 scripts/fidelity/bind_llm.py --selftest
    python3 scripts/fidelity/bind_llm.py --rules-baseline
    python3 scripts/fidelity/bind_llm.py --dry-run

    # 没有任何 key 时验证整条链路（假装是模型端点）
    python3 scripts/fidelity/mock_llm_server.py --port 8799 &
    python3 scripts/fidelity/bind_llm.py --api-key fake \
        --base-url http://127.0.0.1:8799 --out-dir tmp/llm-mock
"""
import argparse
import hashlib
import json
import re
import statistics
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

# 指标口径与规则实验共用同一份实现——不重写，否则两张表不可比。
import bind_elements as rules  # noqa: E402
from ablate_structure import build_variant  # noqa: E402

PROMPT_VERSION = "bind-v1"

DEFAULT_BASE_URL = "https://api.siliconflow.cn"
DEFAULT_MODEL = "Qwen/Qwen2.5-7B-Instruct"

# 元素文本截断：真实讲义里有整段正文塞进一个 shape 的，全量喂会把单页 prompt 顶到
# 几千 token，而超出这一段的文字对「绑到哪一段」几乎不增信息。
MAX_ELEM_CHARS = 120
MAX_SEG_CHARS = 400

SYSTEM_PROMPT = """你在做一件很窄的事：把幻灯片页面上看得见的元素，绑到讲解稿的段落上。

规则：
1. 只能使用我给你的 elementId 与 segmentId，不许创造新的 id。
2. 每个元素最多绑一个段落；每个段落可以绑多个元素。
3. 不要绑页码、页眉页脚、图注、资料来源、联系方式、版权声明这类元信息。
4. 不要绑没有文字、也没有图形的纯装饰块（铺底背景、边角色块）。
5. 讲解顺序大体就是元素的阅读顺序；但一句话同时讲到几件东西时，允许多个元素绑到同一段。
6. 宁可少绑，也不要猜。拿不准的元素就不要出现在结果里。

只输出一个 JSON 对象，形状严格如下，不要任何解释文字、不要 markdown 代码块：
{"bindings":[{"elementId":"...","segmentId":"...","confidence":0.0}]}
confidence 是你自己的把握（0~1）。它不会被当作准确率展示给用户，只用来排序复核。"""


# --------------------------------------------------------------------------
# 输入构造：紧凑行式，不用 XML/JSON 描述结构
# --------------------------------------------------------------------------

def _clip(s, n):
    s = (s or "").strip().replace("\r", " ").replace("\n", " / ")
    return s if len(s) <= n else s[: n - 1] + "…"


def build_page_input(page, segments):
    """一页的输入文本。行式而不是 JSON：省 token，也少一层模型需要遵守的嵌套结构。"""
    lines = ["SLIDE %s" % (page.get("index", "?"))]
    for e in page.get("elements") or []:
        text = _clip(e.get("text"), MAX_ELEM_CHARS)
        lines.append("E %s | %s | %s" % (e.get("id"), e.get("kind") or "shape", text or "(no text)"))
    lines.append("SEG")
    for s in segments:
        lines.append("S %s | %s" % (s.get("id"), _clip(s.get("text"), MAX_SEG_CHARS)))
    return "\n".join(lines)


def build_page_input_with_rules(page, segments, pairs):
    """规则已绑的那一份：把规则结果也告诉模型，让它只补空位。

    这是产品侧的真实形态（规则先跑，模型只填空），也是 `rule+model` 变体的输入。
    """
    bound = {page["elements"][ei]["id"]: segments[si]["id"] for ei, si, _ in pairs}
    lines = ["SLIDE %s" % (page.get("index", "?"))]
    for e in page.get("elements") or []:
        eid = e.get("id")
        text = _clip(e.get("text"), MAX_ELEM_CHARS)
        mark = "BOUND->%s" % bound[eid] if eid in bound else "FREE"
        lines.append("E %s | %s | %s | %s" % (eid, e.get("kind") or "shape", text or "(no text)", mark))
    lines.append("SEG")
    for s in segments:
        lines.append("S %s | %s" % (s.get("id"), _clip(s.get("text"), MAX_SEG_CHARS)))
    return "\n".join(lines)


# --------------------------------------------------------------------------
# 校验器：纯函数，四条拒绝规则 + 两条统计
# --------------------------------------------------------------------------

REJECT_ORDER = ["not_object", "bad_element_id", "bad_segment_id", "duplicate",
                "rule_locked", "aux_element", "no_text", "bad_confidence"]


def extract_json(raw):
    """模型偶尔会包 ```json 或加一句前言。剥掉，但只剥这一层，不做模糊修补。"""
    if raw is None:
        return None
    s = raw.strip()
    m = re.search(r"```(?:json)?\s*(.+?)```", s, re.S)
    if m:
        s = m.group(1).strip()
    try:
        return json.loads(s)
    except json.JSONDecodeError:
        pass
    # 兜底：从第一个 { 到最后一个 }。仍解析不了就返回 None（计 invalid），
    # 不试图用正则把残缺的 JSON 拼起来——那会把模型的格式错误伪装成成功。
    i, j = s.find("{"), s.rfind("}")
    if i >= 0 and j > i:
        try:
            return json.loads(s[i: j + 1])
        except json.JSONDecodeError:
            return None
    return None


def validate(obj, page, segments, allow_ids=None):
    """把模型输出收敛成 {segId: [(eid, 0)]}，并回报每条被拒的原因。

    allow_ids 非空时（rule+model 变体），只接受这些 elementId——规则已经绑过的
    元素对模型是只读的，覆盖只会新增看不见的错绑。
    """
    stats = Counter()
    pred = {}
    seen = set()
    monotonic_violations = 0

    elems = {e.get("id"): e for e in (page.get("elements") or [])}
    seg_ids = {s.get("id") for s in segments}
    order = {e.get("id"): i for i, e in enumerate(page.get("elements") or [])}

    if not isinstance(obj, dict) or not isinstance(obj.get("bindings"), list):
        stats["not_object"] += 1
        return pred, stats, monotonic_violations

    last = -1
    for item in obj["bindings"]:
        if not isinstance(item, dict):
            stats["not_object"] += 1
            continue
        eid = item.get("elementId")
        sid = item.get("segmentId")
        if eid not in elems:
            stats["bad_element_id"] += 1
            continue
        if sid not in seg_ids:
            stats["bad_segment_id"] += 1
            continue
        if eid in seen:
            stats["duplicate"] += 1
            continue
        if allow_ids is not None and eid not in allow_ids:
            # 规则已绑的元素对模型只读。单列 rule_locked 而不是并进 duplicate：
            # 「模型重复输出了一个元素」和「模型想覆盖规则」是两种完全不同的失败，
            # 前者是格式问题，后者说明它不守约束——混在一起就看不出来了。
            stats["rule_locked"] += 1
            continue
        if rules.is_aux(elems[eid]):
            stats["aux_element"] += 1
            continue
        if not (elems[eid].get("text") or "").strip():
            stats["no_text"] += 1
            continue
        conf = item.get("confidence")
        if conf is not None and not isinstance(conf, (int, float)):
            stats["bad_confidence"] += 1
            continue
        if isinstance(conf, (int, float)) and not (0.0 <= float(conf) <= 1.0):
            stats["bad_confidence"] += 1
            continue

        seen.add(eid)
        pred.setdefault(sid, []).append((eid, 0))
        # 单调性只统计、不强制：模型可能故意跨段合并（一段话讲两件事再回来）。
        # 但「大量倒退」说明它没按阅读顺序看页面，这个信号要报出来。
        cur = order.get(eid, 0)
        if cur < last:
            monotonic_violations += 1
        last = max(last, cur)

    for sid in pred:
        pred[sid].sort(key=lambda p: order.get(p[0], 0))
    return pred, stats, monotonic_violations


def count_items(obj):
    """invalid 率的分母。每页至少算 1 条：整页输出非 JSON 时也要有分母，
    否则 rejectRatio = 0/0 = 0.0，「invalid ≤ 5%」会假通过。"""
    if isinstance(obj, dict) and isinstance(obj.get("bindings"), list):
        return max(1, len(obj["bindings"]))
    return 1


def validate_stats_total(stats):
    return sum(v for k, v in stats.items() if k in REJECT_ORDER)


# --------------------------------------------------------------------------
# 模型调用：OpenAI 兼容 /v1/chat/completions，带磁盘缓存
# --------------------------------------------------------------------------

def cache_key(model, prompt_version, system, user):
    h = hashlib.sha256()
    for part in (model, prompt_version, system, user):
        h.update(part.encode("utf-8"))
        h.update(b"\x00")
    return h.hexdigest()[:32]


def chat(base_url, api_key, model, system, user, timeout=120):
    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0,
        "top_p": 1,
        "max_tokens": 1024,
        "response_format": {"type": "json_object"},
        "stream": False,
    }
    req = urllib.request.Request(
        base_url.rstrip("/") + "/v1/chat/completions",
        data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + api_key,
        },
        method="POST",
    )
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    latency_ms = int((time.time() - t0) * 1000)
    content = ""
    try:
        content = payload["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        content = ""
    usage = payload.get("usage") or {}
    return {
        "raw": content,
        "latencyMs": latency_ms,
        "promptTokens": usage.get("prompt_tokens", 0),
        "completionTokens": usage.get("completion_tokens", 0),
        "model": payload.get("model") or model,
    }


def call_cached(cache_dir, key, fn):
    """命中即返回（含 latencyMs，但标 cached=True）；未命中则调用并落盘。"""
    cache_dir = Path(cache_dir)
    path = cache_dir / (key + ".json")
    if path.exists():
        try:
            rec = json.loads(path.read_text(encoding="utf-8"))
            rec["cached"] = True
            return rec
        except (json.JSONDecodeError, OSError):
            pass
    rec = fn()
    rec["cached"] = False
    rec["at"] = time.strftime("%Y-%m-%dT%H:%M:%S")
    cache_dir.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(rec, ensure_ascii=False, indent=1), encoding="utf-8")
    return rec


# --------------------------------------------------------------------------
# 规则基线：与规则实验同一条命令，不在这里重算
# --------------------------------------------------------------------------

def rule_pairs(page, segments):
    """跑一遍 v2 规则，得到 (pairs, 指标)。规则侧必须现场算，不能硬编码旧数字。

    顺序、上下文、去重三处都照抄 `bind_elements.main`：任何一处不同，基线就不是
    0.972 那个数了，而「模型比规则好多少」这个结论全靠这个基线。
    """
    elements = page["elements"]
    pairs, _ = rules.align(elements, segments)
    pairs = [p for p in pairs if not rules.is_aux(elements[p[0]])]
    bound = {elements[ei]["id"]: si for ei, si, _ in pairs}
    count_pairs = rules.count_bind(elements, segments, bound)
    bound.update({elements[ei]["id"]: si for ei, si, _ in count_pairs})
    ordinal_pairs = rules.ordinal_bind(elements, segments, bound)
    bound.update({elements[ei]["id"]: si for ei, si, _ in ordinal_pairs})
    spatial_pairs = rules.spatial_bind(elements, segments, bound)
    bound.update({elements[ei]["id"]: si for ei, si, _ in spatial_pairs})
    sibling_pairs = rules.sibling_bind(
        elements, segments, pairs + count_pairs + ordinal_pairs + spatial_pairs)
    bound.update({elements[ei]["id"]: si for ei, si, _ in sibling_pairs})
    # 先标题先验、再结构推断：容器的落点依赖它装着的元素，而那个元素可能正是
    # 靠标题先验绑上的。反过来跑，卡片就无主可依（口语版里实测漏掉）。
    prior_pairs = rules.title_prior(
        elements, segments, pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs)
    struct_pairs = rules.structural_bind(
        elements, segments,
        pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs + prior_pairs)
    merged = (pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs
              + prior_pairs + struct_pairs)
    # 按元素去重、保留先出现的来源：同一元素出现两条 clip 会在轨道上叠两次淡入。
    seen, dedup = set(), []
    for p in merged:
        eid = elements[p[0]]["id"]
        if eid in seen:
            continue
        seen.add(eid)
        dedup.append(p)
    return dedup


def pred_from_pairs(page, segments, pairs):
    """规则 pairs（元素序, 段序, 相似度）→ 与模型同形状的 pred，供 compare 用。"""
    pred = {}
    for ei, si, _ in sorted(pairs):
        pred.setdefault(segments[si]["id"], []).append((page["elements"][ei]["id"], 0))
    return pred


def score(page, segments, pred, gold_page):
    # 第三个参数要的是「元素 id → (段 id, role)」，不是讲稿页本身。
    # 传错一次，全部绑定都会被判成 extra（golden 查不到 ⇒ 每条都算误绑）。
    gold = rules.golden_binding(gold_page)
    rows = rules.compare(pred, gold, page["elements"])
    return rows, rules.summarize(rows)


def empty_summary(n):
    return {"elements": n, "exact": 0, "wrong_seg": 0, "missed": 0, "extra": 0,
            "both_skip": n, "recall": None, "need": 0,
            "recall_content": None, "need_content": 0, "precision": None}


def merge_summaries(items):
    """跨页汇总：召回与精确必须按分子分母合并，不能对小数取平均。

    5 页里 3 页满分、2 页零分，平均小数的结果与按条合并差得远，而后者才是
    「这份讲义上整体绑对了多少」。
    """
    tot = Counter()
    hit_content = need_content = 0
    hit_all = need_all = 0
    for s in items:
        for k in ("elements", "exact", "wrong_seg", "missed", "extra", "both_skip"):
            tot[k] += s.get(k, 0)
        need = s.get("need") or 0
        need_all += need
        hit_all += round((s.get("recall") or 0) * need)
        nc = s.get("need_content") or 0
        need_content += nc
        hit_content += round((s.get("recall_content") or 0) * nc)
    denom = tot["exact"] + tot["wrong_seg"] + tot["extra"]
    return {
        "elements": tot["elements"],
        "exact": tot["exact"],
        "wrong_seg": tot["wrong_seg"],
        "missed": tot["missed"],
        "extra": tot["extra"],
        "both_skip": tot["both_skip"],
        "recall": round(hit_all / need_all, 3) if need_all else None,
        "need": need_all,
        "recall_content": round(hit_content / need_content, 3) if need_content else None,
        "need_content": need_content,
        "precision": round(tot["exact"] / denom, 3) if denom else None,
    }


# --------------------------------------------------------------------------
# 门槛
# --------------------------------------------------------------------------

# 每条都是「不过就不上线」。门槛是绝对数，不跟着规则现值浮动——否则规则一退步，
# 模型的及格线也跟着退步，等于没有门槛。规则现值由 judge 单独带出来做对照。
GATES = {
    "no-structure": [
        ("recall_content", 0.75, "内容召回 ≥ 0.75（主战场）"),
        ("precision", 0.90, "精确 ≥ 0.90（错绑不可见）"),
    ],
    "colloquial": [
        ("recall_content", 0.70, "内容召回 ≥ 0.70（口语样本）"),
    ],
    "baseline": [
        ("recall_content", 0.922, "内容召回 ≥ 0.922（不得比规则低 0.05）"),
    ],
}


def judge(variant_name, summary, reject_ratio=None, p95_ms=None, rule_summary=None):
    """门槛判定。每条同时带出规则的现值与差值。

    门槛是绝对数（「模型到 0.75 才算可用」），但只看绝对数会把两件事混起来：
    模型真的补上了缺口，还是基线本身变了。附上 delta，报告自己就能说明这一点。
    """
    out = []
    for field, need, label in GATES.get(variant_name, []):
        got = summary.get(field)
        base = (rule_summary or {}).get(field)
        ok = got is not None and got >= need
        out.append({"name": label, "need": need, "got": got,
                    "rules": base,
                    "delta": None if (got is None or base is None) else round(got - base, 3),
                    "pass": ok})
    if variant_name in GATES and reject_ratio is not None:
        ok = reject_ratio <= 0.05
        out.append({"name": "输出合法率 ≥ 95%（invalid ≤ 5%）",
                    "need": 0.05, "got": round(reject_ratio, 4), "pass": ok})
    if variant_name in GATES and p95_ms is not None:
        out.append({"name": "P95 延迟 ≤ 8000ms/页",
                    "need": 8000, "got": p95_ms, "pass": p95_ms <= 8000})
    return out


# 规则侧的已知数字。`--rules-baseline` 会用本脚本自己的评测路径重算一遍并对账：
# 对不上就是脚本或规则漂了，不是「数字变了」。
#
# 只有 q8 的两档是硬锚点：它们同时被 Go 侧的金标测试（bind_golden_test.go）钉住，
# 换夹具会先让 Go 测试红，所以这里对不上一定是本脚本的问题。
#
# colloquial 是**软锚点**：README 记的 0.533 来自 19 元素的旧夹具，而当前 tmp/q7
# 是 23 元素，实测 0.353/0.75。差 0.18 不是脚本错，是夹具换了、README 表没跟着更新。
# 硬校验一个已经失效的数字只会训练人「看到红就改阈值」，所以只记录、不判红。
ANCHORS = {
    "baseline": (0.972, 0.962),
    "no-structure": (0.667, 0.902),
}
SOFT_ANCHORS = {
    "colloquial": (0.533, 0.533),
}
ANCHOR_EPS = 0.001


def rules_baseline(args):
    """不需要 key 的出口：用与模型对照完全相同的评测路径，重算规则基线。

    这是脚本正确性的锚。如果这里的 0.972 / 0.667 / 0.533 对不上，那么后面
    「模型比规则高多少」全是错的——所以宁可在这里直接红。
    """
    a = load_assets()

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    report = {"meta": {"mode": "rules-baseline", "at": time.strftime("%Y-%m-%dT%H:%M:%S")},
              "variants": []}
    drift = []

    print("%-28s %8s %8s   %s" % ("变体", "内容召回", "精确", "exact/wrong/missed/extra/skip"))
    for name in args.variants:
        pages = variant_pages(name, a["q8_seg"], a["q7_seg"], a["q8_elem"],
                              a["q7_elem"], a["q7_colloquial"])
        sums, per = [], []
        for idx, page, segments, gold_page in pages:
            pairs = rule_pairs(page, segments)
            pred = pred_from_pairs(page, segments, pairs)
            rows, summ = score(page, segments, pred, gold_page)
            sums.append(summ)
            per.append({"index": idx, "model": summ, "rows": rows if args.show_rows else None})
        total = merge_summaries(sums)
        print("%-28s %8s %8s   %d/%d/%d/%d/%d"
              % (VARIANT_LABELS[name], total["recall_content"], total["precision"],
                 total["exact"], total["wrong_seg"], total["missed"],
                 total["extra"], total["both_skip"]))

        anchor = ANCHORS.get(name)
        soft = SOFT_ANCHORS.get(name)
        if anchor:
            got = (total["recall_content"], total["precision"])
            if got[0] is None or got[1] is None or any(
                    abs(g - w) > ANCHOR_EPS for g, w in zip(got, anchor)):
                drift.append("%s: 实测 %s，锚点 %s" % (name, got, anchor))
        elif soft:
            got = (total["recall_content"], total["precision"])
            print("    软锚点 %s（README 旧夹具口径）：实测 %s，差 %s"
                  % (soft, got, tuple(round(g - w, 3) for g, w in zip(got, soft))))
        report["variants"].append({"variant": name, "label": VARIANT_LABELS[name],
                                   "model": total, "pages": per,
                                   "anchor": anchor, "softAnchor": soft})

    (out_dir / "report-rules-baseline.json").write_text(
        json.dumps(report, ensure_ascii=False, indent=1), encoding="utf-8")

    if drift:
        print("\nDRIFT 规则基线偏离锚点：")
        for d in drift:
            print("  " + d)
        print("先查规则是否被改动，再谈模型对照——基线不准，后面的差值没有意义。")
        return 1
    print("\n锚点对账通过（±%.3f）。报告：%s"
          % (ANCHOR_EPS, out_dir / "report-rules-baseline.json"))
    return 0


# --------------------------------------------------------------------------
# 变体定义
# --------------------------------------------------------------------------

def load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def load_assets():
    """五个夹具一次读全。逐处自己 load 最容易漏一个文件——colloquial 第一版就漏了。

    缺文件时给一句能照做的提示，而不是让 traceback 停在 FileNotFoundError：
    `tmp/` 不在版本控制里，别人 clone 下来必然没有这两个元素文件。
    """
    paths = {
        "q8_seg": "scripts/fidelity/q8-script.json",
        "q7_seg": "scripts/fidelity/q7-script.json",
        "q7_colloquial": "scripts/fidelity/q7-script-colloquial.json",
        "q8_elem": "tmp/q8/fid/elements.json",
        "q7_elem": "tmp/q7/fid/elements.json",
    }
    out = {}
    missing = []
    for key, path in paths.items():
        p = Path(path)
        if not p.exists():
            missing.append(path)
            continue
        out[key] = load(p)
    if missing:
        raise SystemExit(
            "缺夹具文件：\n  " + "\n  ".join(missing) +
            "\nscripts/fidelity/ 下的讲稿在版本控制里，tmp/ 下的元素文件不在。"
            "\n元素文件要先由元素提取流程生成；缺了就没法算召回。")
    return out


def variant_pages(name, q8_seg, q7_seg, q8_elem, q7_elem, q7_colloquial):
    """返回 [(page_index, page, segments, gold_page)]。

    golden 一律取**未扰动**的那一份：扰动只改讲稿用词，不改「哪个元素该在哪一段出现」。
    口语变体要拿 `q7-script-colloquial.json` 的分段，golden 仍取 `q7-script.json`
    ——第一版这里传错，所谓「口语样本」跑的是原稿，数字照抄了却没有意义。
    """
    if name == "baseline":
        return list(zip(range(len(q8_elem)), q8_elem, [p["segments"] for p in q8_seg["pages"]],
                        q8_seg["pages"]))
    if name == "no-structure":
        ablated, _ = build_variant(q8_seg["pages"], ("ordinal", "count", "spatial"))
        return list(zip(range(len(q8_elem)), q8_elem, [p["segments"] for p in ablated["pages"]],
                        q8_seg["pages"]))
    if name == "colloquial":
        return list(zip(range(len(q7_elem)), q7_elem,
                        [p["segments"] for p in q7_colloquial["pages"]], q7_seg["pages"]))
    raise SystemExit("未知变体：%s" % name)


VARIANT_LABELS = {
    "baseline": "q8 原始讲稿（不能倒退）",
    "no-structure": "q8 去全部结构词（主战场）",
    "colloquial": "q7 合成口语样本",
}


def run_variant(name, pages, args, cache_dir):
    per_page = []
    reject_total = Counter()
    total_items = 0
    latencies = []
    requests = 0
    cache_hits = 0

    for idx, page, segments, gold_page in pages:
        rp = rule_pairs(page, segments)  # 别叫 rule_pairs：同名局部变量会让函数本身不可见
        rule_pred = pred_from_pairs(page, segments, rp)
        _, rule_sum = score(page, segments, rule_pred, gold_page)

        if args.mode == "rule+model":
            # 规则已绑的元素对模型只读：只允许它填规则留下的空位。
            user = build_page_input_with_rules(page, segments, rp)
            bound_ids = {page["elements"][ei]["id"] for ei, _, _ in rp}
            allow = {e["id"] for e in page["elements"]} - bound_ids
        else:
            allow = None
            user = build_page_input(page, segments)

        key = cache_key(args.model, PROMPT_VERSION + "|" + args.mode, SYSTEM_PROMPT, user)
        rec = call_cached(cache_dir, key, lambda: chat(
            args.base_url, args.api_key, args.model, SYSTEM_PROMPT, user, args.timeout))
        requests += 1
        if rec.get("cached"):
            cache_hits += 1
        latencies.append(rec.get("latencyMs") or 0)

        obj = extract_json(rec.get("raw"))
        pred, stats, mono = validate(obj, page, segments, allow)
        reject_total.update(stats)
        # 分母至少算 1：整页返回非 JSON 时 bindings 不存在，若按 0 条算，
        # rejectRatio 会变成 0/0 → 0.0，「invalid ≤ 5%」这条门槛就会假通过。
        # 这是最危险的一种绿——模型什么都没给，报告却说格式合规。
        total_items += count_items(obj)

        if args.mode == "rule+model":
            # 规则的结果原样保留，模型只补空位。
            merged = dict(rule_pred)
            for sid, lst in pred.items():
                merged.setdefault(sid, [])
                merged[sid] = merged[sid] + lst
            pred = merged

        rows, summary = score(page, segments, pred, gold_page)
        per_page.append({
            "index": idx,
            "latencyMs": rec.get("latencyMs"),
            "cached": bool(rec.get("cached")),
            "promptTokens": rec.get("promptTokens"),
            "completionTokens": rec.get("completionTokens"),
            "raw": rec.get("raw") if args.keep_raw else None,
            "rejected": dict(stats),
            "nonMonotonic": mono,
            "model": summary,
            "rules": rule_sum,
            "rows": rows if args.show_rows else None,
        })

    total_model = merge_summaries([p["model"] for p in per_page])
    total_rule = merge_summaries([p["rules"] for p in per_page])
    rej = validate_stats_total(reject_total)
    reject_ratio = (rej / total_items) if total_items else 0.0
    p95 = int(statistics.quantiles(latencies, n=20)[-1]) if len(latencies) >= 2 else (
        latencies[0] if latencies else None)

    return {
        "variant": name,
        "label": VARIANT_LABELS[name],
        "pages": per_page,
        "model": total_model,
        "rules": total_rule,
        "rejected": dict(reject_total),
        "rejectRatio": round(reject_ratio, 4),
        "p95Ms": p95,
        "requests": requests,
        "cacheHits": cache_hits,
        "gate": judge(name, total_model, reject_ratio, p95, total_rule),
    }


# --------------------------------------------------------------------------
# 三种不要 key 的出口
# --------------------------------------------------------------------------

SELFTEST_RAW = {
    "ok": """{"bindings":[{"elementId":"p2-s5","segmentId":"p2-q1","confidence":0.8}]}""",
    "fenced": """```json
{"bindings":[{"elementId":"p2-s5","segmentId":"p2-q1","confidence":0.8}]}
```""",
    "bad_element": '{"bindings":[{"elementId":"p9-s99","segmentId":"p2-q1"}]}',
    "bad_segment": '{"bindings":[{"elementId":"p2-s5","segmentId":"p2-q99"}]}',
    "dup": '{"bindings":[{"elementId":"p2-s5","segmentId":"p2-q1"},'
           '{"elementId":"p2-s5","segmentId":"p2-q2"}]}',
    "aux": '{"bindings":[{"elementId":"p2-note","segmentId":"p2-q1"}]}',
    "no_text": '{"bindings":[{"elementId":"p2-bg","segmentId":"p2-q1"}]}',
    "bad_conf": '{"bindings":[{"elementId":"p2-s5","segmentId":"p2-q1","confidence":1.7}]}',
    "not_json": "抱歉，我无法完成这个任务。",
    "not_object": '{"items":[1,2,3]}',
}


def selftest():
    """校验器必须有牙：每条拒绝规则都要能真的拒绝，且拒绝原因要分得清。"""
    page = {"index": 1, "elements": [
        {"id": "p2-bg", "kind": "shape", "box": [0, 0, 1920, 1080], "text": ""},
        {"id": "p2-s5", "kind": "bullet", "box": [200, 300, 600, 60], "text": "市场趋势"},
        {"id": "p2-note", "kind": "note", "box": [100, 900, 400, 30], "text": "图：来源"},
    ]}
    segments = [{"id": "p2-q1", "text": "先看市场趋势"}, {"id": "p2-q2", "text": "再看别的"}]

    # 每条用例显式给「校验后应当剩下几条绑定」，不用空 dict 当哨兵：
    # 拒绝类的期望就是 0，而空容器是 falsy，拿它做判断条件等于跳过检查
    # —— 第一版就是这样，把 aux 的拦截删掉自测仍然全绿。
    cases = [
        ("ok", "exact_match", 1),
        ("fenced", "exact_match", 1),
        ("bad_element", "bad_element_id", 0),
        ("bad_segment", "bad_segment_id", 0),
        ("dup", "duplicate", 1),
        ("aux", "aux_element", 0),
        ("no_text", "no_text", 0),
        ("bad_conf", "bad_confidence", 0),
        ("not_json", "not_object", 0),
        ("not_object", "not_object", 0),
    ]
    fails = []
    for name, expect, expect_binds in cases:
        obj = extract_json(SELFTEST_RAW[name])
        pred, stats, _ = validate(obj, page, segments)
        n_bind = sum(len(v) for v in pred.values())
        if expect == "exact_match":
            if n_bind != expect_binds or "p2-q1" not in pred:
                fails.append("%s: 期望绑上 %d 条到 p2-q1，得到 %s"
                             % (name, expect_binds, pred))
        elif expect in REJECT_ORDER:
            if stats.get(expect, 0) < 1:
                fails.append("%s: 期望拒绝原因 %s，实际 %s" % (name, expect, dict(stats)))
            if n_bind != expect_binds:
                fails.append("%s: 拒绝后应剩 %d 条绑定，实际 %d 条 %s"
                             % (name, expect_binds, n_bind, pred))
        else:
            fails.append("%s: 未知期望 %s" % (name, expect))

    # 允许集合：规则已绑的元素，模型再给一次必须被拒（规则只读）。
    obj = extract_json(SELFTEST_RAW["ok"])
    pred, stats, _ = validate(obj, page, segments, allow_ids={"p2-bg"})
    if sum(len(v) for v in pred.values()) != 0:
        fails.append("allow_ids 没拦住规则已绑的元素：%s" % pred)
    if stats.get("rule_locked", 0) != 1:
        fails.append("越界未被记成 rule_locked（而不是 duplicate）：%s" % dict(stats))

    # 输入构造：必须每页都有 SLIDE/E/SEG 三段，且元素数一致。
    text = build_page_input(page, segments)
    if "SLIDE" not in text or "SEG" not in text or text.count("\nE ") != len(page["elements"]):
        fails.append("输入构造不对：\n%s" % text)
    t2 = build_page_input_with_rules(page, segments, [(1, 0, 0.9)])
    if "BOUND->p2-q1" not in t2 or "FREE" not in t2:
        fails.append("rule+model 输入没标出已绑/空位：\n%s" % t2)

    # 缓存 key：模型或 promptVersion 变化必须换 key，否则「换模型却读到旧结果」。
    k1 = cache_key("m1", "v1", "s", "u")
    if k1 == cache_key("m2", "v1", "s", "u") or k1 == cache_key("m1", "v2", "s", "u"):
        fails.append("缓存 key 没把 model/promptVersion 算进去")

    # 评测链路：给定一份「模型输出」，分数必须能算对。校验器只证明「拒绝得对」，
    # 这里证明「留下的那些算得对」——没有 key 时，这是分数可信的唯一依据。
    page2 = {"index": 1, "elements": [
        {"id": "a", "kind": "bullet", "box": [100, 100, 400, 60], "text": "收入增长"},
        {"id": "b", "kind": "bullet", "box": [100, 200, 400, 60], "text": "毛利率"},
    ]}
    segs2 = [{"id": "s1", "text": "先看收入增长"}]
    gold2 = {"segments": [{"id": "s1", "text": "先看收入增长",
                           "bind": [{"elementId": "a"}, {"elementId": "b"}]}]}
    pred, _, _ = validate({"bindings": [{"elementId": "a", "segmentId": "s1"}]}, page2, segs2)
    _, summ = score(page2, segs2, pred, gold2)
    if summ["exact"] != 1 or summ["missed"] != 1 or summ["recall_content"] != 0.5:
        fails.append("评测链路算错：期望 exact=1 missed=1 recall_content=0.5，得到 %s" % summ)
    if summ["precision"] != 1.0:
        fails.append("评测链路精确率算错：期望 1.0，得到 %s" % summ["precision"])

    # 跨页合并必须按分子分母合并，不能对小数取平均。
    half = {"elements": 2, "exact": 1, "wrong_seg": 0, "missed": 1, "extra": 0,
            "both_skip": 0, "recall": 0.5, "need": 2, "recall_content": 0.5,
            "need_content": 2, "precision": 1.0}
    one = {"elements": 4, "exact": 4, "wrong_seg": 0, "missed": 0, "extra": 0,
           "both_skip": 0, "recall": 1.0, "need": 4, "recall_content": 1.0,
           "need_content": 4, "precision": 1.0}
    merged = merge_summaries([half, one])
    if merged["recall_content"] != 0.833:
        fails.append("跨页合并不是按分子分母合并：期望 0.833（5/6），得到 %s"
                     % merged["recall_content"])

    # 门槛判定：能过就要 PASS，差一点就要 FAIL——两种都要有，否则判据没被验证过。
    ok_sum = {"recall_content": 0.80, "precision": 0.95}
    bad_sum = {"recall_content": 0.74, "precision": 0.95}
    if not all(g["pass"] for g in judge("no-structure", ok_sum, 0.0, 1000)):
        fails.append("门槛该过却判 FAIL：%s" % judge("no-structure", ok_sum, 0.0, 1000))
    if any(g["pass"] for g in judge("no-structure", bad_sum, 0.0, 1000)
           if "召回" in g["name"]):
        fails.append("召回 0.74 未达 0.75 却判 PASS")
    if all(g["pass"] for g in judge("no-structure", ok_sum, 0.2, 1000)):
        fails.append("invalid 20% 超 5% 却全过")

    # 分母守卫：整页非 JSON 时必须记 1 条，否则 invalid 率恒为 0、门槛假通过。
    if count_items(None) != 1 or count_items({"items": []}) != 1:
        fails.append("count_items 对非 JSON 输出算成 0，invalid 率会恒为 0")
    if count_items({"bindings": [1, 2]}) != 2 or count_items({"bindings": []}) != 1:
        fails.append("count_items 对空 bindings 应至少记 1")

    if fails:
        for f in fails:
            print("FAIL " + f)
        return 1
    print("selftest OK：%d 条校验器用例 + 评测链路 + 跨页合并 + 门槛判定 + 分母守卫 + 缓存 key"
          % len(cases))
    return 0


def dry_run(args):
    a = load_assets()
    pages = variant_pages(args.variants[0], a["q8_seg"], a["q7_seg"], a["q8_elem"],
                          a["q7_elem"], a["q7_colloquial"])
    idx, page, segments, _ = pages[0]
    user = build_page_input(page, segments)
    chars = len(SYSTEM_PROMPT) + len(user)
    print("== system ==")
    print(SYSTEM_PROMPT)
    print("== user (第 1 页 / %d 元素 %d 段) ==" % (len(page["elements"]), len(segments)))
    print(user)
    print("== 估算 ==")
    print("字符 %d；中文按 1 字 ≈ 1 token 粗估 → 输入约 %d token，输出上限 %d"
          % (chars, chars, 1024))
    print("未发请求（--dry-run）。有 key 后去掉这个开关即可。")
    return 0


# --------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--variants", nargs="+", default=["baseline", "no-structure", "colloquial"],
                    choices=["baseline", "no-structure", "colloquial"])
    ap.add_argument("--mode", choices=["model", "rule+model"], default="model",
                    help="model = 模型独立完成；rule+model = 规则先绑、模型只补空位")
    ap.add_argument("--base-url", default=None)
    ap.add_argument("--api-key", default=None)
    ap.add_argument("--model", default=None)
    ap.add_argument("--timeout", type=int, default=120)
    ap.add_argument("--cache-dir", default="tmp/llm-bind-cache")
    ap.add_argument("--out-dir", default="tmp/q8/llm")
    ap.add_argument("--show-rows", action="store_true")
    ap.add_argument("--keep-raw", action="store_true")
    ap.add_argument("--no-cache", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--rules-baseline", action="store_true",
                    help="不需要 key：用同一套评测路径重算规则基线并与锚点对账")
    args = ap.parse_args()

    if args.selftest:
        return selftest()
    if args.dry_run:
        return dry_run(args)
    if args.rules_baseline:
        return rules_baseline(args)

    import os
    args.base_url = args.base_url or os.environ.get("PPTS_LLM_BASE_URL") or DEFAULT_BASE_URL
    args.api_key = args.api_key or os.environ.get("PPTS_LLM_API_KEY")
    args.model = args.model or os.environ.get("PPTS_LLM_MODEL") or DEFAULT_MODEL

    if not args.api_key:
        sys.stderr.write(
            "缺少 PPTS_LLM_API_KEY，未发送任何请求（不伪造数字）。\n"
            "需要其中一个：\n"
            "  - 环境变量 PPTS_LLM_PROVIDER=siliconflow + PPTS_LLM_API_KEY=sk-...\n"
            "  - 或命令行 --api-key / --base-url / --model\n"
            "不开 key 也能跑的两件事：\n"
            "  python3 scripts/fidelity/bind_llm.py --selftest\n"
            "  python3 scripts/fidelity/bind_llm.py --dry-run\n"
        )
        return 2

    if args.no_cache:
        args.cache_dir = "tmp/llm-bind-cache-nocache-%d" % int(time.time())

    a = load_assets()

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    report = {
        "meta": {
            "model": args.model,
            "baseUrl": args.base_url,
            "promptVersion": PROMPT_VERSION,
            "mode": args.mode,
            "cacheDir": args.cache_dir,
            "at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        },
        "variants": [],
    }

    for name in args.variants:
        pages = variant_pages(name, a["q8_seg"], a["q7_seg"], a["q8_elem"],
                              a["q7_elem"], a["q7_colloquial"])
        res = run_variant(name, pages, args, args.cache_dir)
        report["variants"].append(res)
        (out_dir / ("report-%s-%s.json" % (args.mode.replace("+", "-"), name))).write_text(
            json.dumps(res, ensure_ascii=False, indent=1), encoding="utf-8")
        print("[%s] 模型 %s / 规则 %s / 拒绝 %d / P95 %sms / 缓存命中 %d/%d"
              % (name,
                 res["model"].get("recall_content"), res["rules"].get("recall_content"),
                 validate_stats_total(Counter(res["rejected"])),
                 res["p95Ms"], res["cacheHits"], res["requests"]))
        for g in res["gate"]:
            extra = ""
            if g.get("rules") is not None:
                extra = "，规则 %s，差 %+0.3f" % (g["rules"], g["delta"])
            print("    %s %s（need %s, got %s%s）"
                  % ("PASS" if g["pass"] else "FAIL", g["name"], g["need"], g["got"], extra))

    (out_dir / ("report-%s.json" % args.mode.replace("+", "-"))).write_text(
        json.dumps(report, ensure_ascii=False, indent=1), encoding="utf-8")

    failed = [v["variant"] for v in report["variants"] for g in v["gate"] if not g["pass"]]
    print("\n" + ("门槛未过：%s" % ", ".join(sorted(set(failed))) if failed else "全部门槛通过"))
    print("报告：%s" % (out_dir / ("report-%s.json" % args.mode.replace("+", "-"))))
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
