#!/usr/bin/env python3
"""Q7 第 1 件事的实验：元素 → 讲解段落，能不能自动绑。

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

## 两轮规则

`--rules base` 是第一轮（纯字面），`--rules v2` 加三条**与字面无关**的规则：

| 规则 | 触发条件 | 修的是哪一类失败 |
|---|---|---|
| 辅助元素白名单 | kind 是页码/眉标/页脚，或文本以「图：/表：/联系：」开头 | 图注被当成正文绑上（错绑） |
| 数量词 → 编号组 | 段里说「五个」，页内有 5 个编号标记 | 「五步」「分五块」这类概括（字面为 0） |
| 方位指代 | 段里说「右边那张图」 | 纯空间指代（字面为 0） |

再加一条收紧：结构推断（无文本元素跟随内容块）**必须真的包住一个已绑元素**，
且整页铺底与边角碎块一律不绑。第一轮是无条件「挂到前一个已绑元素」，在真实讲义上
（每页 20 多个装饰形状）会把它变成误绑机器。

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

# 页面坐标系。两个样本都换算到 1920×1080（与 gen_pages.py 一致），
# 方位判定与面积占比才有可比的量纲。
PAGE_W, PAGE_H = 1920, 1080

# ---- v2 规则用的常量 ----

# 这几类元素默认**不参与绑定**：它们不是讲解的抓手，而是页面的元信息。
# 判定依据是 kind（真实链路里的形状类型）+ 文本前缀（图注/资料来源/联系方式）。
# 词典刻意保守：每加一条都要能说出「为什么它不该单独淡入」，否则会把该动的东西一起关掉。
DENY_KINDS = {"page_number", "eyebrow", "note", "footer"}
AUX_TEXT_RE = re.compile(
    r"^(图|表|注)\s*[:：]"          # 图注 / 表注
    r"|资料来源|来源[:：]"           # 出处
    r"|联系[:：]|分机|电话[:：]"     # 联系方式
    r"|汇报人|报告人"                # 讲者信息
    r"|内部资料|仅供[内参]|©"        # 文档密级与版权
)

# 数量词：段里说「五个 / 四条 / 三步」时，页面上多半有一组编号元素。
# 前面加 `(?<!第)`：`第二条` 是序数，不是「两条」。不加这条的话，逐条讲的页面会在
# 每条上各触发一次计数规则，把整页的编号块反复绑到最后一段（第二样本实测如此）。
QTY_RE = re.compile(r"(?<!第)[一二两三四五六七八九十\d]{1,3}\s*(?:步|个|条|项|块|部分|段|层|方面|件事|类)")
_CN_DIGIT_ONLY = {"一": 1, "二": 2, "两": 2, "三": 3, "四": 4,
                  "五": 5, "六": 6, "七": 7, "八": 8, "九": 9, "十": 10}

# 「第N步 / 第N条」：出现它就说明列表是被**逐条**讲的，数量词那一段只是引言。
ORDINAL_RE = re.compile(r"第[一二三四五六七八九十\d]{1,3}\s*(?:步|条|个|点|项|部分|块|类)")

# 元素文本内部的有序条目（`1. … 2. …`），用于「一个元素里装了整张列表」的情形。
ITEM_RE = re.compile(r"(?:^|[\s、，,;；:：])\s*\d{1,2}\s*[.、)）]\s*")

# 方位词。只认「左/右/上/下 + 边/侧/方/角/半」这种带后缀的形态：
# 「下面我们看」里的「下面」是语篇连接词，不是空间指代，误判一次就绑错一整段。
DIR_RE = re.compile(r"(左|右|上|下)(?:边|侧|方|角|半)")
_CENTER_WORDS = ("中间", "正中", "居中", "中央")

TABLE_REF = ("这张表", "表格", "表里", "下表", "上表", "这张报表")
IMAGE_REF = ("这张图", "图里", "看图", "截图", "示意图", "架构图", "流程图",
             "照片", "画面", "右边是", "左侧是")


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
    short = min(len(a), len(b))
    # 极短文本（「01」「02」这种编号）上，lcs 覆盖率会被一个字顶到 0.5：
    # 段里出现任意一个 0，编号就"像"了。这类元素只能靠完全相等算命中。
    if short < 3:
        lc = 1.0 if a == b else 0.0
    else:
        lc = lcs_len(a, b) / short

    ta, tb = tokens(seg_text), tokens(elem_text)
    tok = (len(ta & tb) / len(tb)) if tb else 0.0

    score = 0.5 * d + 0.3 * lc + 0.2 * tok
    # 标题类元素的文本通常被完整念出来，权重略提。不是玄学：标题是唯一
    # 「整句复述」概率高的元素，放宽它对召回有利，且它每页只有一个，误绑代价小。
    if elem_kind == "title":
        score *= 1.15
    return min(score, 1.0), {"dice": round(d, 3), "lcs": round(lc, 3), "token": round(tok, 3)}


def align(elements, segments, threshold=THRESHOLD):
    """单调对齐 DP。返回 ([(elem_index, seg_index, score)], 相似度矩阵)。"""
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


# ---------------- v2 规则 ----------------

def is_aux(e):
    """这个元素是不是页面的元信息（不该单独淡入）。

    两道判据分开记：kind 来自形状类型/字号位置，文本前缀来自内容。
    任何一道命中就不绑——但只影响「绑不绑」，不影响「页面上画不画」。
    """
    if e.get("kind") in DENY_KINDS:
        return True
    return bool(AUX_TEXT_RE.search((e.get("text") or "").strip()))


def _area(e):
    b = e.get("box") or [0, 0, 0, 0]
    return max(b[2], 0) * max(b[3], 0)


def _center(e):
    b = e.get("box") or [0, 0, 0, 0]
    return b[0] + b[2] / 2.0, b[1] + b[3] / 2.0


def _contains(outer, inner):
    ox, oy, ow, oh = outer["box"]
    ix, iy = _center(inner)
    return ox <= ix <= ox + ow and oy <= iy <= oy + oh


def _overlap_y(a, b):
    ax, ay, aw, ah = a["box"]
    bx, by, bw, bh = b["box"]
    lo, hi = max(ay, by), min(ay + ah, by + bh)
    if hi <= lo:
        return 0
    return (hi - lo) / max(1, min(ah, bh))


def _overlap_x(a, b):
    ax, ay, aw, ah = a["box"]
    bx, by, bw, bh = b["box"]
    lo, hi = max(ax, bx), min(ax + aw, bx + bw)
    if hi <= lo:
        return 0
    return (hi - lo) / max(1, min(aw, bw))


def detect_count(text):
    """段里有没有「N 个/条/步」这种数量概括。返回 N 或 None。"""
    m = QTY_RE.search(text or "")
    if not m:
        return None
    raw = re.match(r"[一二两三四五六七八九十]+|\d+", m.group(0)).group(0)
    n = int(raw) if raw.isdigit() else _cn_int(raw) if len(raw) > 1 else _CN_DIGIT_ONLY.get(raw)
    # 1 不是「一组」，12 以上在这类讲义里更可能是年份或数据，都不是列表概括。
    return n if n and 2 <= n <= 12 else None


def has_ordinal_after(segments, si):
    """本段之后还有没有「第N步/第N条」。

    有的话，数量词那一段只是引言（「今天讲四个方面」），列表是逐条讲的，
    这时候把整组绑到引言段是**错绑**——而这正是用户看不出来的那类错误。
    """
    return any(ORDINAL_RE.search(s.get("text") or "") for s in segments[si + 1:])


def _markers(elements):
    return [e for e in elements if e.get("kind") == "marker"
            or re.fullmatch(r"0?\d{1,2}", (e.get("text") or "").strip())]


def _cluster(marker, elements, claimed):
    """一个编号标记在版面上属于哪一块。

    两条路径，覆盖两种真实排法：
    - **卡片式**（`01 / 标题 / 注释` 叠在一张卡片里）：找最小的无文本容器把它包住，
      容器里的元素整块算一个。
    - **行式**（`01` 在左、标题在右，整行一个条目）：取同一 y 带里右侧最近的那个。
    """
    containers = [e for e in elements
                  if not (e.get("text") or "").strip()
                  and _area(e) < 0.4 * PAGE_W * PAGE_H
                  and _contains(e, marker)]
    if containers:
        box = min(containers, key=_area)
        block = [e for e in elements if e is not box and _contains(box, e)]
        if len(block) > 1:
            return [box] + block

    right = []
    for e in elements:
        if e is marker or not (e.get("text") or "").strip():
            continue
        if _overlap_y(e, marker) < 0.5:
            continue
        dx = e["box"][0] - (marker["box"][0] + marker["box"][2])
        if 0 <= dx <= 420:
            right.append((dx, e))
    return [marker] + [e for _, e in sorted(right, key=lambda x: x[0])[:1]]


def count_bind(elements, segments, bound):
    """数量词 → 编号组。

    只在「本段之后没有第N条」时触发（见 has_ordinal_after）。
    两组路径与 _cluster 对应，另外补一条：**单个元素内部**装着整张列表
    （`1. … 2. … 3. …` 写在一个形状的文本里），这种情况不需要看编号标记。
    """
    out = []
    for si, seg in enumerate(segments):
        n = detect_count(seg.get("text") or "")
        if not n or has_ordinal_after(segments, si):
            continue

        # 路径 A：一个元素里就装了 n 条
        inline = [e for e in elements
                  if e.get("id") not in bound and not is_aux(e)
                  and len(ITEM_RE.findall(e.get("text") or "")) == n]
        if len(inline) == 1:
            out.append((elements.index(inline[0]), si, 0.0))
            continue

        # 路径 B：n 个编号标记
        ms = [m for m in _markers(elements) if not is_aux(m)]
        if len(ms) != n:
            continue
        block, ok = [], True
        for m in ms:
            part = _cluster(m, elements, bound)
            if not part:
                ok = False
                break
            block.extend(part)
        if not ok:
            continue
        seen = set()
        for e in block:
            i = elements.index(e)
            if e["id"] in bound or i in seen:
                continue
            seen.add(i)
            out.append((i, si, 0.0))
    return out


def directions(text):
    """段里提到的方位。返回 {'left','right','top','bottom','center'} 的子集。"""
    out = set()
    for m in DIR_RE.finditer(text or ""):
        out.add({"左": "left", "右": "right", "上": "top", "下": "bottom"}[m.group(1)])
    for w in _CENTER_WORDS:
        if w in (text or ""):
            out.add("center")
    return out


def _in_region(e, dirs):
    cx, cy = _center(e)
    fx, fy = cx / PAGE_W, cy / PAGE_H
    # 左右用 0.5 切：讲义里的「右边那张图」经常从 45% 处开始，
    # 用 0.67 会把整栏图表判成中间。上下同理用 0.5，但中间只认 0.33–0.67。
    if "left" in dirs and fx >= 0.5:
        return False
    if "right" in dirs and fx <= 0.5:
        return False
    if "top" in dirs and fy >= 0.5:
        return False
    if "bottom" in dirs and fy <= 0.5:
        return False
    if "center" in dirs and not (0.30 <= fx <= 0.70 and 0.20 <= fy <= 0.80):
        return False
    return True


def spatial_bind(elements, segments, bound):
    """方位指代 → 包围盒位置。

    只在**指向一类视觉对象**时才做：段里说「图/表/截图」，否则「右边第三个优点」
    这种也会被当成空间指代，绑上的却是文本框。

    精确率优先：候选必须收敛到 1 个。两个都在右边就放弃——宁可漏，不可错。
    """
    out = []
    for si, seg in enumerate(segments):
        text = seg.get("text") or ""
        dirs = directions(text)
        wants_table = any(k in text for k in TABLE_REF)
        wants_image = any(k in text for k in IMAGE_REF)
        if not dirs and not wants_table and not wants_image:
            continue

        kinds = {"table"} if wants_table else {"image", "table", "chart", "shape"}
        cands = [e for e in elements
                 if e.get("id") not in bound and not is_aux(e)
                 and e.get("kind") in kinds]
        if dirs:
            cands = [e for e in cands if _in_region(e, dirs)]

        # 一个文本框被说成「图」是常见的口语（「右边是部署形态」没说图），
        # 但反过来把「图」绑到一行小字上就是错绑。按类别收敛一次。
        if len(cands) > 1:
            for pref in ("image", "table", "chart", "shape"):
                sub = [e for e in cands if e.get("kind") == pref]
                if len(sub) == 1:
                    cands = sub
                    break
        if len(cands) == 1:
            out.append((elements.index(cands[0]), si, 0.0))
    return out


def ordinal_bind(elements, segments, bound):
    """「第一条 / 第 3 步」→ 页面上第 N 个编号块。

    这是第二样本逼出来的一条：讲稿逐条讲（`第一条，图和文字一起看`），页面上就是
    `01 / 标题 / 注释` 三件套。字面相似度在这里毫无作用（「图和文字一起看」对
    「双通道理解」是 0），但**序数与编号是对应的**，这层对应不依赖语义。

    严格到什么程度：只认纯数字标记（`01`…`0N`），要求序号在本页编号范围内，
    且整块都没被绑过。有一条不满足就不绑——序数在口语里也常被虚指
    （「第一个想法」），宁可漏。
    """
    out = []
    for si, seg in enumerate(segments):
        m = ORDINAL_RE.search(seg.get("text") or "")
        if not m:
            continue
        raw = re.search(r"[一二三四五六七八九十]+|\d+", m.group(0)).group(0)
        n = int(raw) if raw.isdigit() else _cn_int(raw) if len(raw) > 1 \
            else _CN_DIGIT_ONLY.get(raw)
        if not n or n < 1:
            continue
        ms = [x for x in _markers(elements) if not is_aux(x)]
        if len(ms) < n:
            continue
        target = ms[n - 1]
        if target["id"] in bound:
            continue
        part = _cluster(target, elements, bound)
        if not part:
            continue
        # 块里已经有文本元素被绑在**别的段**上，说明这一块已经归别人了，别抢。
        # 绑在同一段上则不算冲突——字面匹配常常只够绑上块里的一件。
        owners = {bound[e["id"]] for e in part
                  if e["id"] in bound and (e.get("text") or "").strip()}
        if owners - {si}:
            continue
        for e in part:
            if e["id"] not in bound:
                out.append((elements.index(e), si, 0.0))
    return out


def sibling_bind(elements, segments, pairs):
    """同块带出：和已绑元素挤在同一块里的东西，跟着它一起进。

    第二样本里最典型的是四步流程：一列的 `圆形底座 / 图标 / 标题 / 注释` 四件。
    字面匹配只够绑上「上传文件」这一件（其余是「自动识别页面、备注与异常提示」
    这种意译），于是同一列里其它三件整块消失——观感上是「点了名才出来一半」。

    判据只有两条，都是几何的：**上下紧邻**（间隙 ≤ 60px，允许负值即轻微重叠）
    或**互相包含**，且横向投影重合 ≥ 0.6。不判内容，因为这里本来就没有字面对应。

    仍然只认「唯一去向」：旁边同时挨着两个绑在不同段上的元素就放弃。
    """
    bound = {ei: si for ei, si, _ in pairs}          # 元素下标 → 段号
    out = []
    for i, e in enumerate(elements):
        if i in bound or is_aux(e):
            continue
        area = _area(e)
        if area == 0 or area >= 0.85 * PAGE_W * PAGE_H:
            continue
        if area < 0.002 * PAGE_W * PAGE_H:          # 分隔条、小圆点
            continue
        owners = set()
        for bi, si in bound.items():
            b = elements[bi]
            if _overlap_x(b, e) < 0.6:
                continue
            if _contains(b, e) or _contains(e, b):
                owners.add(si)
                continue
            gap = max(e["box"][1] - (b["box"][1] + b["box"][3]),
                      b["box"][1] - (e["box"][1] + e["box"][3]))
            if gap <= 60:
                owners.add(si)
        if len(owners) == 1:
            out.append((i, owners.pop(), 0.0))
    return out


def structural_bind(elements, segments, pairs):
    """没有文本的元素，跟着它**装着的**内容走。

    覆盖容器类形状（卡片底、内容区底板）：它们没有文字，但位置就是内容块，
    讲解带出这一块时应该同时出现。这是**结构推断**，不是文本匹配——
    报告里单独统计，不能算进文本匹配的命中率。

    第一轮这里是「挂到前一个已绑元素」，在 5 页干净样本上没问题；换到真实讲义
    （每页 20+ 装饰形状）就变成误绑机器：满页的圆形底、色块、分隔条全会被挂上。
    现在要求**几何上真的包住一个已绑元素**，并排除整页铺底与边角碎块。
    代价是图标类伴随元素绑不上，那是「同块带出」要解决的，不在这里糊。
    """
    bound = {ei: si for ei, si, _ in pairs}
    out = []
    for i, e in enumerate(elements):
        if i in bound or (e.get("text") or "").strip() or is_aux(e):
            continue
        area = _area(e)
        if area == 0 or area >= 0.85 * PAGE_W * PAGE_H:      # 整页铺底 / 蒙层
            continue
        if area < 0.002 * PAGE_W * PAGE_H:                    # 分隔条、角标
            continue
        hosts = [k for k in bound if k != i and _contains(e, elements[k])]
        picks = []
        if hosts:
            # 落点取「这一块是从哪段开始讲的」：装着的元素里最早的那个。
            picks.append(min(bound[h] for h in hosts))
        # 标题就在这块的正上方时，卡片底板通常**跟标题一起**进来（讲「本期四件事」
        # 时整块淡入），而不是等第一行被念到——尤其当里面的内容一个都没绑上时，
        # 没有这条它会整块消失。只认 220px 以内的标题，中段卡片上方没有标题。
        for k, ks in bound.items():
            t = elements[k]
            # 标题本身得是「一段话」而不是横跨整页的整行标题：整行标题的 x 范围
            # 覆盖一切，用它判「正上方」等于不判（第二样本里把四个圆形底座全吸走了）。
            if t.get("kind") == "title" and t["box"][2] < 0.6 * PAGE_W \
                    and _overlap_x(t, e) > 0.5 \
                    and 0 <= e["box"][1] - (t["box"][1] + t["box"][3]) <= 220:
                picks.append(ks)
        if picks:
            out.append((i, min(picks), 0.0))
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
    """golden 支持给每条绑定标 role：content（内容）/ decor（伴随装饰）。

    不分的话，装饰图标绑不上会被算成「漏绑」，把召回率压低却说明不了任何事。
    """
    out = {}
    for s in page_script["segments"]:
        for b in s.get("bind", []):
            out[b["elementId"]] = (s["id"], b.get("role", "content"))
    return out


def compare(pred, gold, elements):
    """逐元素对照，分成五类。

    分类刻意分开「绑错段」与「漏绑」：前者会**多一个不该动的元素**，
    后者会让**该动的元素在页首就出现**（退回静态）。两种缺陷的观感完全不同，
    修复优先级也不同——漏绑更糟，因为它是「该动没动」。
    """
    p = {eid: sid for sid, lst in pred.items() for eid, _ in lst}
    rows = []
    for e in elements:
        eid = e["id"]
        gp, role = gold.get(eid, (None, "content"))
        pp = p.get(eid)
        if gp and pp:
            status = "exact" if gp == pp else "wrong_seg"
        elif gp:
            status = "missed"
        elif pp:
            status = "extra"
        else:
            status = "both_skip"
        rows.append({"element": eid, "kind": e.get("kind"),
                     "text": (e.get("text") or "")[:36],
                     "role": role, "golden": gp, "pred": pp, "status": status})
    return rows


def _recall(rows, role=None):
    sel = [r for r in rows if role is None or r["role"] == role]
    need = [r for r in sel if r["status"] in ("exact", "wrong_seg", "missed")]
    if not need:
        return None, 0
    return round(sum(1 for r in need if r["status"] == "exact") / len(need), 3), len(need)


def summarize(rows):
    counts = Counter(r["status"] for r in rows)
    hit = counts["exact"]
    recall_all, need_all = _recall(rows)
    recall_content, need_content = _recall(rows, "content")
    return {
        "elements": len(rows),
        "exact": counts["exact"],
        "wrong_seg": counts["wrong_seg"],
        "missed": counts["missed"],
        "extra": counts["extra"],
        "both_skip": counts["both_skip"],
        # 召回：golden 要求它出现的元素里，有多少绑对了段。漏绑与绑错都算没召回。
        "recall": recall_all,
        "need": need_all,
        # 只算内容元素（不含图标这类伴随装饰）的召回，跨样本可比。
        "recall_content": recall_content,
        "need_content": need_content,
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
    ap.add_argument("--rules", choices=["base", "v2"], default="v2",
                    help="base = 只有字面匹配（第一轮）；v2 = 加辅助白名单/计数/方位")
    ap.add_argument("--siblings", action=argparse.BooleanOptionalAction, default=True,
                    help="同块带出（与已绑元素上下紧邻/包含的元素一起进）")
    ap.add_argument("--show-matrix", action="store_true")
    args = ap.parse_args()

    seg_doc = json.load(open(args.segments, encoding="utf-8"))
    gold_doc = json.load(open(args.golden, encoding="utf-8"))
    elements_doc = json.load(open(args.elements, encoding="utf-8"))

    pages = []
    totals = Counter()
    need_total = need_content_total = hit_content_total = 0

    for page, seg_page, gold_page in zip(elements_doc, seg_doc["pages"], gold_doc["pages"]):
        elements, segments = page["elements"], seg_page["segments"]

        pairs, sims = align(elements, segments, args.threshold)
        if args.rules == "v2":
            # 辅助元素白名单：字面匹配上的也退掉。图注/页码被绑上属于**错绑**，
            # 精确率会掉，而且用户看不出来。
            pairs = [p for p in pairs if not is_aux(elements[p[0]])]
        text_pairs = list(pairs)
        # id → 段号。规则之间靠它判断「这件东西已经有主了，归哪一段」。
        bound = {elements[ei]["id"]: si for ei, si, _ in pairs}

        count_pairs = spatial_pairs = ordinal_pairs = prior_pairs = struct_pairs = []
        sibling_pairs = []
        if args.rules == "v2":
            count_pairs = count_bind(elements, segments, bound)
            bound.update({elements[ei]["id"]: si for ei, si, _ in count_pairs})
            ordinal_pairs = ordinal_bind(elements, segments, bound)
            bound.update({elements[ei]["id"]: si for ei, si, _ in ordinal_pairs})
            spatial_pairs = spatial_bind(elements, segments, bound)
            bound.update({elements[ei]["id"]: si for ei, si, _ in spatial_pairs})
            if args.siblings:
                sibling_pairs = sibling_bind(
                    elements, segments,
                    pairs + count_pairs + ordinal_pairs + spatial_pairs)
                bound.update({elements[ei]["id"]: si for ei, si, _ in sibling_pairs})
        # 先标题先验、再结构推断：容器的落点依赖「它装着的元素」，而那个元素
        # 可能正是靠标题先验绑上的。反过来跑，卡片就无主可依（口语版里实测漏掉）。
        prior_pairs = title_prior(
            elements, segments,
            pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs)
        struct_pairs = structural_bind(
            elements, segments,
            pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs + prior_pairs)
        pairs = (pairs + count_pairs + ordinal_pairs + spatial_pairs + sibling_pairs
                 + prior_pairs + struct_pairs)
        # 五条规则各自判断「有没有主」，理论上不会重复；但重复一次会让同一个元素在
        # 轨道上出现两条 clip。这里按元素去重（保留先出现的来源），代价是一次 O(n)。
        _seen, _dedup = set(), []
        for p in pairs:
            if elements[p[0]]["id"] in _seen:
                continue
            _seen.add(elements[p[0]]["id"])
            _dedup.append(p)
        pairs = _dedup

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
        need_total += summ["need"]
        need_content_total += summ["need_content"]
        hit_content_total += sum(
            1 for r in rows if r["role"] == "content" and r["status"] == "exact")

        pages.append({
            "index": page["index"],
            "segments": [s["id"] for s in segments],
            "pred": pred,
            "golden": {k: v[0] for k, v in gold.items()},
            "rows": rows,
            "summary": summ,
            # 绑定来源分开记：把容器或标题先验算进"文本匹配命中"是虚报。
            "text_matched": [elements[ei]["id"] for ei, _, _ in text_pairs],
            "count": [elements[ei]["id"] for ei, _, _ in count_pairs],
            "ordinal": [elements[ei]["id"] for ei, _, _ in ordinal_pairs],
            "spatial": [elements[ei]["id"] for ei, _, _ in spatial_pairs],
            "sibling": [elements[ei]["id"] for ei, _, _ in sibling_pairs],
            "prior": [elements[ei]["id"] for ei, _, _ in prior_pairs],
            "structural": [elements[ei]["id"] for ei, _, _ in struct_pairs],
        })

    overall = {
        "elements": sum(p["summary"]["elements"] for p in pages),
        "exact": totals["exact"],
        "wrong_seg": totals["wrong_seg"],
        "missed": totals["missed"],
        "extra": totals["extra"],
        "both_skip": totals["both_skip"],
        "recall": round(totals["exact"] / need_total, 3) if need_total else None,
        "need": need_total,
        "recall_content": round(hit_content_total / need_content_total, 3)
                          if need_content_total else None,
        "need_content": need_content_total,
        "precision": round(
            totals["exact"] / (totals["exact"] + totals["wrong_seg"] + totals["extra"]), 3)
            if (totals["exact"] + totals["wrong_seg"] + totals["extra"]) else None,
        "by_rule": {k: sum(len(p[k]) for p in pages)
                    for k in ("text_matched", "count", "ordinal", "spatial",
                              "sibling", "prior", "structural")},
    }
    report = {"rules": args.rules, "threshold": args.threshold,
              "overall": overall, "pages": pages}

    print(f"\n=== 汇总（rules={args.rules}）===")
    print(f"元素 {overall['elements']} 个；golden 要求出现 {need_total} 个"
          f"（其中内容元素 {need_content_total} 个）")
    print(f"  绑对段 exact     {overall['exact']}")
    print(f"  绑错段 wrong_seg {overall['wrong_seg']}")
    print(f"  漏绑   missed    {overall['missed']}")
    print(f"  误绑   extra     {overall['extra']}")
    print(f"  两边都不绑       {overall['both_skip']}")
    print(f"  绑定来源：字面 {overall['by_rule']['text_matched']}"
          f" / 计数 {overall['by_rule']['count']}"
          f" / 序数 {overall['by_rule']['ordinal']}"
          f" / 方位 {overall['by_rule']['spatial']}"
          f" / 同块 {overall['by_rule']['sibling']}"
          f" / 标题先验 {overall['by_rule']['prior']}"
          f" / 结构推断 {overall['by_rule']['structural']}")
    print(f"  召回 {overall['recall']}（仅内容元素 {overall['recall_content']}）"
          f"  精确 {overall['precision']}")
    for p in pages:
        bad = [r for r in p["rows"] if r["status"] in ("wrong_seg", "missed", "extra")]
        if bad:
            print(f"  page {p['index']}: " + ", ".join(
                f"{r['element']}[{r['role']}]({r['status']}: golden={r['golden']} pred={r['pred']})"
                for r in bad))

    if args.out:
        json.dump(report, open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
        print(f"\n写入 {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
