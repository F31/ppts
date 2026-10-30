#!/usr/bin/env python3
"""图表有没有在说谎：**柱子的高度，和它标注的数值成不成比例。**

## 为什么需要它

样片第 2 页原来是这样画的：每根柱子配一个手填的 `ratio`（0.72 / 0.54 / 1.0 / 1.0）。
于是 174s 被画成 943s 的 72%，而它实际只有 18%——图上「50 秒的片子和 300 秒的片子
差不多贵」，数据说的正好相反，差了近 4 倍。

那时门禁 20 项全过。因为它们量的全是「元素在不在、准时不准时、空不空、动不动」，
**没有一条问过「图里画的东西和它自己标的数字对不对得上」**。这是量表的第三类盲区：
前两类是「元素级 vs 画面级」，这一类是「画面级 vs 数据级」。

## 怎么量

对每个带 `value` 的元素：取该页**末帧**（动画早已收敛），在它的包围盒里逐行算
「这一行有多少像素不是背景色」，从下往上数出柱体高度，再与 `value` 求比值。

只判**比例**，不判绝对值：判「柱高 = 值 × 常数」比判「柱高 = 值 ÷ 轴上限 × 像素」
稳得多——后者要认刻度、认轴上限，而轴怎么画是版式自由，前者是图表唯一不能违反的约定。

## 边界

- 只在页末量：动画中间的柱高是设计出来的，不是数据。
- 只认得「实心柱」：折线、饼图、堆叠柱都不适用，脚本遇到没有 `value` 的元素直接跳过。
- 量不出绝对坐标误差：柱底画在亚像素位置时高度会差 1–2px，所以容差给到 8%。

用法：
  python3 scripts/fidelity/check_chart.py [film] [segments.json] [elements.json]
输出：tmp/q7/chart.json
"""
import json
import os
import subprocess
import sys
import tempfile

import numpy as np
from PIL import Image

# 两根柱子的「每单位数值对应多少像素」之间允许的最大倍数差。
# 8% 来自像素量化：最矮的一根只有 72px，±1px 就是 1.4%；再放宽一倍多留点余量。
MAX_SPREAD = 1.08

# 判断「这一行是柱体」时，与背景色的距离阈值（RGB 欧氏距离）。
# 卡片底与柱子之间差着 100 以上，文字边缘也不过 60 上下——这条线落在两者之间。
ROW_DIFF = 60


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def frame_at(film, t_ms):
    """抽一帧，返回 ndarray(H, W, 3) uint8。"""
    with tempfile.TemporaryDirectory() as td:
        path = os.path.join(td, "f.png")
        subprocess.run([ffmpeg_bin(), "-v", "error", "-ss", "%.3f" % (t_ms / 1000.0),
                        "-i", film, "-frames:v", "1", "-y", path], check=True)
        return np.asarray(Image.open(path).convert("RGB"))


def bar_height(img, box):
    """包围盒里柱体的像素高度：逐行判「是不是柱体」，从下往上数连续的行。

    背景色取**盒子正上方 8px** 的那一列：那里一定是卡片底色（柱子不会越过自己的顶），
    比在盒内找众数稳——盒子在柱子长满时整块都是柱色，找不到背景。
    """
    x, y, w, h = box
    # 用 float32 而不是 int16：差值最大 255，平方 65025 会溢出 int16（32767），
    # 溢出成负数再开方就是 NaN——比较结果全假，柱高被判成 0，而 0 会让「比例」
    # 这一项悄悄通过（0 被排除在最大值/最小值之外）。第一版就栽在这里。
    ref = img[max(0, y - 8), x + w // 2].astype(np.float32)
    rows = img[y:y + h, x:x + w].astype(np.float32)
    dist = np.sqrt(((rows - ref) ** 2).sum(axis=2))
    is_bar = (dist > ROW_DIFF).mean(axis=1) > 0.5
    height = 0
    for v in is_bar[::-1]:
        if not v:
            break
        height += 1
    return height


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_F_narrated.mp4"
    seg_path = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/segments.json"
    elems_path = sys.argv[3] if len(sys.argv) > 3 else "tmp/q7/fid/elements.json"
    out_path = "tmp/q7/chart.json"
    if "--out" in sys.argv:
        out_path = sys.argv[sys.argv.index("--out") + 1]

    seg = json.load(open(seg_path, encoding="utf-8"))
    elems = {p["index"]: p["elements"] for p in json.load(open(elems_path, encoding="utf-8"))}

    # 页起点：段落时间都是相对本页的，成片是逐页首尾相接。
    starts, acc = {}, 0
    for page in seg["pages"]:
        starts[page["index"]] = acc
        acc += page["durationMs"]

    report = []
    for page in seg["pages"]:
        bars = [e for e in elems.get(page["index"], []) if e.get("value")]
        if len(bars) < 2:
            continue
        # 页末前 300ms：入场早已收敛，又还没被切到下一页。
        t = starts[page["index"]] + page["durationMs"] - 300
        img = frame_at(film, t)
        rows = []
        for e in bars:
            h = bar_height(img, e["box"])
            rows.append({
                "element": e["id"], "value": e["value"], "heightPx": h,
                # 每单位数值对应的像素数。柱子成比例 ⇔ 这个值全页一致。
                "pxPerUnit": h / float(e["value"]) if e["value"] else None,
            })
        missing = [r["element"] for r in rows if r["heightPx"] < 2]
        vals = [r["pxPerUnit"] for r in rows if r["pxPerUnit"]]
        spread = max(vals) / min(vals) if vals and min(vals) > 0 else None
        report.append({
            "page": page["index"], "atMs": t, "bars": rows,
            "spread": round(spread, 3) if spread else None,
            "missing": missing,
            # 柱体没画出来也是「图在说谎」：标注写着 131s，画面上一根柱子都没有。
            # 不能因为「没法求比例」就放它过去。
            "ok": bool(spread and spread <= MAX_SPREAD and not missing),
        })

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    json.dump(report, open(out_path, "w", encoding="utf-8"), ensure_ascii=False, indent=1)

    for r in report:
        print("== page %d  @%.2fs  柱高/数值 最大差 %.2f 倍  %s"
              % (r["page"], r["atMs"] / 1000, r["spread"] or 0,
                 "OK" if r["ok"] else "**不成比例**"))
        for b in r["bars"]:
            print("   %-10s 值 %4d 高 %3dpx  %.3f px/单位%s"
                  % (b["element"], b["value"], b["heightPx"], b["pxPerUnit"] or 0,
                     "   ← 柱体缺失" if b["element"] in r["missing"] else ""))
    if not report:
        print("没有带 value 的元素：本片无图表可查")
    print("\n→ %s" % out_path)


if __name__ == "__main__":
    main()
