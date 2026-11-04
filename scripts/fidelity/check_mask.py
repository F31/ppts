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


def measure_page(ref, samples, spans, page_ms):
    """一页的汇总：hold / after 两段各自的强度与孔内残余。"""
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
    return {
        "holdSamples": len(hold_s),
        "holdStrength": round(float(np.mean(hold_s)), 3) if hold_s else None,
        "holdInside": round(float(np.mean(hold_in)), 3) if hold_in else None,
        "afterSamples": len(after_s),
        "afterStrength": round(float(np.mean(after_s)), 3) if after_s else None,
    }


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

        # 参照帧：第一次遮罩开始前 300ms 的最后一帧。同一页、同一套入场动画之后，
        # 与中间帧只差遮罩这一层。遮罩从页首就开始时没有这样的帧——报未测，
        # 而不是拿「遮罩已经压上的那一帧」当基准（那会把强度整体读成 0）。
        cut = first - 300
        before = [(t, f) for t, f in frames if t <= cut]
        if not before:
            report.append({"index": idx, "spans": len(spans), "holdSamples": 0,
                           "skipped": "遮罩在页首 %dms 就开始，没有干净参照帧" % first})
            continue
        ref_t, ref = before[-1]
        summary = measure_page(ref, frames, spans, page_ms)
        summary.update({"index": idx, "spans": len(spans), "refAtMs": ref_t,
                        "holdFromMs": first})
        if summary["holdSamples"] == 0:
            summary["skipped"] = "没有落在稳态的采样点（遮罩太短或全在过渡里）"
        else:
            checked += 1
        report.append(summary)
        if summary.get("skipped"):
            print("页 %d：未测——%s" % (idx, summary["skipped"]))
        else:
            print("页 %d：%d 个稳态采样点，白纱 %.2f，孔内残余 %s，放手后 %s"
                  % (idx, summary["holdSamples"], summary["holdStrength"],
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
