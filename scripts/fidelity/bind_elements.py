#!/usr/bin/env python3
"""Q7 第 1 件事的最小实验：元素 → 讲解段落，能不能自动绑。

## 这个脚本在回答什么

Q7 的验收样片证明了「绑到段落之后，元素确实跟着讲解出现」。但那份绑定是**手写的**。
真正要定的是：这份绑定从哪来。三个候选——模型生成、规则匹配、人工在轨道上挂。
这里实现并测的是最便宜的那个：**规则匹配**（段落文本 ↔ 元素文本）。

如果规则版能到 90%，就不必上模型；如果只能到 60%，那 60% 的部分也值得先做成
「自动预填 + 人工确认」，把人工从 19 次拖拽降到几次修正。

## 为什么是「单调对齐」而不是「每个元素找最像的段」

讲解是按页内元素的阅读顺序讲的。逐个取最大值会把四行条目全绑到第一段
（它们共享「本期四件事」这种高频词），而且第二行绑到第一段之后，第三行绑到
第二段这种「倒退」在真实讲解里不会发生。加一条单调约束就把搜索空间压掉一个量级，
且这条约束不来自数据、来自「讲解是线性的」这一事实。

DP 定义：`dp[i][j]` = 前 i 个元素、只用到前 j 个段落时的最大相似度和。
三种转移：段 j 不用 / 元素 i 不绑 / 元素 i 绑到段 j（相似度 ≥ 阈值才允许）。
允许同一段连续绑多个元素（`bind` 分支不推进 j），这正是「一段话带出好几个元素」。

## 相似度

中文短句用字符 bigram 的 Dice 系数 + 最长公共子串覆盖率。**不用**词向量、
不用编辑距离：编辑距离对「同义不同词」毫无办法，而这正是要测的失败模式；
词向量在这台机器上没有可离线跑的模型，引入依赖会让这个实验无法复跑。

额外加一路 token 命中（数字、英文）：`174s`、`2.7GB`、`sha256`、`PPTX` 这类
在讲解里被原样念出来，是很强的信号，而 bigram 会把它们切碎。

    python3 scripts/fidelity/bind_elements.py \
        --segments scripts/fidelity/q7-script.json \
        --elements tmp/q7/fid/elements.json \
        --golden scripts/fidelity/q7-script.json \
        --out tmp/q7/bind-report.json
"""
import argparse
import json
import re
import sys
import unicodedata
from collections import Counter

# 相似度低于这个值就不允许绑。定在 0.12 是「至少有两个 bigram 重合」的量级；
# 报告里会打印全部候选的相似度分布，阈值该不该改由分布说话，不是拍脑袋。
THRESHOLD = 0.12

# 段内错落：同一段绑多个元素时，后面的元素依次晚 140ms 出现。
# 与 q7-script.json 里手写的 0 / 140 / 320 一致，不另造一套节奏。
STAGGER_MS = 140


def norm(s):
    """归一化：全角转半角、去掉空白与标点、小写。

    保留数字与拉丁字母。`°`、`·`、`－` 这些在讲解词里不会念出来，
    留着只会稀释 bigram。
    """
    s = unicodedata.normalize("NFKC", s or "")
    s = re.sub(r"[\s\W_]+", "", s, flags=re.UNICODE)
    return s.lower()


def bigrams(s):
    if len(s) < 2:
        return Counter(s)
    return Counter(s[i:i + 2] for i in range(len(s) - 1))


def dice(a, b):
    if not a or not b:
        return 0.0
    ca, cb = bigrams(a), bigrams(b)
    inter = sum((ca & cb).values())
    total = sum(ca.values()) + sum(cb.values())
    return 2.0 * inter / total if total else 0.0


def lcs_len(a, b):
    """最长公共子串长度。滚动数组，O(min(n,m)) 空间。"""
    if not a or not b:
        return 0
    if len(a) < len(b):
        a, b = b, a
    prev = [0] * (len(b) + 1)
    best = 0
    for i in range(1, len(a) + 1):
        cur = [0] * (len(b) + 1)
        for j in range(1, len(b) + 1):
            if a[i - 1] == b[j - 1]:
                cur[j] = prev[j - 1] + 1
                if cur[j] > best:
                    best = cur[j]
        prev = cur
    return best


TOKEN_RE = re.compile(r"[a-z0-9]+(?:\.[0-9]+)?")

# 数字与英文分开抽：`174s` 整串是一个 token 时，对「174 秒」永远匹配不上
# （中文量词不在 [a-z0-9] 里）。分开之后 174 与 s 各是一个 token，数字就对上了。
NUM_RE = re.compile(r"\d+(?:\.\d+)?")
ALPHA_RE = re.compile(r"[a-z]{2,}")

_CN_DIGIT = {"零": 0, "一": 1, "二": 2, "两": 2, "三": 3, "四": 4,
             "五": 5, "六": 6, "七": 7, "八": 8, "九": 9}
_CN_UNIT = {"十": 10, "百": 100, "千": 1000, "万": 10000}
_CN_RUN = re.compile(r"[零一二三四五六七八九十百千万两]{2,}")
_CN_DEC = re.compile(r"([零一二三四五六七八九])点([零一二三四五六七八九]+)")


def _cn_int(run):
    """把「一百七十四」解析成 174。非法组合返回 None（保持原样）。"""
    total = section = number = 0
    for ch in run:
        if ch in _CN_DIGIT:
            number = _CN_DIGIT[ch]
        elif ch in _CN_UNIT:
            unit = _CN_UNIT[ch]
            if unit == 10000:
                section = (section + number) * unit
                total, section = total + section, 0
            else:
                section += (number or 1) * unit
            number = 0
        else:
            return None
    val = total + section + number
    return val if 0 < val < 100000 else None


def cn2num(s):
    """把中文数字改写成阿拉伯数字，只动「两位以上且带单位」的串。

    保守是有意的：单字不动。`一致`、`统一`、`一部分`、`两次` 里的数词一旦被
    改写，文本会变成 `1致` `2次` 这种怪物，bigram 反而更差。
    而 `一百七十四`、`三十` 这类带单位的串不会与普通词撞车。
    """
    def dec(m):
        return f"{_CN_DIGIT[m.group(1)]}." + "".join(str(_CN_DIGIT[c]) for c in m.group(2))

    s = _CN_DEC.sub(dec, s)

    def run(m):
        v = _cn_int(m.group(0))
        return str(v) if v is not None else m.group(0)

    return _CN_RUN.sub(run, s)


def tokens(s):
    s2 = cn2num(unicodedata.normalize("NFKC", s or "")).lower()
    return set(NUM_RE.findall(s2)) | set(ALPHA_RE.findall(s2))


def similarity(seg_text, elem_text, elem_kind=""):
    """返回 (总分, 明细)。总分 0..1。

    权重：bigram Dice 0.5、最长公共子串覆盖率 0.3、token 命中 0.2。
    子串覆盖率单独留一路，是因为短文本上 Dice 会被高频单字摊薄：
    「架构要点」对「五段流水线…架构上每段独立重试」，共享的只有「段」「独」，
    Dice 低，但「架构」这个连续子串已经说明是同一件事。
    """
    a, b = norm(cn2num(seg_text)), norm(cn2num(elem_text))
    if not a or not b:
        return 0.0, {"dice": 0.0, "lcs": 0.0, "token": 0.0}

    d = dice(a, b)
    lc = lcs_len(a, b) / min(len(a), len(b))

    ta, tb = tokens(seg_text), tokens(elem_text)
    tok = (len(ta & tb) / len(tb)) if tb else 0.0

    score = 0.5 * d + 0.3 * lc + 0.2 * tok
    # 标题类元素的文本通常被完整念出来，权重略提。不是玄学：标题是唯一
    # 「整句复述」概率高的元素，放宽它对召回有利，且它每页只有一个，误绑代价小。
    if elem_kind == "title":
        score *= 1.15
    return min(score, 1.0), {"dice": round(d, 3), "lcs": round(lc, 3), "token": round(tok, 3)}


def align(elements, segments, threshold=THRESHOLD):
    """单调对齐 DP。返回 [(elem_index, seg_index, score)]。"""
    n, m = len(elements), len(segments)
    sims = [[similarity(s["text"], e.get("text", ""), e.get("kind", "")) for s in segments]
            for e in elements]

    dp = [[0.0] * (m + 1) for _ in range(n + 1)]
    back = [[None] * (m + 1) for _ in range(n + 1)]

    for i in range(1, n + 1):
        for j in range(1, m + 1):
            # 默认：元素 i 不绑（与 dp[i-1][j] 等价，但显式记下来更利于回溯）
            best, how = dp[i - 1][j], "elem"
            if dp[i][j - 1] > best:              # 段 j 空着不用
                best, how = dp[i][j - 1], "seg"
            s = sims[i - 1][j - 1][0]
            if s >= threshold and dp[i - 1][j] + s > best:
                best, how = dp[i - 1][j] + s, "bind"
            dp[i][j], back[i][j] = best, how

    pairs = []
    i, j = n, m
    while i > 0 and j > 0:
        how = back[i][j]
        if how == "bind":
            pairs.append((i - 1, j - 1, sims[i - 1][j - 1][0]))
            i -= 1                  # j 不动：同一段可以继续绑前面的元素
        elif how == "elem":
            i -= 1
        else:
            j -= 1
    pairs.reverse()
    return pairs, sims


def structural_bind(elements, segments, pairs):
    """没有文本的元素，按阅读顺序挂到前一个已绑元素所在段。

    覆盖的是容器类形状（卡片底、分隔块）：它们没有文字，但位置和内容块绑定，
    讲解带出这一块时它们应该同时出现。这是**结构推断**，不是文本匹配——
    报告里单独统计，不能算进文本匹配的命中率。
    """
    bound = {ei: si for ei, si, _ in pairs}
    out = []
    for i, e in enumerate(elements):
        if i in bound or (e.get("text") or "").strip():
            continue
        for k in range(i - 1, -1, -1):
            if k in bound:
                out.append((i, bound[k], 0.0))
                break
    return out


def title_prior(elements, segments, pairs):
    """页内第一个 title 元素若没绑上，绑到本页第一段。

    为什么这条先验成立：讲一页必然从标题讲起，而标题的措辞经常是**概括**
    而不是重复元素文字（「架构要点」被讲成「五段流水线，每段都有独立的重试」）。
    纯字面匹配在这种页上一定会漏，而「标题在第一段」这个结构事实不依赖字面。

    只在文本匹配失败时兜底，不覆盖已经绑上的结果——它是个补丁，不是主路径。
    """
    bound = {ei for ei, _, _ in pairs}
    if not segments:
        return []
    for i, e in enumerate(elements):
        if e.get("kind") != "title":
            continue
        # 只看页内第一个 title：第二个以后的标题不该无条件吸到第一段。
        if i in bound:
            return []
        return [(i, 0, 0.0)]
    return []


def to_binding(elements, segments, pairs):
    """转成 {segId: [(elementId, offsetMs)]}，段内按阅读顺序错落 STAGGER_MS。"""
    by_seg = {}
    for ei, si, _ in sorted(pairs):
        by_seg.setdefault(si, []).append(elements[ei]["id"])
    out = {}
    for si, ids in by_seg.items():
        out[segments[si]["id"]] = [(eid, k * STAGGER_MS) for k, eid in enumerate(ids)]
    return out


def golden_binding(page_script):
    return {s["id"]: [(b["elementId"], b.get("offsetMs", 0)) for b in s.get("bind", [])]
            for s in page_script["segments"]}


def compare(pred, gold, elements):
    """逐元素对照，分成四类。

    分类刻意分开「绑错段」与「漏绑」：前者会**多一个不该动的元素**，
    后者会让**该动的元素在页首就出现**（退回静态）。两种缺陷的观感完全不同，
    修复优先级也不同——漏绑更糟，因为它是「该动没动」。
    """
    all_ids = [e["id"] for e in elements]
    p = {eid: sid for sid, lst in pred.items() for eid, _ in lst}
    g = {eid: sid for sid, lst in gold.items() for eid, _ in lst}

    rows = []
    for eid in all_ids:
        gp, pp = g.get(eid), p.get(eid)
        if gp and pp:
            status = "exact" if gp == pp else "wrong_seg"
        elif gp:
            status = "missed"
        elif pp:
            status = "extra"
        else:
            status = "both_skip"
        rows.append({"element": eid, "golden": gp, "pred": pp, "status": status})
    return rows


def summarize(rows):
    counts = Counter(r["status"] for r in rows)
    need = [r for r in rows if r["status"] in ("exact", "wrong_seg", "missed")]
    hit = counts["exact"]
    return {
        "elements": len(rows),
        "exact": counts["exact"],
        "wrong_seg": counts["wrong_seg"],
        "missed": counts["missed"],
        "extra": counts["extra"],
        "both_skip": counts["both_skip"],
        # 召回：golden 要求它出现的元素里，有多少绑对了段。漏绑与绑错都算没召回。
        "recall": round(hit / len(need), 3) if need else None,
        # 精确率：规则版给出的绑定里有多少是对的（错段与误绑都算错）。
        "precision": round(hit / (hit + counts["wrong_seg"] + counts["extra"]), 3)
                     if (hit + counts["wrong_seg"] + counts["extra"]) else None,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--segments", default="scripts/fidelity/q7-script.json")
    ap.add_argument("--elements", default="tmp/q7/fid/elements.json")
    ap.add_argument("--golden", default="scripts/fidelity/q7-script.json")
    ap.add_argument("--out", default="")
    ap.add_argument("--threshold", type=float, default=THRESHOLD)
    ap.add_argument("--show-matrix", action="store_true")
    args = ap.parse_args()

    seg_doc = json.load(open(args.segments, encoding="utf-8"))
    gold_doc = json.load(open(args.golden, encoding="utf-8"))
    elements_doc = json.load(open(args.elements, encoding="utf-8"))

    pages = []
    totals = Counter()
    need_total = 0

    for page, seg_page, gold_page in zip(elements_doc, seg_doc["pages"], gold_doc["pages"]):
        assert page["index"] == seg_page["index"] == gold_page["index"]
        elements, segments = page["elements"], seg_page["segments"]

        pairs, sims = align(elements, segments, args.threshold)
        text_pairs = list(pairs)
        # 先标题先验、再结构推断：容器的落点依赖「前一个已绑元素」，而那个元素
        # 可能正是靠标题先验绑上的。反过来跑，卡片就无主可依（口语版里实测漏掉）。
        prior_pairs = title_prior(elements, segments, pairs)
        struct_pairs = structural_bind(elements, segments, pairs + prior_pairs)
        pairs = pairs + prior_pairs + struct_pairs

        pred = to_binding(elements, segments, pairs)
        gold = golden_binding(gold_page)
        rows = compare(pred, gold, elements)
        summ = summarize(rows)

        if args.show_matrix:
            print(f"\n=== page {page['index']} 相似度矩阵 ===")
            head = "%-12s" % "element"
            for s in segments:
                head += " %8s" % s["id"]
            print(head + "   元素文本")
            for i, e in enumerate(elements):
                line = "%-12s" % e["id"]
                for j in range(len(segments)):
                    line += " %8.3f" % sims[i][j][0]
                print(line + "   " + (e.get("text") or "<无文本>")[:34])

        for k in ("exact", "wrong_seg", "missed", "extra", "both_skip"):
            totals[k] += summ[k]
        need_total += summ["exact"] + summ["wrong_seg"] + summ["missed"]

        pages.append({
            "index": page["index"],
            "segments": [s["id"] for s in segments],
            "pred": pred,
            "golden": gold,
            "rows": rows,
            "summary": summ,
            # 三种来源分开记：把容器或标题先验算进"文本匹配命中"是虚报。
            "structural": [elements[ei]["id"] for ei, _, _ in struct_pairs],
            "prior": [elements[ei]["id"] for ei, _, _ in prior_pairs],
            "text_matched": [elements[ei]["id"] for ei, _, _ in text_pairs],
        })

    need = [r for p in pages for r in p["rows"] if r["status"] in ("exact", "wrong_seg", "missed")]
    overall = {
        "elements": sum(p["summary"]["elements"] for p in pages),
        "exact": totals["exact"],
        "wrong_seg": totals["wrong_seg"],
        "missed": totals["missed"],
        "extra": totals["extra"],
        "both_skip": totals["both_skip"],
        "recall": round(totals["exact"] / need_total, 3) if need_total else None,
        "precision": round(
            totals["exact"] / (totals["exact"] + totals["wrong_seg"] + totals["extra"]), 3)
            if (totals["exact"] + totals["wrong_seg"] + totals["extra"]) else None,
        "structural": sum(len(p["structural"]) for p in pages),
    }
    report = {"threshold": args.threshold, "overall": overall, "pages": pages}

    print("\n=== 汇总 ===")
    print(f"元素 {overall['elements']} 个；golden 要求出现 {need_total} 个")
    print(f"  绑对段 exact     {overall['exact']}")
    print(f"  绑错段 wrong_seg {overall['wrong_seg']}")
    print(f"  漏绑   missed    {overall['missed']}")
    print(f"  误绑   extra     {overall['extra']}")
    print(f"  两边都不绑       {overall['both_skip']}")
    print(f"  其中靠结构推断补的 {overall['structural']}（不算文本匹配命中）")
    print(f"  召回 {overall['recall']}  精确 {overall['precision']}")
    for p in pages:
        bad = [r for r in p["rows"] if r["status"] in ("wrong_seg", "missed", "extra")]
        if bad:
            print(f"  page {p['index']}: " + ", ".join(
                f"{r['element']}({r['status']}: golden={r['golden']} pred={r['pred']})" for r in bad))

    if args.out:
        json.dump(report, open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
        print(f"\n写入 {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
