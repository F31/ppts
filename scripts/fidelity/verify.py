#!/usr/bin/env python3
"""第 7 项「该动的时候才动」的自动验证：把三份测量摆在一起对账，而不是各看各的。

为什么需要这个门禁，而不是继续逐个跑检查脚本：

  5.5 与 5.6 两轮人工评分都没能收口，两次的原因不一样，但都是**证据的问题**：
  5.5 是「4 分和 3 分没有定位」（量表没有分值定义），5.6 是「四个 1 分没有定位」
  且与逐帧实测相反。事后查下来，片子两次都不是主要问题——是**测量工具有结构上的盲区**：

    * `check_alignment.py` 只回看开口前 400ms。元素若在更早出现，窗口里它「已经在了」，
      相对窗口首帧的差恒为 0 → 报「不提前、准时」。**它的「通过」和「看不见」长得一模一样。**
    * 判据写「3 帧内」，而评审页只有原生播放器、没有步进键。评审人没法数帧，
      只能凭整体观感给分。**判据不可执行，分数就不可解释。**
    * 所有视频侧测量的参照点都是文本时间轴 `startMs`，音频整体偏移一个都发现不了。

  所以这个门禁的第一职责不是「再测一遍」，是**拒绝在证据自相矛盾或结构上看不见的时候放行**：

    V1 制品    音轨在不在、时长对不对、是不是静音
    V2 音画    lag ≤ 1 帧，且互相关的峰够尖（峰平 = 读数不可信）
    V3 覆盖    设计里绑了的元素，两份报告里都得有；同一元素不许绑两个段落
    V4 盲区    有没有元素在 alignment 的 400ms 窗口**之前**就到位了——有则它的读数无效
    V5 判据    不提前 / 不滞后 / 有层次 / 不空转，逐条给数
    V6 新鲜度  报告比成片旧就作废（改了片子忘记重测，是最容易发生的假通过）
    V7 观感    这一刻画面上有多少东西：内容覆盖不到一半持续多久、容器空框挂多久
    V8 连续    讲着话时画面连续多久没变——「空」是元素没出现，「静」是全出现了却不动
    V9 数据    图里画出来的高度和它自己标的数值成不成比例（柱子会不会说谎）
    V10 叙事   图上标了数的元素，讲解有没有念到这个数（讲稿会不会漏讲）
    V11 同拍   念到某个数的那一刻，注意力是不是正好挪到它身上（讲早了就是抢话）

  V7 与 V8 是仅有的两组**不问元素、只问画面**的：
  前六组全过而人看着不对，差异就在这里。V7 管「每一刻只有半页内容」，
  V8 管「内容铺满之后画面不再变化」——D 片页 1 在第 1.4s 铺满，之后 10.8s 静止，
  而旁白还在逐条念。两个病都不是「元素准不准」，是「该不该在这时候动」。

  V9 与 V10 又是另一种：它们不问「画面内部自不自洽」，而是问**画面与另一个来源对不对得上**。
  V9 对的是元素自带的 `value`（作图数据），V10 对的是旁白文本。共同点是人可以一路看完
  而不觉得有问题——只有把两个来源并排放才看得见。V10 尤其：讲稿读起来是通的，
  要对着图数一遍才知道「四根柱子只讲了两根」。

  V11 把这件事推到时间轴上：V10 只问「念到没有」，念到也可能**太早**。柱子在
  「时间都花在哪」这句里就长齐了，三秒后讲到 943 才把注意力挪过去——观众先看结论、
  再听数字，而那三秒里画面不动，V8 也正好判不了它（它量的是「全片」静止，不是
  「这元素该动时动了没」）。参照物是 TTS 的逐词边界（`words.json`），不是脚本里的
  偏移——偏移正是被审的对象。

`--self-test` 是给这个门禁自己的：往合成数据里逐条注入故障，断言每种都能被红下来。
没有它，门禁本身也会重演「看起来通过了」——这正是 5.6 的教训。

用法：
    python3 scripts/fidelity/verify.py                 # 用已有报告审计（快）
    python3 scripts/fidelity/verify.py --rerun         # 先重跑三个测量，再审计
    python3 scripts/fidelity/verify.py --self-test     # 只跑门禁自检（无需 ffmpeg/成片，CI 可跑）

退出码：0 全过；1 有阻断项；2 输入/环境缺失（成片、报告、ffmpeg）。
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

# 判据阈值集中在这里：改口径只改这一处，别散落在各个检查脚本里。
TOL_PRE_LEVEL = 0.5       # 开口前那一帧的对比度能量 ≥ 峰值的这个比例 = 已经到位（剧透）
TOL_LATE_MS = 100         # 「3 帧内出现」
TOL_EARLY_MS = 100        # 反向的容差：早于开口 100ms 以上才算提前
TOL_STAGGER_MS = 67       # 同段错落与设计值之差（2 帧）
TOL_AV_S = 0.033          # 音画 lag（1 帧）
TOL_STORY_EARLY_MS = 120  # 「讲它」比念到它的词早多少算抢话（≈4 帧，只够量化误差）
TOL_STORY_LATE_MS = 350   # 「讲它」比念到它的词晚多少算走神（一句话里的正常拖尾）
TOL_DUR_MS = 400          # 成片与旁白原件的时长差
TOL_RELEASE_GAP_MS = 500  # 讲完到下一段开讲之间，空档超过这个就必须放手（与 plan/main.go 同值）
TOL_RELEASE_EARLY_MS = 300  # 比句尾早这么多就放手 = 话没说完先撤了（反向也会红）
MIN_MEAN_DB = -50.0       # 低于这个当静音轨
MAX_SHARP = 0.7           # 互相关峰在 ±100ms 处高于此值 = 峰太宽，lag 不可信
LAYOUT_COVER_MIN = 0.50   # 画面内容覆盖低于此值 = 观众看到半页
LAYOUT_HOLD_MS = 1500     # 半页状态持续多久算问题
# 图表：两根柱子的「每单位数值对应多少像素」之间允许的最大倍数差。
# 旧样片是 3.92 倍（174s 被画成 943s 的 72%），新图是 1.003 倍。
CHART_SPREAD_MAX = 1.08
AHEAD_MS = 2000           # 容器的框比最后一件内容早多久算「空框悬挂」
SILENT_MAX_MS = 3500      # 讲着话时画面连续多久不变算「静」——本片最长一句 3.24s
# 焦点淡出的时长，与 shared/motion.ts 的 FOCUS_FADE_MS 一致。V14 用它划窗口：
# 一次切换的过渡应当落在这个窗口里，而不是只占一帧。
FOCUS_FADE_MS = 240
# V14 判「这一帧是不是孤立尖峰」：峰值帧之后仍要有变化，且不低于峰值的 10%
# （同时不低于绝对下限 0.5，免得 3.0 的小切换被 0.3 的编码残差糊过去）。
# 为什么不用「数几帧超过峰值 30%」：easeOutCubic 前快后慢，240ms 的过渡里第二帧
# 只剩峰值的 25%（L 片 p1-item1：5.45 → 1.38），30% 的门槛会把它误判成硬切。
# 而真正的硬切，下一帧是 0.2%（13.29 → 0.028）——两者相差两个数量级。
STEP_AFTER_RATIO = 0.10
STEP_AFTER_MIN = 0.5


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


# ---------------------------------------------------------------- 制品探针

def probe_film(path):
    """音轨有没有、时长多少、响度多少。ffprobe 常常不在 PATH 上，统一走 ffmpeg -i。"""
    err = subprocess.run([ffmpeg_bin(), "-hide_banner", "-i", path],
                         capture_output=True, text=True, errors="replace").stderr
    info = {"hasAudio": False, "durMs": 0, "meanDb": None}
    for line in err.splitlines():
        if "Audio:" in line:
            info["hasAudio"] = True
        if "Duration:" in line:
            h, m, s = line.split("Duration:")[1].split(",")[0].strip().split(":")
            info["durMs"] = round((int(h) * 3600 + int(m) * 60 + float(s)) * 1000)
    if info["hasAudio"]:
        v = subprocess.run([ffmpeg_bin(), "-hide_banner", "-i", path,
                            "-af", "volumedetect", "-f", "null", "-"],
                           capture_output=True, text=True, errors="replace").stderr
        for line in v.splitlines():
            if "mean_volume:" in line:
                info["meanDb"] = float(line.split("mean_volume:")[1].split("dB")[0])
    return info


# ---------------------------------------------------------------- 对账

# ---------------------------------------------------------------- 讲稿覆盖

# 中文数字的字面值。只认读数用的那一套，不认财务大写（壹贰叁）：讲稿里不会用大写，
# 认了反而会把「壹」这类正常用字误判成数字。
_CN_DIGIT = {"零": 0, "〇": 0, "一": 1, "二": 2, "两": 2, "三": 3, "四": 4,
             "五": 5, "六": 6, "七": 7, "八": 8, "九": 9}
_CN_UNIT = {"十": 10, "百": 100, "千": 1000}
# 一段里既可能有「174s」，也可能有「一百七十四秒」。两者要折成同一个数才能比。
_NUM_RE = re.compile(r"\d+(?:\.\d+)?|[零〇一二两三四五六七八九十百千万点]+")
# 「从一个数开头」：跨词拼一个数时只认这种词做起点，否则前面的任意词都能和后面的数
# 拼成「含一个数」的假词，同一个数就会得到一串更早的假时刻。
_NUM_TOKEN_RE = re.compile(r"[\d零〇一二两三四五六七八九十百千万]+")


def _cn_int(s):
    """把「一百七十四」折成 174。按位读，不做「十二万三千」这类口语省略。"""
    total = 0
    num = None
    for ch in s:
        if ch in _CN_DIGIT:
            num = _CN_DIGIT[ch]
        elif ch in _CN_UNIT:
            total += (1 if num is None else num) * _CN_UNIT[ch]
            num = None
        elif ch == "万":
            total = (total + (num or 0)) * 10000
            num = None
    if num is not None:
        total += num
    return total


def numbers_in(text):
    """抽出文本里的全部数值。小数按「点」后逐位读（二点七 = 2.7，一点二五 = 1.25）。

    量词会被一并吞掉：「两次」→ 2、「四核」→ 4。这对判据无害——它只问
    「图上这个数在讲稿里出现过没有」，多认几个量词只会让判据更宽松，不会让它说谎。
    """
    out = []
    for m in _NUM_RE.finditer(text):
        tok = m.group()
        if tok[0].isdigit():
            out.append(float(tok))
            continue
        if "点" in tok:
            head, _, tail = tok.partition("点")
            val = float(_cn_int(head))
            for i, ch in enumerate(tail):
                val += _CN_DIGIT.get(ch, 0) / (10 ** (i + 1))
            out.append(val)
            continue
        out.append(float(_cn_int(tok)))
    return out


def narration_findings(design, elems):
    """V10：图上标了数的元素，讲解必须念到这个数。

    为什么这一条不归画面管：V5～V9 量的是「画面自己准不准」。图上有四根柱子、
    讲稿只念了前两根时，画面每一帧都是对的——柱子比例对、出现时机对、整页也不静。
    漏掉的是**观众要的那个数**：后两根柱子正是「长片线性放大、并发只多 5 秒」
    这句结论的证据，不讲，那张图就只是背景。

    人为什么看不出：讲稿自己读起来是通的（「两核一百七十四秒，四核一百三十一秒」
    是个完整句子）。只有把图和讲稿并排数一遍，才知道四根柱子只讲了 2/4。
    """
    pages = {p.get("index"): p for p in (elems or [])}
    missing = []
    for page in design.get("pages") or []:
        meta = pages.get(page.get("index"))
        if meta is None:
            continue
        spoken = []
        for seg in page.get("segments") or []:
            spoken.extend(numbers_in(seg.get("text") or ""))
        for e in meta.get("elements") or []:
            v = e.get("value")
            if v is None:
                continue
            if not any(abs(v - s) < 1e-6 for s in spoken):
                missing.append("页 %d %s = %g：整页讲解没有念这个数"
                               % (page.get("index"), e.get("id"), v))
    return [{"code": "V10.1", "ok": not missing,
             "msg": "讲稿覆盖：图上每个数值都被念到", "detail": missing}]


def attention_findings(design, words, appear=None, elems=None):
    """V11：该指它的时候，注意力要在它身上。

    V10 只问「这个数念到没有」。念到了，也可能念得太早或太晚：柱子在「时间都花在哪」
    这句里就长齐了，直到三秒后讲到 943 才把注意力挪过去——观众先看见结论，再听人念
    一遍数字，画面等于提前剧透，而那 3 秒里柱子一动不动，V8 也正好判不了它。

    所以这一条问的是**两个时刻之间的距离**。但「该指的时刻」有两种，参照物不同：

    - **一段里的第一个焦点**：这一段讲的就是它，参照是**段开口**。句首就该指过去。
      原口径一律拿「念到那个数」当参照，于是 s2-3「两核跑五分钟，九百四十三秒」
      要等到 1.4s 后的「九百四十三」才切——前半句在讲 bar2，画面却全亮着不指任何人。
      观众听到「两核跑五分钟」，眼睛找不到这根柱子；这不是「不抢话」，是「讲话了没反应」。
    - **句中换人**（同段第二个及以后的焦点）：参照是**念到它的那个数**。
      一句话里从 A 讲到 B，切换点必然落在 B 的那个数上，早一点就是替旁白剧透。

    参照物是 `words.json` 的逐词边界，不是脚本里的偏移：脚本里的偏移正是被审的对象。
    这也是唯一一条**同时**吃「音频的词」与「编排的数」的判据——两边各自都对、
    对不上，只有把两列并排放才看得见。

    容差不对称：早 120ms 只够覆盖渲染量化与测量粒度，晚 350ms 才是一句话里的正常拖尾。
    抢话比迟到难听得多（先给答案再提问），所以早的那一边收得紧。

    另有一半走「出场」：没有焦点的元素，用它自己的实测出场时刻去比数值词（原逻辑）。
    这条同时覆盖了「有焦点」和「只有出场」两种编排，也是 V11 与 V5 的接口。
    """
    if not words:
        return [{"code": "V11.1", "ok": True,
                 "msg": "逐词时间轴未提供（--words），本项跳过", "detail": []}]
    wmap = words.get("segments") or {}
    pages = {p.get("index"): p for p in (elems or [])}
    appear_by = {r["element"]: r for r in (appear or [])}
    bad, unjudged = [], []
    for page in design.get("pages") or []:
        idx = page.get("index")
        vals = {e["id"]: e["value"]
                for e in (pages.get(idx) or {}).get("elements") or []
                if e.get("value") is not None}
        if not vals:
            continue
        # 逐词 → 页内绝对时刻。数值常常不是单独一个词（「一百七十四」是一个词，
        # 「五分钟」也是一个词，但换个文本就可能劈开），所以单字、双词、三词都试一遍。
        word_at = []
        for seg in page.get("segments") or []:
            rec = wmap.get(seg.get("id")) or {}
            ws = rec.get("words") or []
            base = seg.get("startMs", 0)
            for i in range(len(ws)):
                for span in (1, 2, 3):
                    chunk = ws[i:i + span]
                    if len(chunk) < span:
                        break
                    # 跨词拼接只用来救「一个数被劈成两个词」（如 九百 / 四十三）。
                    # 不加这道门，任何词都能跟后面的数拼成一个「含一个数」的大词，
                    # 于是同一个数会在它真正被念出来之前就有一堆更早的假时刻——
                    # 判据会静悄悄地松掉（自检里 V11.1-early 就是这么红的）。
                    if span > 1 and not _NUM_TOKEN_RE.match(chunk[0]["text"]):
                        continue
                    ns = numbers_in("".join(w["text"] for w in chunk))
                    if len(ns) == 1:
                        word_at.append((base + chunk[0]["tMs"], ns[0]))
        word_at.sort()
        # 注意力时刻。与旧版的差别只在这里：**段内第一个焦点对齐段开口**，
        # 其余焦点才对齐数值词。att[eid] = (t, how, ref, 参照名)，
        # ref 非 None 表示有一个不依赖数值的直接参照（句首）。
        att = {}
        for seg in page.get("segments") or []:
            base = seg.get("startMs", 0)
            focals = []
            if seg.get("focus"):
                focals.append((base, seg["focus"], "段级焦点"))
            for fa in seg.get("focusAt") or []:
                focals.append((base + fa.get("atMs", 0), fa["elementId"], "句内焦点"))
            # 段内按时间排：「第一个」指的是观众最先被指到的那个，不是脚本里先写的那个。
            focals.sort(key=lambda x: x[0])
            for i, (t, eid, how) in enumerate(focals):
                if i == 0:
                    att.setdefault(eid, (t, how, base, "句首"))
                else:
                    att.setdefault(eid, (t, how, None, "念到它的词"))
        for eid, v in sorted(vals.items()):
            if eid in att:
                ta, how, ref, refname = att[eid]
                if ref is not None:
                    tw = ref
                else:
                    tw = next((t for t, n in word_at if abs(n - v) < 1e-6), None)
                    if tw is None:
                        continue  # 没念到是 V10 的事，这里不重复报
            else:
                r = appear_by.get(eid)
                if not r or r.get("settledMs") is None:
                    unjudged.append("%s = %g：既没有焦点也没有出场记录" % (eid, v))
                    continue
                ta, how, refname = r["settledMs"] - r.get("pageStart", 0), "出场", "念到它的词"
                tw = next((t for t, n in word_at if abs(n - v) < 1e-6), None)
                if tw is None:
                    continue
            gap = ta - tw
            if gap < -TOL_STORY_EARLY_MS:
                bad.append("页 %d %s = %g：%s比%s早 %dms（抢话）"
                           % (idx, eid, v, how, refname, -gap))
            elif gap > TOL_STORY_LATE_MS:
                bad.append("页 %d %s = %g：%s比%s晚 %dms（>%dms）"
                           % (idx, eid, v, how, refname, gap, TOL_STORY_LATE_MS))
    return [{"code": "V11.1", "ok": not bad and not unjudged,
             "msg": "该指的时候指：段内首个焦点对齐句首，句中换人对齐念到它的词"
                    "（早 ≤%dms / 晚 ≤%dms）"
                    % (TOL_STORY_EARLY_MS, TOL_STORY_LATE_MS),
             "detail": bad + unjudged}]


def focus_unique_findings(rep):
    """V13：同一时刻亮着几个——压暗是互斥的，满墨的只能有一个。

    这条问的是**取景**，不是内容：四个元素都在、都按时出现、切换时刻也对，
    但第一根柱子从第 3 个焦点开始一直没被压暗（`focusDim` 少了一个 `i === 0`），
    页面上两根柱子一样深，「现在讲的是哪根」就没人知道了。

    前面十二条判据没有一条会红：V5 问「该动时动了没」（动了），V7/V8 问「空不空、
    静不静」（不空不静），V11 问「什么时候切」（切得对）。压暗本身是从未被审的对象。
    """
    bad = []
    for page in rep.get("pages") or []:
        for v in page.get("violations") or []:
            bad.append("页 %d %.2f–%.2fs：%s 同时满墨（该退后的没退）"
                       % (page.get("index"), v["fromAbsMs"] / 1000, v["toAbsMs"] / 1000,
                          " / ".join(v.get("bright") or [])))
    return [{"code": "V13.1", "ok": not bad,
             "msg": "只指一个：有人被压暗时，满墨的元素恰好一个",
             "detail": bad}]


def focus_release_findings(design, plan):
    """V12：压暗有没有定义终点——「这一句讲完了」之后是不是还指着上一个对象。

    审的是编排（plan.json 的 focus 区间），不是成片像素。这两件事要分开：
    压暗区间是不是这么算的，是编排的事；渲染端有没有照做，由 V5.2 的起播延迟
    与抽帧目视一起兜。要是拿成片的亮度去反推「谁被压暗」，深色元素与浅色元素
    会得出两种阈值，判据自己先不稳。

    规则只有一条：**讲它的那一段结束之后，若下一段没有紧接着换人，就必须放手。**
    「放手」= 整页回到全亮（不指任何人），而不是「把焦点让给另一个对象」——
    那段时间旁白在说别的事，画面唯一诚实的做法是不指。
    """
    findings = []
    by_page = {p.get("index"): p for p in (plan.get("pages") or [])}
    bad = []
    checked = 0
    for page in design.get("pages") or []:
        pl = by_page.get(page.get("index"))
        if pl is None:
            continue
        spans = pl.get("focus") or []
        if len(spans) < 2:
            continue
        page_ms = page.get("durationMs", 0)
        segs = page.get("segments") or []
        for i, f in enumerate(spans):
            start = f.get("startMs", 0)
            owner = next((s for s in segs
                          if s.get("startMs", 0) <= start
                          < s.get("startMs", 0) + s.get("durationMs", 0)), None)
            if owner is None:
                continue
            seg_end = owner.get("endMs") or owner.get("startMs", 0) + owner.get("durationMs", 0)
            nxt = spans[i + 1].get("startMs", page_ms) if i + 1 < len(spans) else page_ms
            gap = nxt - seg_end
            if gap < TOL_RELEASE_GAP_MS:
                # 下一句紧接着讲下一个对象：这里放手会闪一下全亮，比一直压着难听。
                continue
            checked += 1
            end = f.get("endMs") or 0
            if end == 0:
                bad.append("页 %d %s：「%s」讲完（%dms）之后仍压着它 %dms 才换人"
                           % (page.get("index"), f.get("elementId"),
                              owner.get("text", owner.get("id", ""))[:12],
                              seg_end, nxt - seg_end))
            elif end < seg_end - TOL_RELEASE_EARLY_MS:
                bad.append("页 %d %s：%dms 就放手，那一句还有 %dms 没讲完"
                           % (page.get("index"), f.get("elementId"),
                              end, seg_end - end))
            elif end > nxt:
                bad.append("页 %d %s：放手时刻 %dms 晚于下一段 %dms"
                           % (page.get("index"), f.get("elementId"), end, nxt))
    return [{"code": "V12.1", "ok": not bad,
             "msg": "讲完就放手：空档 ≥%dms 时压暗必须有终点（共 %d 处要放手）"
                    % (TOL_RELEASE_GAP_MS, checked),
             "detail": bad}]


def focus_transition_findings(design, motion):
    """V14：切换不能一帧跳完——「此刻亮着几个」对了，不代表「怎么变过去的」对。

    页 2 的**第一个**焦点原来只设值、不给过渡（`focusDim` 的 `i === 0` 分支直接
    写 1/0），于是 tMs 一过 startMs，整页从全亮跳到压暗只用一帧：50Hz 采样下也
    只有一个 20ms 窗口有变化（L 片 18.9s），像有人啪一下把灯关了。后面每次换人
    都有 240ms，同一个动作两种手感，只因为它是第一个。

    前面 26 项全绿也查不到：V13 问「此刻亮着几个」（一个，对），V11 问「什么时候
    切」（对），V5/V7/V8 问「动没动、空不空、静不静」（都动）。**没有一条问过
    「动得连不连续」**——判定的是状态，不是状态之间。

    怎么量：只看像素，不看 focus.json 的比值。取焦点起点前后一个淡出窗口的逐帧
    块差，找峰值帧；再看**峰值之后**还有没有像样的变化。硬切是孤立尖峰——下一帧
    直接归零（L 片 13.29 → 0.028，0.2%）；有过渡则下一帧仍在动（5.45 → 1.38，25%）。
    不用比值是因为深色柱压到 0.55、浅色卡片压到 0.75，比值判据自己要先分档。
    """
    if not motion:
        return [{"code": "V14.1", "ok": True,
                 "msg": "画面连续性未测（--motion），本项跳过", "detail": []}]
    steps = motion.get("steps") or []
    if not steps:
        return [{"code": "V14.1", "ok": True,
                 "msg": "逐帧差值未提供（旧版 motion.json 没有 steps），本项跳过",
                 "detail": []}]

    bad, checked, skipped = [], 0, 0
    base = 0
    for page in design.get("pages") or []:
        idx = page.get("index")
        pts = []
        for seg in page.get("segments") or []:
            s0 = seg.get("startMs", 0)
            if seg.get("focus"):
                pts.append((base + s0, seg["focus"]))
            for fa in seg.get("focusAt") or []:
                pts.append((base + s0 + fa.get("atMs", 0), fa["elementId"]))
        pts.sort()
        # 相邻两次切换挨得比一个淡出还近的，算同一次动作，别重复判。
        merged = []
        for p in pts:
            if merged and p[0] - merged[-1][0] < FOCUS_FADE_MS:
                continue
            merged.append(p)

        for t0, eid in merged:
            lo, hi = t0 - 100, t0 + FOCUS_FADE_MS + 200
            win = [s for s in steps if lo <= s["tMs"] <= hi]
            if len(win) < 3:
                continue
            pk = max(win, key=lambda s: s["block"])
            if pk["block"] < 3.0:
                skipped += 1
                continue      # 这次切换几乎没改变画面（元素太小，或本来就没有可压暗的）
            after = [s["block"] for s in win if s["tMs"] > pk["tMs"]]
            if not after:
                skipped += 1
                continue      # 峰值贴着窗口右边界，看不出后面还有没有
            checked += 1
            if max(after) < max(pk["block"] * STEP_AFTER_RATIO, STEP_AFTER_MIN):
                bad.append("页 %d %s @%dms：峰值 %.1f 之后立刻归零（%.2f）——一帧跳完，没有过渡"
                           % (idx, eid, t0, pk["block"], max(after)))
        base += page.get("durationMs", 0)

    msg = "切换有过程：每次焦点切换都不是孤立尖峰（共判 %d 处" % checked
    if skipped:
        msg += "，%d 处画面变化太小不判" % skipped
    msg += "）"
    return [{"code": "V14.1", "ok": not bad, "msg": msg, "detail": bad}]


def _bound_of(design):
    """设计意图 → {元素: [(段号, 段内错落)]}，顺序保留，用来发现重复绑定。"""
    out = {}
    for page in design["pages"]:
        for seg in page["segments"]:
            for ref in seg.get("bind", []):
                out.setdefault(ref["elementId"], []).append(
                    (seg["id"], ref.get("offsetMs", 0)))
    return out


def audit(design, align, appear, film, elems=None, plan=None):
    """纯函数：只吃数据，不碰文件。返回 [(code, ok, msg, detail)]。

    拆出来的目的是 `--self-test` 能往合成数据里注入故障——门禁自己也得被验证。
    """
    findings = []

    def add(code, ok, msg, detail=None):
        findings.append({"code": code, "ok": ok, "msg": msg, "detail": detail or []})

    bound = _bound_of(design)
    align_by = {r["element"]: r for r in align}
    appear_by = {r["element"]: r for r in appear}

    # ---- V1 制品
    add("V1.1", film["hasAudio"], "成片含音轨",
        [] if film["hasAudio"] else ["没有音轨 → 四个判据都以语音为参照，结构上无法评"])
    if film.get("refDurMs"):
        d = abs(film["durMs"] - film["refDurMs"])
        add("V1.2", d <= TOL_DUR_MS, "成片时长与旁白一致（%dms 差）" % d,
            [] if d <= TOL_DUR_MS else ["差 %dms > %dms：片尾被切或多了空白" % (d, TOL_DUR_MS)])
    if film.get("meanDb") is not None:
        ok = film["meanDb"] > MIN_MEAN_DB
        add("V1.3", ok, "音轨非静音（mean %.1f dB）" % film["meanDb"],
            [] if ok else ["mean %.1f dB 低于 %.0f，等同静音轨" % (film["meanDb"], MIN_MEAN_DB)])
    if film.get("lag") is not None:
        ok = abs(film["lag"]) <= TOL_AV_S
        add("V2.1", ok, "音画同步 lag %+.3fs" % film["lag"],
            [] if ok else ["|lag| > %.0fms：所有元素相对人声整体偏了" % (TOL_AV_S * 1000)])
        ok = film.get("sharp", 1.0) <= MAX_SHARP
        add("V2.2", ok, "互相关峰可判（±100ms 处 %.2f）" % film.get("sharp", 0),
            [] if ok else ["峰太宽：两条包络都平，lag 读数不可信"])

    # ---- V3 覆盖
    missing_align = [e for e in bound if e not in align_by]
    missing_appear = [e for e in bound if e not in appear_by]
    add("V3.1", not missing_align and not missing_appear,
        "绑定元素 %d 个，两份报告都覆盖" % len(bound),
        (["alignment 缺：%s" % ", ".join(missing_align)] if missing_align else [])
        + (["appearance 缺：%s" % ", ".join(missing_appear)] if missing_appear else []))
    dup = {e: v for e, v in bound.items() if len(v) > 1}
    add("V3.2", not dup, "没有元素被绑到多个段落",
        ["%s → %s" % (e, v) for e, v in dup.items()] or [])

    # ---- V4 工具盲区：在 alignment 的 400ms 窗口之前就已经到位的元素
    blind = []
    for e, r in appear_by.items():
        if r.get("seg") is None or r.get("settledMs") is None:
            continue
        window_start = r["at"] + r.get("offsetMs", 0) - 400
        if r["settledMs"] < window_start:
            blind.append("%s 在 %dms 就到位，而 alignment 的窗口从 %dms 开始"
                         % (e, r["settledMs"], window_start))
    add("V4.1", not blind, "无元素早于 alignment 的取窗（其读数有效）", blind)

    # ---- V5 四个判据
    early, late, stagger_bad, never = [], [], [], []
    for e, slots in bound.items():
        r = appear_by.get(e)
        a = align_by.get(e)
        if r is not None:
            if r.get("settledMs") is None:
                never.append(e)
            if r.get("preSegLevel") is not None and r["preSegLevel"] >= TOL_PRE_LEVEL:
                early.append("%s 开口前已到位（preSegLevel %.2f ≥ %.2f）"
                             % (e, r["preSegLevel"], TOL_PRE_LEVEL))
        if a is not None and a.get("delayMs") is not None:
            lag = a["delayMs"] - a.get("offsetMs", 0)
            if lag > TOL_LATE_MS:
                late.append("%s 开口后 %dms 才动（>%dms）" % (e, lag, TOL_LATE_MS))
    add("V5.1", not early, "不提前：无元素在开口前到位", early)
    add("V5.2", not late, "不滞后：全部在开口后 %dms 内开始" % TOL_LATE_MS, late)
    add("V5.5", not never, "全部绑定元素都到达终态", ["%s 整页未稳定出现" % e for e in never])

    # 有层次：同一段里设计上错开的两件，实测差值要与设计差值对得上
    by_seg = {}
    for e, slots in bound.items():
        a = align_by.get(e)
        if a is None or a.get("delayMs") is None:
            continue
        by_seg.setdefault(a["seg"], []).append((e, a["delayMs"], a.get("offsetMs", 0)))
    for sid, items in by_seg.items():
        for i in range(len(items)):
            for j in range(i + 1, len(items)):
                e1, d1, o1 = items[i]
                e2, d2, o2 = items[j]
                if o1 == o2:
                    continue
                err = abs((d1 - d2) - (o1 - o2))
                if err > TOL_STAGGER_MS:
                    stagger_bad.append("%s/%s 设计差 %dms、实测差 %dms（差 %dms > %d）"
                                       % (e1, e2, o1 - o2, d1 - d2, err, TOL_STAGGER_MS))
    add("V5.3", not stagger_bad, "有层次：同段错落与设计一致（±%dms）" % TOL_STAGGER_MS,
        stagger_bad)

    # 不空转：讲解没提到的元素不许在页首就在，也不许早于本页最后一段
    last_start = {}
    for page in design["pages"]:
        last_start[page["index"]] = max(
            [s["startMs"] for s in page["segments"]] or [0])
    idle = []
    # 容器（完整包住别的元素）不算「内容」：底板、隔板这类东西页首就在，是版式决定的，
    # 不是「没讲到却自己冒出来」。V7.2 判空框时已经不把它算进分母，这里同口径。
    containers = set()
    for page in (elems or []):
        boxes = {e["id"]: e["box"] for e in page.get("elements") or []}
        for a, ba in boxes.items():
            for b, bb in boxes.items():
                if a != b and bb[0] >= ba[0] - 1 and bb[1] >= ba[1] - 1 \
                        and bb[0] + bb[2] <= ba[0] + ba[2] + 1 \
                        and bb[1] + bb[3] <= ba[1] + ba[3] + 1:
                    containers.add(a)
                    break
    for e, r in appear_by.items():
        if e in bound or e in containers or r.get("page") not in last_start:
            continue
        if r.get("always"):
            idle.append("%s 页首就在（没被讲到却常驻）" % e)
        elif r.get("settledMs") is not None:
            floor = r.get("pageStart", 0) + last_start[r["page"]]
            if r["settledMs"] < floor:
                idle.append("%s 在 %dms 就出，早于本页最后一段（%dms）"
                            % (e, r["settledMs"], floor))
    add("V5.4", not idle, "不空转：未提及的元素只在页末出现", idle)

    # ---- V7 观感：元素都准时，不代表画面是完整的
    if elems:
        findings.extend(layout_findings(design, appear, elems))

    # ---- V8 画面连续性：元素都在，不代表画面还在动
    motion = film.get("motion")
    if motion is None:
        add("V8.1", True, "画面连续性未测（--motion），本项跳过", [])
    else:
        longest = motion.get("longestMs") or 0
        ok = longest <= SILENT_MAX_MS
        detail = []
        if not ok:
            for s in sorted(motion.get("spans", []), key=lambda x: -x["durMs"]):
                if s["durMs"] > SILENT_MAX_MS:
                    detail.append("%.2fs–%.2fs 画面 %.2fs 未变（其中讲解 %.2fs）"
                                  % (s["startMs"] / 1000, s["endMs"] / 1000,
                                     s["durMs"] / 1000, s["speechMs"] / 1000))
        add("V8.1", ok, "讲着话时画面不长静止（最长 %.2fs）" % (longest / 1000), detail)

    # ---- V9 图表真实性：画面都对，不代表图没在说谎
    chart = film.get("chart")
    if chart is None:
        add("V9.1", True, "图表比例未测（--chart），本项跳过", [])
    else:
        detail = []
        for r in chart:
            if r.get("ok"):
                continue
            if r.get("missing"):
                detail.append("页 %d：标注有值但柱体没画出来 %s"
                              % (r["page"], ", ".join(r["missing"])))
            if r.get("spread"):
                detail.append("页 %d：柱高/数值 最大差 %.2f 倍（容差 %.2f）"
                              % (r["page"], r["spread"], CHART_SPREAD_MAX))
        add("V9.1", all(r.get("ok") for r in chart),
            "图表不说谎：柱高与标注值成比例", detail)

    # ---- V10 讲稿覆盖：画面与旁白对不对得上（不需要成片，纯查两份文本）
    if elems:
        findings.extend(narration_findings(design, elems))
    # ---- V11 讲与念是否同拍：V10 管「念没念到」，这一条管「是不是那一刻念的」
    findings.extend(attention_findings(design, film.get("words"), appear, elems))
    # ---- V12 讲完有没有放手：V11 管「开始讲它」的时刻，这一条管「讲完之后还压着谁」
    if plan:
        findings.extend(focus_release_findings(design, plan))
    # ---- V13 只指一个：压暗是互斥的，看「此刻亮着几个」。要成片像素，不要计划
    if film.get("focus"):
        findings.extend(focus_unique_findings(film["focus"]))
    # ---- V14 切换有没有过程：V13 管「亮着几个」，这一条管「从上一个到这个是怎么变的」
    findings.extend(focus_transition_findings(design, film.get("motion")))

    return findings


def layout_findings(design, appear, elems):
    """V7：**这一刻画面上有多少东西**。

    前六组都在问「元素准时吗」，这一组问的是另一件事。5.5 / 5.6 两轮人工评分
    都收不了口，而片子在前六组上全过 —— 差异就在这一层：元素各就各位，
    但每一刻都只有半页内容。

    两个数：

    - 内容覆盖：已到位元素占本页内容面积的比例。**按面积、不按像素**——
      像素法在页 1 上是瞎的（10.15s 时四条要点只出两条，卡片里空着三分之二，
      但卡片底色已铺满，整页像素到位率 93%）。
    - 空框悬挂：容器的框先出现、内容后填。观众看到的是「还没加载完」。

    返回 [(code, ok, msg, detail)]，与 audit() 同形，便于合并。
    """
    out = []
    app = {r["element"]: r for r in appear}
    by_page = {e["index"]: e.get("elements") or [] for e in elems}
    pages = design["pages"]
    starts = {r["page"]: r["pageStart"] for r in appear}

    for i, page in enumerate(pages):
        idx = page["index"]
        # 封面与谢幕不查：「从标题开始讲」本身就是版式，开头只有一行字不是缺陷。
        if idx in (0, len(pages) - 1):
            continue
        els = by_page.get(idx) or []
        if len(els) < 2:
            continue
        boxes = {e["id"]: e["box"] for e in els}
        # 容器 = 完整包住至少一个别的元素的元素。它不计入分母：
        # 卡片底板到位不等于内容到位，算进去会让「空盒子」看起来是满的。
        containers = set()
        for a, ba in boxes.items():
            for b, bb in boxes.items():
                if a != b and bb[0] >= ba[0] - 1 and bb[1] >= ba[1] - 1 \
                        and bb[0] + bb[2] <= ba[0] + ba[2] + 1 \
                        and bb[1] + bb[3] <= ba[1] + ba[3] + 1:
                    containers.add(a)
                    break
        info = [e for e in els if e["id"] not in containers
                and app.get(e["id"], {}).get("settledMs")]
        if len(info) < 2:
            continue

        area = {e["id"]: float(e["box"][2] * e["box"][3]) for e in info}
        total = sum(area.values()) or 1.0
        t0 = starts.get(idx, 0)
        nxt = starts.get(idx + 1)
        t_end = nxt if nxt is not None else t0 + page["durationMs"]
        times = sorted({app[e["id"]]["settledMs"] for e in info})
        spans = []
        for j, t in enumerate(times):
            got = {e["id"] for e in info if app[e["id"]]["settledMs"] <= t}
            end = times[j + 1] if j + 1 < len(times) else t_end
            spans.append((t, end, sum(area[k] for k in got) / total, got))

        best = (0.0, None, None, set())
        run = None
        for t0_, t1_, c, got in spans:
            if c < LAYOUT_COVER_MIN:
                if run is None:
                    run = [t0_, t1_, got]
                else:
                    run[1], run[2] = t1_, got
            elif run is not None:
                if run[1] - run[0] > best[0]:
                    best = (run[1] - run[0], run[0], run[1], run[2])
                run = None
        if run is not None and run[1] - run[0] > best[0]:
            best = (run[1] - run[0], run[0], run[1], run[2])
        dur, s0, s1, seen = best
        ok = dur < LAYOUT_HOLD_MS
        missing = [e["id"] for e in info if e["id"] not in seen]
        out.append({
            "code": "V7.1", "ok": ok,
            "msg": ("第 %d 页无明显空窗（低覆盖最长 %.1fs）" % (idx, dur / 1000)) if ok
                   else ("第 %d 页空窗 %.1fs" % (idx, dur / 1000)),
            "detail": [] if ok else [
                "%.2fs–%.2fs（%.1fs）画面上不到本页内容的 %d%%，缺：%s"
                % (s0 / 1000, s1 / 1000, dur / 1000, int(LAYOUT_COVER_MIN * 100),
                   "、".join(missing[:5]))],
        })

        # 空框悬挂：框开始出现 → 最后一件内容到位
        for e in els:
            if e["id"] not in containers:
                continue
            r = app.get(e["id"], {})
            if not r.get("onsetMs"):
                continue
            inner = [app[k["id"]]["settledMs"] for k in els
                     if k["id"] != e["id"] and app.get(k["id"], {}).get("settledMs")
                     and k["box"][0] >= e["box"][0] - 1
                     and k["box"][1] >= e["box"][1] - 1
                     and k["box"][0] + k["box"][2] <= e["box"][0] + e["box"][2] + 1
                     and k["box"][1] + k["box"][3] <= e["box"][1] + e["box"][3] + 1]
            if not inner:
                continue
            gap = max(inner) - r["onsetMs"]
            ok = gap <= AHEAD_MS
            out.append({
                "code": "V7.2", "ok": ok,
                "msg": "%s 空框悬挂 %.1fs" % (e["id"], gap / 1000),
                "detail": [] if ok else [
                    "%s 的框 %.2fs 就开始出现，最后一件内容 %.2fs 才到位（空框挂了 %.1fs）"
                    % (e["id"], r["onsetMs"] / 1000, max(inner) / 1000, gap / 1000)],
            })
    return out


def summarize(design, align, appear):
    """给报告用的几行数，别让人从一堆 pass 里反推。"""
    bound = _bound_of(design)
    align_by = {r["element"]: r for r in align}
    appear_by = {r["element"]: r for r in appear}
    lags, leads = [], []
    for e in bound:
        a, p = align_by.get(e), appear_by.get(e)
        if a and a.get("delayMs") is not None:
            lags.append(a["delayMs"] - a.get("offsetMs", 0))
        if p and p.get("settledMs") is not None:
            leads.append(p["at"] + p.get("offsetMs", 0) - p["settledMs"])
    return {
        "bound": len(bound),
        "measured": len([e for e in bound if e in align_by and e in appear_by]),
        "maxLagMs": max(lags) if lags else None,
        "minLagMs": min(lags) if lags else None,
        "maxLeadMs": max(leads) if leads else None,
        "unbound": len([e for e in appear_by if e not in bound]),
    }


# ---------------------------------------------------------------- IO 装配

def load_inputs(args):
    design = json.load(open(args.segments, encoding="utf-8"))
    elems = (json.load(open(args.elements, encoding="utf-8"))
             if args.elements and os.path.exists(args.elements) else None)
    align = json.load(open(args.alignment, encoding="utf-8"))
    appear = json.load(open(args.appearance, encoding="utf-8"))
    film = probe_film(args.film)
    film["mtime"] = os.path.getmtime(args.film)
    if args.motion and os.path.exists(args.motion):
        film["motion"] = json.load(open(args.motion, encoding="utf-8"))
    if args.focus and os.path.exists(args.focus):
        film["focus"] = json.load(open(args.focus, encoding="utf-8"))
    if args.chart and os.path.exists(args.chart):
        film["chart"] = json.load(open(args.chart, encoding="utf-8"))
    if args.words and os.path.exists(args.words):
        film["words"] = json.load(open(args.words, encoding="utf-8"))
    if args.skip_sync:
        film["lag"] = None
    else:
        if not os.path.exists(args.ref_audio):
            raise FileNotFoundError("参考音轨不存在：%s（用 --skip-sync 跳过音画检查）"
                                    % args.ref_audio)
        import check_av_sync
        s = check_av_sync.measure(args.film, args.ref_audio)
        film.update({"lag": s["lag"], "sharp": s["sharp"],
                     "refDurMs": round(s["durRef"] * 1000)})
    return design, align, appear, film, elems


def stale(design_paths, film_mtime):
    """报告比成片旧就作废。改了片子忘记重测，会让门禁对着旧数据说「全过」。"""
    out = []
    for p in design_paths:
        if os.path.exists(p) and os.path.getmtime(p) < film_mtime - 1:
            out.append("%s 比成片旧（%.0fs）" % (os.path.basename(p),
                                               film_mtime - os.path.getmtime(p)))
    return out


def elements_stale(elements_path, report_paths, now=None):
    """elements.json 比报告新 = 包围盒换了但报告还是按旧版面量的。

    注意方向：元素集是**输入**，比成片新是正常的（先有版面再渲染）；
    只有比报告新才说明「报告没跟上」。把它并进 V6.1 会让任何正常顺序都误报。
    """
    if not os.path.exists(elements_path):
        return []
    rep_m = min(os.path.getmtime(p) for p in report_paths if os.path.exists(p))
    el_m = os.path.getmtime(elements_path)
    if el_m <= rep_m + 1:
        return []
    return ["elements.json 比报告新 %.0fs：包围盒改了，报告要对新版面重测" % (el_m - rep_m)]


def rerun(args):
    """重跑三个测量。--rerun 存在的理由：门禁读的是报告，报告得先是真的。"""
    py = sys.executable or "python3"
    for cmd in (
        [py, os.path.join(HERE, "check_alignment.py"), args.film, args.segments, args.elements,
         "--out", args.alignment],
        [py, os.path.join(HERE, "check_appearance.py"), args.film, args.segments, args.elements,
         "--out", args.appearance],
        [py, os.path.join(HERE, "check_av_sync.py"), args.film, args.ref_audio],
        [py, os.path.join(HERE, "check_motion.py"), args.film, args.segments,
         "--out", args.motion],
        [py, os.path.join(HERE, "check_chart.py"), args.film, args.segments,
         args.elements, "--out", args.chart],
        [py, os.path.join(HERE, "check_focus.py"), args.film, args.segments,
         args.elements, args.plan, "--out", args.focus],
    ):
        if cmd[1].endswith("check_av_sync.py") and args.skip_sync:
            continue
        print("→ %s" % " ".join(os.path.basename(c) for c in cmd[1:3]))
        r = subprocess.run(cmd, capture_output=True, text=True, errors="replace")
        if r.returncode not in (0,):
            print(r.stdout[-2000:] or r.stderr[-2000:])
            raise SystemExit("测量脚本失败：%s（exit %d）"
                             % (os.path.basename(cmd[1]), r.returncode))


# ---------------------------------------------------------------- 自检

def _fixture():
    """一份「应当全过」的合成数据：1 页 2 段，3 个元素（2 绑 1 未绑）。"""
    design = {"pages": [{"index": 0, "durationMs": 6000, "segments": [
        {"id": "s0", "startMs": 200, "durationMs": 2000, "bind": [
            {"elementId": "a", "offsetMs": 0}, {"elementId": "b", "offsetMs": 300}]},
        {"id": "s1", "startMs": 2600, "durationMs": 2000, "bind": [
            {"elementId": "c", "offsetMs": 0}]},
    ]}]}
    align = [
        {"element": "a", "seg": "s0", "at": 200, "offsetMs": 0, "delayMs": 40,
         "onset": 240, "preMax": 0.0},
        {"element": "b", "seg": "s0", "at": 200, "offsetMs": 300, "delayMs": 340,
         "onset": 540, "preMax": 0.0},
        {"element": "c", "seg": "s1", "at": 2600, "offsetMs": 0, "delayMs": 20,
         "onset": 2620, "preMax": 0.0},
    ]
    appear = [
        {"element": "a", "page": 0, "pageStart": 0, "seg": "s0", "at": 200, "offsetMs": 0,
         "settledMs": 640, "onsetMs": 240, "always": False, "preSegLevel": 0.05},
        {"element": "b", "page": 0, "pageStart": 0, "seg": "s0", "at": 200, "offsetMs": 300,
         "settledMs": 900, "onsetMs": 540, "always": False, "preSegLevel": 0.06},
        {"element": "c", "page": 0, "pageStart": 0, "seg": "s1", "at": 2600, "offsetMs": 0,
         "settledMs": 3020, "onsetMs": 2620, "always": False, "preSegLevel": 0.03},
        {"element": "d", "page": 0, "pageStart": 0, "seg": None, "at": None, "offsetMs": 0,
         "settledMs": 5400, "onsetMs": 5400, "always": False, "preSegLevel": None},
    ]
    film = {"hasAudio": True, "durMs": 6000, "refDurMs": 6010, "meanDb": -26.0,
            "lag": 0.0, "sharp": 0.44,
            "motion": {"silentDiff": 0.25, "sampleMs": 100, "longestMs": 1200,
                       "spans": [{"startMs": 600, "endMs": 1800, "durMs": 1200,
                                  "speechMs": 1100}]},
            "chart": [{"page": 1, "spread": 1.003, "missing": [], "ok": True}]}
    # elems 给空：只有一页，V7 会因「封面页不查」整组跳过。
    # V7 的故障注入用单独的 _layout_fixture()，见 _fixture 之后。
    return design, align, appear, film, []


def _layout_fixture(late_ms):
    """三页（封面/内容/谢幕），内容页有一个容器包着两件内容。

    `late_ms` 是第二件内容比第一件晚多久到位 —— 它同时决定两件事：
    覆盖不到一半持续多久（V7.1）和空框挂多久（V7.2）。
    """
    design = {"pages": [
        {"index": 0, "durationMs": 2000, "segments": [
            {"id": "s0", "startMs": 0, "durationMs": 2000, "bind": []}]},
        {"index": 1, "durationMs": 8000, "segments": [
            {"id": "s1", "startMs": 0, "durationMs": 8000, "bind": []}]},
        {"index": 2, "durationMs": 2000, "segments": [
            {"id": "s2", "startMs": 0, "durationMs": 2000, "bind": []}]},
    ]}
    appear = [
        # 容器先出（onset 2400），内容 2600 / 2600+late_ms
        {"element": "box", "page": 1, "pageStart": 2000, "settledMs": 2700,
         "onsetMs": 2400, "always": False, "preSegLevel": None},
        {"element": "x", "page": 1, "pageStart": 2000, "settledMs": 2600,
         "onsetMs": 2500, "always": False, "preSegLevel": None},
        {"element": "y", "page": 1, "pageStart": 2000, "settledMs": 2600 + late_ms,
         "onsetMs": 2500 + late_ms, "always": False, "preSegLevel": None},
    ]
    # x 占内容面积 40%、y 占 60%：只在 x 到位时覆盖 40% < 50%，V7.1 会看见。
    elems = [{"index": 1, "elements": [
        {"id": "box", "kind": "shape", "box": [0, 0, 1000, 600]},
        {"id": "x", "kind": "bullet", "box": [50, 50, 900, 66]},
        {"id": "y", "kind": "bullet", "box": [50, 400, 900, 100]},
    ]}]
    return design, appear, elems


def self_test():
    """往合成数据里逐条注入故障，断言门禁能红——门禁自己也要有回归。"""
    design, align, appear, film, elems = _fixture()
    cases = []

    def red(code, mutate):
        d = json.loads(json.dumps(design))
        a = json.loads(json.dumps(align))
        p = json.loads(json.dumps(appear))
        f = dict(film)
        mutate(d, a, p, f)
        got = {x["code"]: x["ok"] for x in audit(d, a, p, f, elems)}
        cases.append((code, got.get(code) is False, got.get(code)))

    # 0 基线：不许有任何一项红
    base = audit(design, align, appear, film, elems)
    bad = [x["code"] for x in base if not x["ok"]]
    cases.append(("baseline", not bad, bad))

    red("V1.1", lambda d, a, p, f: f.update(hasAudio=False))
    red("V1.2", lambda d, a, p, f: f.update(durMs=9000))
    red("V1.3", lambda d, a, p, f: f.update(meanDb=-91.0))
    red("V2.1", lambda d, a, p, f: f.update(lag=0.42))
    red("V2.2", lambda d, a, p, f: f.update(sharp=0.95))
    def drop_appear_of_c(d, a, p, f):
        p.remove(next(x for x in p if x["element"] == "c"))

    red("V3.1", drop_appear_of_c)
    red("V3.2", lambda d, a, p, f: d["pages"][0]["segments"][1]["bind"].append(
        {"elementId": "a", "offsetMs": 0}))
    # 盲区：元素在 alignment 窗口起点（at+offset-400=100）之前就到位
    red("V4.1", lambda d, a, p, f: p[1].update(settledMs=60))
    # 剧透：开口前那一帧已经与终态一致
    red("V5.1", lambda d, a, p, f: p[0].update(preSegLevel=0.95))
    red("V5.2", lambda d, a, p, f: a[0].update(delayMs=480))
    # 错落：设计差 300ms，实测差 40ms
    red("V5.3", lambda d, a, p, f: a[1].update(delayMs=240))
    red("V5.4", lambda d, a, p, f: p[3].update(always=True))
    red("V5.5", lambda d, a, p, f: p[0].update(settledMs=None))
    # 静：讲着话，画面 8 秒没变（D 片页 1 的读数）
    red("V8.1", lambda d, a, p, f: f["motion"].update(
        longestMs=8000, spans=[{"startMs": 6700, "endMs": 14700, "durMs": 8000,
                                "speechMs": 7600}]))

    # 图表在说谎：174s 的柱子按 943s 的比例画（旧样片的真实读数 3.92 倍）
    red("V9.1", lambda d, a, p, f: f["chart"][0].update(ok=False, spread=3.92))
    # 标注有值、柱体没画出来：没有比例可算，但同样是假的。
    # red() 按「故障码」查结果，这里两种故障同属 V9.1，得自己断言。
    _f = json.loads(json.dumps(film))
    _f["chart"][0].update(ok=False, missing=["bar1"])
    _got = {x["code"]: x["ok"] for x in audit(design, align, appear, _f, elems)}
    cases.append(("V9.1-missing", _got.get("V9.1") is False, _got.get("V9.1")))

    # V10 讲稿漏讲：图上有 174 和 943 两根柱子，讲稿只念了 174。
    # 这是 F→G 时发现的真实读数，页 2 的四根柱子只讲了两根。
    d10 = {"pages": [{"index": 1, "segments": [
        {"id": "s1", "text": "两核一百七十四秒。"}]}]}
    e10 = [{"index": 1, "elements": [
        {"id": "bar0", "kind": "chart", "box": [0, 0, 10, 10], "value": 174},
        {"id": "bar2", "kind": "chart", "box": [0, 0, 10, 10], "value": 943}]}]
    got10 = {x["code"]: x["ok"] for x in narration_findings(d10, e10)}
    cases.append(("V10.1", got10.get("V10.1") is False, got10))
    # 反向：把两个数都念到就绿。中文读法与图上阿拉伯数字必须折成同一个数。
    d10b = json.loads(json.dumps(d10))
    d10b["pages"][0]["segments"][0]["text"] = "两核一百七十四秒，长片九百四十三秒。"
    got10b = {x["code"]: x["ok"] for x in narration_findings(d10b, e10)}
    cases.append(("V10.1-ok", got10b.get("V10.1") is True, got10b))
    # 解析器本身也要有回归：这个函数错了，V10 会静静地全绿。
    cases.append(("V10-parse",
                  numbers_in("一百七十四秒") == [174.0]
                  and numbers_in("二点七 GB") == [2.7]
                  and numbers_in("174s") == [174.0]
                  and numbers_in("九百四十三") == [943.0],
                  [numbers_in("一百七十四秒"), numbers_in("二点七 GB")]))

    # V11：这一段讲的就是 174，段首就指它 —— 正确，哪怕「一百七十四」1.5s 后才念到。
    # 这是口径改过的地方：旧版会把这条判成「抢话」，于是逼着编排等数字，
    # 结果句首那一秒多画面全亮、不指任何人（s2-3 的原始症状）。
    d11 = {"pages": [{"index": 0, "durationMs": 6000, "segments": [
        {"id": "s0", "startMs": 200, "durationMs": 3000,
         "bind": [{"elementId": "bar", "offsetMs": 0}], "focus": "bar"}]}]}
    w11 = {"segments": {"s0": {"startMs": 200, "words": [
        {"tMs": 0, "dMs": 300, "text": "时间"},
        {"tMs": 1500, "dMs": 600, "text": "一百七十四"}]}}}
    e11 = [{"index": 0, "elements": [{"id": "bar", "kind": "bar", "value": 174}]}]
    p11 = [{"element": "bar", "page": 0, "pageStart": 0, "seg": "s0", "at": 200,
            "offsetMs": 0, "settledMs": 700, "onsetMs": 240, "always": False,
            "preSegLevel": 0.0}]
    got11 = {x["code"]: x["ok"] for x in attention_findings(d11, w11, p11, e11)}
    cases.append(("V11.1-first", got11.get("V11.1") is True, got11))
    # 反向：第一个焦点非要等到念到数字才切（晚 1300ms）→ 红。
    # 这种编排放旧口径里是「标准答案」，而观众听到的正是「讲着它、却没指它」。
    d11b = json.loads(json.dumps(d11))
    d11b["pages"][0]["segments"][0].pop("focus")
    d11b["pages"][0]["segments"][0]["focusAt"] = [{"elementId": "bar", "atMs": 1500}]
    got11b = {x["code"]: x["ok"] for x in attention_findings(d11b, w11, p11, e11)}
    cases.append(("V11.1-late", got11b.get("V11.1") is False, got11b))
    # 句中换人仍要卡数值：同段第二个焦点在句首就切，而它那个数 2.6s 后才念 —— 抢话。
    d11c = {"pages": [{"index": 0, "durationMs": 9000, "segments": [
        {"id": "s0", "startMs": 200, "durationMs": 6000,
         "focusAt": [{"elementId": "bar", "atMs": 0},
                     {"elementId": "bar2", "atMs": 200}]}]}]}
    w11c = {"segments": {"s0": {"startMs": 200, "words": [
        {"tMs": 0, "dMs": 300, "text": "时间"},
        {"tMs": 1500, "dMs": 600, "text": "一百七十四"},
        {"tMs": 3000, "dMs": 500, "text": "三百"}]}}}
    e11c = [{"index": 0, "elements": [
        {"id": "bar", "kind": "bar", "value": 174},
        {"id": "bar2", "kind": "bar", "value": 300}]}]
    p11c = [{"element": "bar", "page": 0, "pageStart": 0, "seg": "s0", "at": 200,
             "offsetMs": 0, "settledMs": 700, "onsetMs": 240, "always": False,
             "preSegLevel": 0.0},
            {"element": "bar2", "page": 0, "pageStart": 0, "seg": "s0", "at": 200,
             "offsetMs": 0, "settledMs": 700, "onsetMs": 240, "always": False,
             "preSegLevel": 0.0}]
    got11c = {x["code"]: x["ok"] for x in attention_findings(d11c, w11c, p11c, e11c)}
    cases.append(("V11.1-second-early", got11c.get("V11.1") is False, got11c))
    # 反向：第二个焦点落在它那个数上 → 绿（否则这条判据只是在惩罚「一句话讲两根」）
    d11d = json.loads(json.dumps(d11c))
    d11d["pages"][0]["segments"][0]["focusAt"][1]["atMs"] = 3000
    got11d = {x["code"]: x["ok"] for x in attention_findings(d11d, w11c, p11c, e11c)}
    cases.append(("V11.1-second-ok", got11d.get("V11.1") is True, got11d))

    # V12：a 在 3200ms 讲完，b 到 4000ms 才开讲 —— 中间 800ms 该放手
    d12 = {"pages": [{"index": 0, "durationMs": 5000, "segments": [
        {"id": "s1", "startMs": 200, "durationMs": 3000, "text": "两核"},
        {"id": "s2", "startMs": 4000, "durationMs": 800, "text": "四核"}]}]}
    plan12 = {"pages": [{"index": 0, "durationMs": 5000, "focus": [
        {"startMs": 400, "elementId": "a"},
        {"startMs": 4000, "elementId": "b"}]}]}
    got12 = {x["code"]: x["ok"] for x in focus_release_findings(d12, plan12)}
    cases.append(("V12.1-hold", got12.get("V12.1") is False, got12))
    # 反向一：给终点 → 绿（否则这条判据只是在惩罚「有焦点」）
    plan12b = json.loads(json.dumps(plan12))
    plan12b["pages"][0]["focus"][0]["endMs"] = 3200
    got12b = {x["code"]: x["ok"] for x in focus_release_findings(d12, plan12b)}
    cases.append(("V12.1-ok", got12b.get("V12.1") is True, got12b))
    # 反向二：终点写在话没说完的位置（2000ms，句尾还有 1200ms）→ 红
    plan12c = json.loads(json.dumps(plan12))
    plan12c["pages"][0]["focus"][0]["endMs"] = 2000
    got12c = {x["code"]: x["ok"] for x in focus_release_findings(d12, plan12c)}
    cases.append(("V12.1-early", got12c.get("V12.1") is False, got12c))

    # V13：同一时刻两根柱子满墨 —— focusDim 少了 i===0 的那个 bug 在成片里的样子
    rep13 = {"pages": [{"index": 1, "pageStartMs": 12000, "violations": [
        {"fromMs": 6100, "toMs": 9000, "fromAbsMs": 18100, "toAbsMs": 21000,
         "frames": 30, "bright": ["bar0", "bar2"], "dimmed": ["bar1"]}]}]}
    got13 = {x["code"]: x["ok"] for x in focus_unique_findings(rep13)}
    cases.append(("V13.1-two", got13.get("V13.1") is False, got13))
    got13b = {x["code"]: x["ok"] for x in focus_unique_findings({"pages": [{"index": 1, "violations": []}]})}
    cases.append(("V13.1-ok", got13b.get("V13.1") is True, got13b))

    # V14：切换一帧跳完 —— focusDim 的 i===0 分支没给过渡，在成片里的样子
    d14 = {"pages": [{"index": 0, "durationMs": 6000, "segments": [
        {"id": "s0", "startMs": 200, "durationMs": 2500, "bind": [], "focus": "bar0"},
        {"id": "s1", "startMs": 2800, "durationMs": 2500, "bind": [], "focus": "bar1"}]}]}

    def _steps(peaks):
        """peaks: {tMs: block}，其余帧一律 0.01（几乎不变）。"""
        out = [{"tMs": t, "block": 0.01} for t in range(0, 6000, 100)]
        for s in out:
            if s["tMs"] in peaks:
                s["block"] = peaks[s["tMs"]]
        return {"steps": out}

    hard = _steps({300: 48.0, 2900: 48.0})                       # 每次切换只动一帧
    got14 = {x["code"]: x["ok"] for x in focus_transition_findings(d14, hard)}
    cases.append(("V14.1-hard", got14.get("V14.1") is False, got14))
    soft = _steps({300: 20.0, 400: 20.0, 500: 20.0,
                   2900: 20.0, 3000: 20.0, 3100: 20.0})          # 跨三帧的过渡
    got14b = {x["code"]: x["ok"] for x in focus_transition_findings(d14, soft)}
    cases.append(("V14.1-soft", got14b.get("V14.1") is True, got14b))


    d7, p7, e7 = _layout_fixture(late_ms=4000)
    got7 = {x["code"]: x["ok"] for x in layout_findings(d7, p7, e7)}
    cases.append(("V7.1", got7.get("V7.1") is False, got7.get("V7.1")))
    cases.append(("V7.2", got7.get("V7.2") is False, got7.get("V7.2")))
    # 反向：晚 600ms 到（正常的分步出现），两项都得绿
    d7b, p7b, e7b = _layout_fixture(late_ms=600)
    got7b = {x["code"]: x["ok"] for x in layout_findings(d7b, p7b, e7b)}
    cases.append(("V7-normal", all(got7b.values()), got7b))

    # V5.4 反例：页首常驻的是**容器**（把别的元素整个包住）——那是版式，不是空转。
    # 容器判定与 V7.2 同口径；没有 elems 时退化成「一律按内容算」，所以这里必须给 elems。
    d4, p4, e4 = _layout_fixture(late_ms=600)
    p4 = [dict(r) for r in p4]
    p4[0].update(always=True, settledMs=0, onsetMs=0)  # box 页首就在
    got4 = {x["code"]: x["ok"] for x in audit(d4, [], p4, film, e4)}
    cases.append(("V5.4-container", got4.get("V5.4") is True, got4.get("V5.4")))

    # 新鲜度单独测：报告比成片旧
    with tempfile.TemporaryDirectory() as tmp:
        rp = os.path.join(tmp, "report.json")
        open(rp, "w").close()
        os.utime(rp, (1_000_000, 1_000_000))
        cases.append(("V6.1", bool(stale([rp], 2_000_000)), stale([rp], 2_000_000)))
        os.utime(rp, (3_000_000, 3_000_000))
        cases.append(("V6.1-fresh", not stale([rp], 2_000_000), None))

        # V6.2 方向要分开测：元素集比报告新 = 报告没跟上（红）；
        # 元素集比报告旧 = 正常顺序（绿）。反过来设，会让每次正常出片都误报。
        el = os.path.join(tmp, "elements.json")
        open(el, "w").close()
        os.utime(el, (4_000_000, 4_000_000))
        cases.append(("V6.2", bool(elements_stale(el, [rp])), elements_stale(el, [rp])))
        os.utime(el, (2_000_000, 2_000_000))
        cases.append(("V6.2-ok", not elements_stale(el, [rp]), None))

    bad = 0
    for code, ok, extra in cases:
        print(("  ok   " if ok else "  FAIL ") + code + ("" if ok else "  %r" % (extra,)))
        if not ok:
            bad += 1
    print("\n%d/%d 通过" % (len(cases) - bad, len(cases)))
    return 0 if not bad else 1


# ---------------------------------------------------------------- CLI

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--film", default="tmp/q7/film_C_narrated.mp4")
    ap.add_argument("--segments", default="tmp/q7/segments.json")
    ap.add_argument("--elements", default="tmp/q7/fid/elements.json")
    ap.add_argument("--alignment", default="tmp/q7/alignment.json")
    ap.add_argument("--appearance", default="tmp/q7/appearance.json")
    ap.add_argument("--motion", default="tmp/q7/motion.json",
                    help="check_motion.py 的输出：讲着话时画面静止的区间")
    ap.add_argument("--chart", default="tmp/q7/chart.json",
                    help="check_chart.py 的输出：每根柱子的实测高度与标注值")
    ap.add_argument("--ref-audio", default="tmp/q7/audio/timeline.wav")
    ap.add_argument("--words", default="tmp/q7/words.json",
                    help="narrate.py 的逐词边界：V11 的参照物")
    ap.add_argument("--plan", default="tmp/q7/job/plan.json",
                    help="编译后的计划：V12 审压暗区间有没有终点；不存在则跳过 V12")
    ap.add_argument("--focus", default="tmp/q7/focus.json",
                    help="check_focus.py 的输出：V13 看「同一时刻亮着几个」")
    ap.add_argument("--rerun", action="store_true", help="先重跑三个测量再审计")
    ap.add_argument("--skip-sync", action="store_true", help="跳过音画检查（无参考音轨时）")
    ap.add_argument("--json", default="tmp/q7/verify.json")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    for p in (args.film, args.segments):
        if not os.path.exists(p):
            print("缺输入：%s" % p)
            return 2
    if args.rerun:
        rerun(args)
    for p in (args.alignment, args.appearance):
        if not os.path.exists(p):
            print("缺报告：%s（用 --rerun 生成）" % p)
            return 2

    design, align, appear, film, elems = load_inputs(args)
    plan = None
    if args.plan and os.path.exists(args.plan):
        with open(args.plan, encoding="utf-8") as fh:
            plan = json.load(fh)
    findings = audit(design, align, appear, film, elems, plan)
    # 报告必须比成片新：改了片子忘记重测，门禁就会对着旧数据说「全过」。
    reports = [args.alignment, args.appearance]
    findings.append({"code": "V6.1", "ok": not stale(reports, film["mtime"]),
                     "msg": "报告不旧于成片",
                     "detail": stale(reports, film["mtime"])})
    # elements.json 是**输入**不是报告：它比成片新是正常的（先有版面再渲染），
    # 比报告新才是问题——那意味着包围盒换了、而报告还是按旧版面量的。
    # 曾经把它并进 V6.1，结果任何「先放元素、后渲染」的顺序都会误报。
    el_detail = elements_stale(args.elements, reports)
    findings.append({"code": "V6.2", "ok": not el_detail,
                     "msg": "elements.json 不新于测量报告", "detail": el_detail})

    s = summarize(design, align, appear)
    print("成片 %s · %.3fs · 音轨 %s · mean %s dB" % (
        os.path.basename(args.film), film["durMs"] / 1000,
        "有" if film["hasAudio"] else "无",
        "-" if film.get("meanDb") is None else "%.1f" % film["meanDb"]))
    print("绑定 %d 个 / 两份报告都测到 %d 个 · 未绑定 %d 个" % (
        s["bound"], s["measured"], s["unbound"]))
    # maxLead 是「开口时刻 − 到位时刻」：负数表示最晚的那一件也已经在开口后才到位。
    # 直接用符号说人话，不然 -42 会被读成「提前了 -42ms」。
    lead = s["maxLeadMs"]
    print("开始动的延迟 %s ~ %s ms（已扣设计错落）· 最晚到位：%s\n" % (
        s["minLagMs"], s["maxLagMs"],
        "开口后 %dms" % -lead if lead is not None and lead < 0
        else "开口前 %sms" % lead if lead is not None else "-"))
    for f in findings:
        print(("  ok   " if f["ok"] else "  FAIL ") + "%-5s %s" % (f["code"], f["msg"]))
        for d in f["detail"]:
            print("         · " + d)

    if args.json:
        os.makedirs(os.path.dirname(args.json) or ".", exist_ok=True)
        json.dump({"film": os.path.basename(args.film), "summary": s,
                   "findings": findings}, open(args.json, "w", encoding="utf-8"),
                  ensure_ascii=False, indent=1)

    bad = [f["code"] for f in findings if not f["ok"]]
    print("\n%s" % ("全部通过" if not bad else "阻断项：%s" % ", ".join(bad)))
    return 0 if not bad else 1


if __name__ == "__main__":
    sys.exit(main())
