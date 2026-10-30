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

  V7 与 V8 都是后加的，也是仅有的两组**不问元素、只问画面**的：
  前六组全过而人看着不对，差异就在这里。V7 管「每一刻只有半页内容」，
  V8 管「内容铺满之后画面不再变化」——D 片页 1 在第 1.4s 铺满，之后 10.8s 静止，
  而旁白还在逐条念。两个病都不是「元素准不准」，是「该不该在这时候动」。

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
TOL_DUR_MS = 400          # 成片与旁白原件的时长差
MIN_MEAN_DB = -50.0       # 低于这个当静音轨
MAX_SHARP = 0.7           # 互相关峰在 ±100ms 处高于此值 = 峰太宽，lag 不可信
LAYOUT_COVER_MIN = 0.50   # 画面内容覆盖低于此值 = 观众看到半页
LAYOUT_HOLD_MS = 1500     # 半页状态持续多久算问题
AHEAD_MS = 2000           # 容器的框比最后一件内容早多久算「空框悬挂」
SILENT_MAX_MS = 3500      # 讲着话时画面连续多久不变算「静」——本片最长一句 3.24s


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

def _bound_of(design):
    """设计意图 → {元素: [(段号, 段内错落)]}，顺序保留，用来发现重复绑定。"""
    out = {}
    for page in design["pages"]:
        for seg in page["segments"]:
            for ref in seg.get("bind", []):
                out.setdefault(ref["elementId"], []).append(
                    (seg["id"], ref.get("offsetMs", 0)))
    return out


def audit(design, align, appear, film, elems=None):
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
                                  "speechMs": 1100}]}}
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

    # V7：内容页上第二件内容晚 4s 才到 —— 半页空了 4s，框也挂了 4s
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
    ap.add_argument("--ref-audio", default="tmp/q7/audio/timeline.wav")
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
    findings = audit(design, align, appear, film, elems)
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
