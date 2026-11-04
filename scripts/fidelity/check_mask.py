#!/usr/bin/env python3
"""矩形焦点遮罩有没有真的画出来：孔开没开、白纱够不够、放手有没有归零。

## 为什么单开一个测量

`focus` 现在有两种形态：指向**图层**（压暗那一层，V13 看「同一时刻亮着几个」），
和指向**一块矩形**（整页盖白、矩形处留孔，用于内嵌图表的柱子——它不是图层）。
第二种形态在成片里如果不生效，画面与没有遮罩时**一模一样**：

- V5 问「该动时动了没」——图表整块出现，动了；
- V7/V8 问「空不空、静不静」——不空不静；
- V11 问「念到 174 时指没指」——它只看 plan 里的 focusAt，不看像素；
- V13 量的是图层墨量，图表柱子不是图层，一页都进不了统计。

也就是说，把 `rect` 在编译时丢掉、或者合成器忘了画遮罩，全片照样全绿。
这一条只问三件事：

  1. **孔开了没有**：矩形内不该被压暗（差 ≈ 0）；只框柱体、标签露在外面时，
     焦点柱自己的数字也会灰掉——那正是 `--label-pad` 那一轮踩到的。
  2. **白纱够不够**：矩形外应当叠上 `1 − FOCUS_DIM_OPACITY = 0.45` 的白。
     不是「看起来淡了」，而是「变白的量 = 0.45 × 原本离白有多远」。
  3. **放手归零没有**：`endMs` 之后必须回到不压暗；否则讲完最后一句画面还灰着，
     而且越到后面越明显。

## 怎么量

参照帧取「第一次遮罩开始前 300ms」的那一帧——同一页、同一套入场动画之后，
只差遮罩这一层。对每个采样点反解出**实际叠了多少白**：

    alpha = mean(cur − ref) / mean(255 − ref)

遮罩若从页首就开始（讲一根柱子讲整页，很常见），前面没有这样的帧。这时退到
**所有遮罩都放开之后**那段干净时间：它是同一页、同一次导出，唯一跨过的时间是
遮罩这一段。代价是参照在采样之后，中间内容可能变过，所以必须过两道门——
参照帧自己要和窗口内早 200ms 的帧几乎一样（说明页面已经静下来），并且每个
采样帧在**孔内**（这里当内容探针）与参照帧一致。任一不满足仍然报「未测」，
理由写清楚是哪道门没过，不拿一个跨了内容变化的数当结论。

它直接与 DIM(0.45) 比：0.45 = 强度刚好，0 = 没画，0.22 = 画淡了，
0.90 = 叠了两层。用 alpha 而不是「与 0.45 的比值」，是为了让报告里的数
能直接和常量对上——比值 1.0 还得回头换算一次才知道白纱多浓。

分母太小（区域本来就接近纯白）时这个比值会被噪声放大，返回 `None` 报「无墨可测」，
而不是给出一个看着像结论的随机数——白底区域本来就没有「变淡」可言。

采样口径与 check_focus 一致：10fps、480×270。遮罩边缘有 8px 羽化（1920 宽），
缩到 480 后约 2px，所以孔内收缩 6px、孔外再让开 8px，避免把过渡带算进任何一边。

用法：
  PPTS_SEG_DIR=tmp/mask-e2e/out python3 scripts/fidelity/check_mask.py \\
      tmp/mask-e2e/out/segment_000.mp4 tmp/mask-e2e/plan.json --out tmp/mask-e2e/mask.json
  python3 scripts/fidelity/check_mask.py --self-test

输出：mask.json。**不判红**——阈值在门禁（verify.py 的 V20）里，测量脚本只给数。
测量脚本非零退出会被 --rerun 当成「工具坏了」而中断，和「测出问题了」长得一样。
"""
import json
import os
import subprocess
import sys

import numpy as np

from _segtime import page_starts

SAMPLE_FPS = 10
SCALE_W, SCALE_H = 480, 270
BASE_W, BASE_H = 1920, 1080

# 非焦点区的目标不透明度是 FOCUS_DIM_OPACITY = 0.55，也就是叠 45% 白。
# 与 shared/motion.ts 同源；mask_preview.py 的自检里有一条守这两个常量不漂。
DIM = 0.45

# 羽化 8px 在 480 宽下约 2px。孔内收缩 6、孔外让开 8，两边都别把过渡带算进去。
INSET = 6
OUTER_GAP = 8

# 区域「几乎没有墨」时不算比值：分母小于这个灰度，噪声就能把结果翻倍。
MIN_INK = 8.0

# 参照帧离遮罩边界起码这么远：淡入淡出 240ms + 一帧余量。比这近的帧上，
# 白纱正在变浓/变淡，拿它当基准会把强度读低或读高。
REF_MARGIN = 300

# 后置参照的两道门。前置参照（同一时刻之前的一帧）只需要「之前」这一条，
# 后置参照跨过了整段遮罩，必须证明画面本身没变过，否则量到的不是白纱：
#   PAIR_MS / PAIR_TOL —— 参照帧与同窗口内早 200ms 的那帧平均差 ≤ 1.5 级，
#     说明取参照的这一刻页面已经静下来了（入场动画收了尾）；
#   HOLE_FIT_TOL —— 参照帧与每个采样帧在**孔内**（这里当内容探针）的差，必须只用
#     一层白就能解释（把叠白量拟合成一个标量后，逐像素平均残差 ≤ 4 级）。
#     孔内没被压暗，所以孔里原本应该一动不动；动了却解释不了，就是内容变了。
REF_PAIR_MS = 200
REF_PAIR_TOL = 1.5
HOLE_FIT_TOL = 4.0


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def probe_ms(path):
    out = subprocess.run([ffmpeg_bin(), "-hide_banner", "-i", path],
                         capture_output=True, text=True, errors="replace").stderr
    for line in out.splitlines():
        if "Duration:" in line:
            h, m, s = line.split("Duration:")[1].split(",")[0].strip().split(":")
            return round((int(h) * 3600 + int(m) * 60 + float(s)) * 1000)
    return 0


def page_frames(film, start_ms, dur_ms):
    """把一页解成缩小后的 gray 帧，逐帧 yield (页内 ms, ndarray)。"""
    span = max(0.0, (dur_ms - 100) / 1000.0)
    cmd = [ffmpeg_bin(), "-v", "error", "-ss", "%.3f" % (start_ms / 1000),
           "-t", "%.3f" % span, "-i", film,
           "-vf", "fps=%d,scale=%d:%d" % (SAMPLE_FPS, SCALE_W, SCALE_H),
           "-f", "rawvideo", "-pix_fmt", "gray", "-"]
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    size = SCALE_W * SCALE_H
    i = 0
    try:
        while True:
            buf = proc.stdout.read(size)
            if len(buf) < size:
                break
            yield int(i * 1000 / SAMPLE_FPS), np.frombuffer(buf, np.uint8).reshape(
                SCALE_H, SCALE_W)
            i += 1
    finally:
        proc.stdout.close()
        proc.wait()


def rect_vals(rect):
    """矩形有两种写法：plan 里是 `{x,y,w,h}`（IR 的 Box），切柱产物里是 `[x,y,w,h]`。
    两种都得吃——写死一种，另一种会在 `v * k` 上抛 TypeError，而报错信息指向的
    是「乘以浮点数」，与真正的原因（字段形态不同）差得很远。"""
    if isinstance(rect, dict):
        return [rect["x"], rect["y"], rect["w"], rect["h"]]
    return list(rect)


def scaled(rect):
    """计划坐标（1920×1080）→ 采样帧坐标。"""
    k = SCALE_W / BASE_W
    x, y, w, h = [int(round(v * k)) for v in rect_vals(rect)]
    x, y = max(0, min(SCALE_W - 1, x)), max(0, min(SCALE_H - 1, y))
    w, h = max(1, min(SCALE_W - x, w)), max(1, min(SCALE_H - y, h))
    return x, y, w, h


def regions(rect):
    """把一帧切成「孔内」与「孔外」两个布尔掩码。

    两块都避开羽化带：孔内收缩 INSET，孔外再让开 OUTER_GAP。它们不重叠，
    所以「内区没被压暗」与「外区被压暗」是两个独立读数——只有这样，
    「孔开在别处」才会同时表现为内区偏暗、外区偏暗一半。
    """
    x, y, w, h = scaled(rect)
    inside = np.zeros((SCALE_H, SCALE_W), dtype=bool)
    inside[y + INSET:y + h - INSET, x + INSET:x + w - INSET] = True
    outer = np.ones((SCALE_H, SCALE_W), dtype=bool)
    pad = INSET + OUTER_GAP
    outer[max(0, y - pad):y + h + pad, max(0, x - pad):x + w + pad] = False
    return inside, outer


def whitening(ref_mean, cur_mean):
    """反解叠白量：cur = ref + a × (255 − ref) 时返回 a；区域太浅返回 None。"""
    denom = 255.0 - ref_mean
    if denom < MIN_INK:
        return None
    return (cur_mean - ref_mean) / denom


def region_means(ref, cur, mask):
    return float(ref[mask].mean()), float(cur[mask].mean())


def classify(spans, t_ms, page_ms):
    """某个采样点属于哪一段：before / hold / after / skip（过渡与夹缝）。

    「过渡」不是宽容，而是**这一刻的读数本来就不代表稳态**：淡入淡出期间
    白纱正在变浓或变淡，量出来的任何一个中间值都不能说明强度对不对。
    """
    if not spans:
        return "skip"
    first_start = spans[0].get("startMs", 0)
    if t_ms < first_start:
        return "before"
    for i, s in enumerate(spans):
        start = s.get("startMs", 0)
        fade = s.get("fadeMs") or 240
        nxt = spans[i + 1]["startMs"] if i + 1 < len(spans) else page_ms
        end = s.get("endMs") or nxt
        if t_ms < start:
            return "skip"
        if t_ms < start + fade:
            return "skip"
        if t_ms >= end:
            continue
        return "hold"
    last = spans[-1]
    fade = last.get("fadeMs") or 240
    if last.get("endMs") and t_ms >= last["endMs"] + fade:
        return "after"
    return "skip"


def clean_windows(spans, page_ms, margin=REF_MARGIN):
    """遮罩完全关闭的时间窗 `(lo, hi)`，按时间升序。

    遮罩的活跃区间是 `[startMs, endMs 或下一段起点)`，两端各留 `margin`——
    淡入从 startMs 开始算，淡出在终点之后还要 `fadeMs` 才回到全亮。
    窗口算窄一点只是少几个候选，算宽了会把过渡帧当干净帧，那才是错的。
    """
    out, cursor = [], 0
    for i, s in enumerate(spans):
        start = s.get("startMs", 0)
        nxt = spans[i + 1]["startMs"] if i + 1 < len(spans) else page_ms
        end = s.get("endMs") or nxt
        if start - margin > cursor:
            out.append((cursor, start - margin))
        cursor = max(cursor, end + margin)
    if page_ms - margin > cursor:
        out.append((cursor, page_ms - margin))
    return out


def _settled(cand):
    """在候选帧里找一对「相隔 200ms 却几乎一样」的帧，返回较晚的那一帧。

    只用时间差判断不够：入场动画正好在这 200ms 里做完，两帧也会不一样。
    所以真去比像素——「页面已经静下来」这件事只能看出来，不能假定。
    """
    for i in range(len(cand) - 1, -1, -1):
        t, f = cand[i]
        for j in range(i - 1, -1, -1):
            t0, f0 = cand[j]
            if t - t0 < REF_PAIR_MS:
                continue
            d = float(np.abs(f.astype(np.float32) - f0.astype(np.float32)).mean())
            if d <= REF_PAIR_TOL:
                return t, f
            break
    return None


def pick_ref(frames, spans, page_ms, first):
    """选参照帧。返回 `(t, frame, mode, reason)`；选不到时 t/frame 为 None。

    优先前置（遮罩开始前 300ms）——它天然跨不过任何一次内容变化。
    页首就开始时退到末尾那段干净时间，并证明这一刻页面是静的。
    """
    cut = first - REF_MARGIN
    before = [(t, f) for t, f in frames if t <= cut]
    if before:
        t, f = before[-1]
        return t, f, "before", ""
    wins = clean_windows(spans, page_ms)
    tail = [w for w in wins if w[0] >= (spans[-1].get("endMs") or page_ms)]
    if not tail:
        return None, None, "", ("遮罩从页首 %dms 开始，且到页末都没放开，没有干净参照帧"
                                % first)
    lo, hi = tail[-1]
    cand = [(t, f) for t, f in frames if lo <= t <= hi]
    if not cand:
        return None, None, "", "遮罩结束后没有解出可用的干净帧"
    settled = _settled(cand)
    if settled is None:
        return None, None, "", ("遮罩结束后画面仍在动（找不到相隔 %dms 却一致的两帧），"
                                "后置参照不可用" % REF_PAIR_MS)
    t, f = settled
    return t, f, "after", ""


def fit_alpha(ref_px, cur_px):
    """最小二乘拟合 `cur ≈ ref + a × (255 − ref)`，返回 `(a, 平均残差)`。

    后置参照要回答的不是「孔内的数变没变」，而是「变的这个量能不能用一层白解释」：
    能解释就说明参照可用，且**读数本身仍然有效**——a≈0 是孔开对了，a≈0.45 是
    遮罩盖在了它不该盖的地方（矩形写错、或播放器漏了定位），后者正是要判红的形态。
    解释不了（残差大）才是内容真的变了，那时才报未测。
    """
    d = 255.0 - ref_px
    den = float((d * d).sum())
    if den < 1.0:
        return None, None
    a = float(((cur_px - ref_px) * d).sum() / den)
    resid = float(np.abs(cur_px - (ref_px + a * d)).mean())
    return a, resid


def measure_page(ref, samples, spans, page_ms, ref_mode="before"):
    """一页的汇总：hold / after 两段各自的强度与孔内残余。"""
    note = ""
    if ref_mode == "after":
        # 后置参照跨过了整段遮罩：先证明孔内的变化只用一层白就能解释。
        worst, worst_at = -1.0, 0
        for t, frame in samples:
            if classify(spans, t, page_ms) not in ("hold", "after"):
                continue
            span = _span_at(spans, t, page_ms)
            if not span or not span.get("rect"):
                continue
            inside, _ = regions(span["rect"])
            _, resid = fit_alpha(ref[inside].astype(np.float32),
                                 frame[inside].astype(np.float32))
            if resid is None:
                continue
            if resid > worst:
                worst, worst_at = resid, t
        if worst > HOLE_FIT_TOL:
            return {"holdSamples": 0, "holdStrength": None, "holdInside": None,
                    "afterSamples": 0, "afterStrength": None,
                    "skipped": ("孔内内容在参照帧与 %dms 之间变了（残差 %.1f，"
                                "不是一层白能解释的），后置参照不可用"
                                % (worst_at, worst))}
        if worst < 0:
            note = "孔内几乎无墨，未做内容一致性校验"

    hold_s, hold_in, after_s = [], [], []

    for t, frame in samples:
        kind = classify(spans, t, page_ms)
        if kind not in ("hold", "after"):
            continue
        # 每一段都要用**自己**的矩形当孔：一页里可以先后指不同的柱子。
        span = _span_at(spans, t, page_ms)
        if not span or not span.get("rect"):
            continue
        inside, outer = regions(span["rect"])
        ref_in, cur_in = region_means(ref, frame, inside)
        ref_out, cur_out = region_means(ref, frame, outer)
        a_out = whitening(ref_out, cur_out)
        a_in = whitening(ref_in, cur_in)
        if a_out is None:
            continue
        if kind == "hold":
            hold_s.append(a_out)
            # 孔内若本来就没墨（纯白柱子区），这个读数没有意义，别拿它判。
            if a_in is not None:
                hold_in.append(a_in)
        else:
            after_s.append(a_out)
    out = {
        "holdSamples": len(hold_s),
        "holdStrength": round(float(np.mean(hold_s)), 3) if hold_s else None,
        "holdInside": round(float(np.mean(hold_in)), 3) if hold_in else None,
        "afterSamples": len(after_s),
        "afterStrength": round(float(np.mean(after_s)), 3) if after_s else None,
    }
    if note:
        out["note"] = note
    return out


def _span_at(spans, t_ms, page_ms):
    cur = None
    for s in spans:
        if s.get("startMs", 0) <= t_ms:
            cur = s
        else:
            break
    return cur


def main():
    argv = sys.argv[1:]
    out_path = "tmp/q7/mask.json"
    if "--out" in argv:
        k = argv.index("--out")
        out_path = argv[k + 1]
        argv = argv[:k] + argv[k + 2:]
    film = argv[0] if len(argv) > 0 else "tmp/q7/film_M_narrated.mp4"
    plan_path = argv[1] if len(argv) > 1 else "tmp/q7/job/plan.json"

    plan = json.load(open(plan_path, encoding="utf-8"))
    # 没有矩形焦点的片子直接出空报告就走：绝大多数片子属于这一类，不该为了
    # 「什么都没有」去解一遍段时长——那会让这条测量依赖 PPTS_SEG_DIR，
    # 而它对结果毫无影响。
    if not any(f.get("rect") for p in plan.get("pages") or [] for f in (p.get("focus") or [])):
        os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
        with open(out_path, "w", encoding="utf-8") as fh:
            json.dump({"film": os.path.basename(film), "dim": DIM, "pages": [],
                       "checked": 0, "totalMs": 0,
                       "reason": "本片没有矩形焦点（focus 里没有 rect）"},
                      fh, ensure_ascii=False, indent=1)
        print("→ %s（本片无矩形焦点，跳过测量）" % out_path)
        return 0
    # 段文件只用来算页起点：一页一片段，页长直接取 plan 的 durationMs。
    script = {"pages": [{"index": p.get("index", 0), "durationMs": p.get("durationMs", 0)}
                        for p in plan.get("pages") or []]}
    starts, total = page_starts(script, probe_ms)

    report, checked = [], 0
    for page in plan.get("pages") or []:
        idx = page.get("index", 0)
        spans = [f for f in (page.get("focus") or []) if f.get("rect")]
        if not spans:
            continue
        page_ms = page.get("durationMs", 0)
        first = spans[0].get("startMs", 0)
        frames = list(page_frames(film, starts[idx], page_ms))
        if not frames:
            report.append({"index": idx, "spans": len(spans), "skipped": "这一页没有解出帧"})
            continue

        # 参照帧：优先「第一次遮罩开始前 300ms 的最后一帧」——同一页、同一套入场
        # 动画之后，与中间帧只差遮罩这一层。遮罩从页首就开始时退到末尾的干净窗口
        # （见 pick_ref）：那种片子里「讲一根柱子的同时压暗整页」是常态，不能
        # 因为参照在前面拿不到就整页不算——但也不能随手拿一帧，所以有两道门。
        ref_t, ref, ref_mode, why = pick_ref(frames, spans, page_ms, first)
        if ref is None:
            report.append({"index": idx, "spans": len(spans), "holdSamples": 0,
                           "skipped": why})
            continue
        summary = measure_page(ref, frames, spans, page_ms, ref_mode)
        summary.update({"index": idx, "spans": len(spans), "refAtMs": ref_t,
                        "refMode": ref_mode, "holdFromMs": first})
        if summary["holdSamples"] == 0:
            summary["skipped"] = "没有落在稳态的采样点（遮罩太短或全在过渡里）"
        else:
            checked += 1
        report.append(summary)
        if summary.get("skipped"):
            print("页 %d：未测——%s" % (idx, summary["skipped"]))
        else:
            print("页 %d：%d 个稳态采样点（参照 %dms，取自遮罩%s），白纱 %.2f，孔内残余 %s，放手后 %s"
                  % (idx, summary["holdSamples"], summary["refAtMs"],
                     "之前" if ref_mode == "before" else "之后",
                     summary["holdStrength"],
                     "-" if summary["holdInside"] is None else "%.2f" % summary["holdInside"],
                     "-" if summary["afterStrength"] is None else "%.2f" % summary["afterStrength"]))

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump({"film": os.path.basename(film), "dim": DIM, "pages": report,
                   "checked": checked, "totalMs": total}, fh, ensure_ascii=False, indent=1)
    print("→ %s（%d 页有矩形焦点）" % (out_path, checked))
    return 0


# ---------------------------------------------------------------- 自检

def _frame_with_ink(seed=0):
    """造一帧有墨的灰度图：白底 + 几块深浅不一的块。"""
    rng = np.random.default_rng(seed)
    f = np.full((SCALE_H, SCALE_W), 255.0)
    f[60:200, 40:200] = 90
    f[80:160, 220:320] = 150
    f[40:100, 330:440] = 210
    f += rng.normal(0, 1.5, f.shape)
    return np.clip(f, 0, 255)


def _overlay(frame, rect, alpha):
    """按真实合成顺序叠一层白：孔内不动，孔外 a × 白。"""
    out = frame.copy()
    inside, _ = regions(rect)
    out[~inside] = out[~inside] * (1 - alpha) + 255 * alpha
    return out


def _self_test():
    cases = []
    rect = [385, 384, 173, 415]
    ref = _frame_with_ink()
    inside, outer = regions(rect)

    # 1. 强度算得准：叠 0.45 白 → 读数 0.45（与 DIM 直接可比）
    cur = _overlay(ref, rect, DIM)
    a = whitening(*region_means(ref, cur, outer))
    cases.append(("叠 45%% 白 → 0.45（实得 %.4f）" % a,
                  a is not None and abs(a - DIM) < 1e-3))

    # 2. 没画遮罩 → 0
    a0 = whitening(*region_means(ref, ref, outer))
    cases.append(("没画 → 0.00（实得 %.4f）" % a0, a0 is not None and abs(a0) < 1e-6))

    # 3. 画淡了（0.225）→ 0.22：判据要能把「强度不对」与「没画」分开
    ah = whitening(*region_means(ref, _overlay(ref, rect, DIM / 2), outer))
    cases.append(("画一半 → 0.22（实得 %.4f）" % ah,
                  ah is not None and abs(ah - DIM / 2) < 1e-3))

    # 4. 孔没开：整页都盖白 → 孔内读数也等于 DIM（这正是 position:absolute 那个 bug）
    flat = np.clip(ref * (1 - DIM) + 255 * DIM, 0, 255)
    ai = whitening(*region_means(ref, flat, inside))
    cases.append(("孔没开 → 孔内也为 %.2f（应 %.2f）" % (ai, DIM),
                  ai is not None and abs(ai - DIM) < 1e-3))

    # 5. 正常时孔内应当 ≈ 0
    ai_ok = whitening(*region_means(ref, cur, inside))
    cases.append(("孔正常 → 孔内 %.4f" % ai_ok, ai_ok is not None and abs(ai_ok) < 1e-6))

    # 6. 纯白区域不冒充结论
    white = np.full((SCALE_H, SCALE_W), 253.0)
    cases.append(("近白区域 → None（分母 %.0f < %.0f）" % (255 - 253, MIN_INK),
                  whitening(*region_means(white, white, outer)) is None))

    # 7. 时间分类：前 / 稳态 / 过渡 / 放手
    spans = [{"startMs": 1000, "endMs": 3000, "fadeMs": 240, "rect": rect}]
    got = [classify(spans, t, 4000) for t in (0, 1100, 2000, 3100, 3500)]
    cases.append(("分类 before/skip/hold/skip/after → %s" % got,
                  got == ["before", "skip", "hold", "skip", "after"]))

    # 8. 两种矩形写法等价：plan 是 {x,y,w,h}，切柱产物是 [x,y,w,h]
    cases.append(("dict 与 list 等价 → %s" % (scaled({"x": 385, "y": 384, "w": 173, "h": 415}),),
                  scaled({"x": 385, "y": 384, "w": 173, "h": 415}) == scaled(rect)))

    # 9. 多段：第二段的稳态不该被第一段的矩形量到
    two = [{"startMs": 0, "endMs": 2000, "fadeMs": 240, "rect": [0, 0, 100, 100]},
           {"startMs": 2000, "endMs": 4000, "fadeMs": 240, "rect": rect}]
    cases.append(("多段取当前矩形 → %s" % _span_at(two, 3000, 4000)["rect"],
                  _span_at(two, 3000, 4000)["rect"] == rect))
    cases.append(("多段第二段起点是过渡 → %s" % classify(two, 2100, 4000),
                  classify(two, 2100, 4000) == "skip"))

    # 10. 干净窗口：遮罩在页中时前后各有一段；页首就开始时只剩末尾那一段
    head = [{"startMs": 0, "endMs": 2500, "fadeMs": 240, "rect": rect}]
    mid = [{"startMs": 1000, "endMs": 3000, "fadeMs": 240, "rect": rect}]
    cases.append(("页首遮罩的干净窗口 → %s" % (clean_windows(head, 4000),),
                  clean_windows(head, 4000) == [(2800, 3700)]))
    cases.append(("页中遮罩的干净窗口 → %s" % (clean_windows(mid, 4000),),
                  clean_windows(mid, 4000) == [(0, 700), (3300, 3700)]))

    # 11. 页首就开始时，参照退到末尾且能读回同一强度（这一条是补洞的正面用例）
    def _film(spans, page_ms):
        out = []
        for t in range(0, page_ms, 100):
            masked = any(s["startMs"] <= t < s["endMs"] for s in spans)
            out.append((t, _overlay(ref, rect, DIM) if masked else ref.copy()))
        return out

    frames_head = _film(head, 4000)
    t_ref, f_ref, mode, why = pick_ref(frames_head, head, 4000, 0)
    cases.append(("页首遮罩 → 后置参照（%s @ %sms）" % (mode, t_ref),
                  mode == "after" and t_ref is not None and t_ref >= 2800))
    got = measure_page(f_ref, frames_head, head, 4000, mode) if f_ref is not None else {}
    cases.append(("后置参照读回 %.2f（应 %.2f）" % (got.get("holdStrength") or -1, DIM),
                  got.get("holdStrength") is not None
                  and abs(got["holdStrength"] - DIM) < 1e-3))

    # 12. 参照之后画面还在动 → 宁可不测，也不拿动着的帧当基准
    moving = [(t, f + (t % 400) * 0.5) for t, f in frames_head]
    moving = [(t, np.clip(f, 0, 255)) for t, f in moving]
    _, _, mode_m, why_m = pick_ref(moving, head, 4000, 0)
    cases.append(("参照窗口内画面在动 → 未测（%s）" % (why_m[:18] or mode_m),
                  mode_m == "" and "仍在动" in why_m))

    # 13. 后置参照的孔内若真的变了（不是一层白能解释的）→ 未测。
    #     用「内容挪了位置」而不是「整块变亮」：后者与叠一层白在数学上同形，
    #     本来就分不开——而它也不会被静默放过，孔内读数会直接偏高、V20 判红。
    changed = [(t, f.copy()) for t, f in frames_head]
    for i, (t, f) in enumerate(changed):
        if t >= 2800:
            # 孔内（480 坐标约 rows 100–194 / cols 100–135）原来是一块均匀的墨，
            # 改成条纹——结构变了，一层白再怎么调都盖不出来。
            f[100:195, 100:136] = np.where(
                np.arange(95)[:, None] % 8 < 4, 40, 200)
            changed[i] = (t, f)
    _, f_ref2, mode2, _ = pick_ref(changed, head, 4000, 0)
    got2 = measure_page(f_ref2, changed, head, 4000, mode2) if f_ref2 is not None else {}
    cases.append(("孔内内容变了 → 未测（%s）" % (got2.get("skipped", "-")[:20]),
                  got2.get("holdSamples") == 0 and "残差" in (got2.get("skipped") or "")))

    # 14. 但「孔开在别处」不是内容变化——它是一层白，必须量得出来而不是报未测
    shifted = rect[:]
    shifted[0] += 400                          # 计划说孔在 A，实际遮罩盖住了 A
    hole_wrong = []
    for t, f in frames_head:
        masked = 0 <= t < 2500
        hole_wrong.append((t, _overlay(ref, shifted, DIM) if masked else ref.copy()))
    _, f_ref3, mode3, _ = pick_ref(hole_wrong, head, 4000, 0)
    got3 = measure_page(f_ref3, hole_wrong, head, 4000, mode3) if f_ref3 is not None else {}
    cases.append(("孔开在别处 → 孔内读到 %.2f（不是未测）" % (got3.get("holdInside") or -1),
                  got3.get("holdInside") is not None
                  and abs(got3["holdInside"] - DIM) < 0.05))


    bad = 0
    for name, ok in cases:
        print(("  ok   " if ok else "  FAIL ") + name)
        if not ok:
            bad += 1
    print("\n%d/%d 通过" % (len(cases) - bad, len(cases)))
    return 0 if not bad else 1


if __name__ == "__main__":
    if "--self-test" in sys.argv[1:]:
        sys.exit(_self_test())
    sys.exit(main())
