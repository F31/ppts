#!/usr/bin/env python3
"""把内嵌图表按**类别**切成一根一根的柱子，给 V11 一个能指的对象。

## 为什么需要它

`extract_pptx_elements.py` 把 chart 形状抽成一块 `chart`（类别 / 系列 / 数值），
V10 因此能问「图上每个数念到没有」。但 V11 问的是**时间**：念到 174 的那一刻，
注意力有没有正好挪到那根柱子上。整块图表只有一个 id，`focus` 只能指向整块——
观众看到的是「整张图一起变暗」，等于没指。

要指单柱，就得先有单柱的 id 和包围盒。而包围盒 PPTX 里没有：图表是一个形状，
柱子由图表引擎在渲染时排布。所以只能**从页面位图上量**。

## 怎么量

1. 在 chart 元素的包围盒内找「柱色」：高饱和、面积占比最大的那一个色簇。
   白底、灰网格线、黑标签全是低饱和，天然被排除。
2. 同色掩码做列投影，取「这一列有 ≥3% 高度是柱色」的连续列段 = 一根柱子。
3. 段数必须等于 `类别数 × 系列数`，且**宽度一致、底边同高**——否则说明量到的
   不是柱子（图例色块、背景块、色带），直接报未测，不硬凑。
4. 高度顺序必须与数值顺序一致。这是唯一一条「几何对数据」的自校验：如果 x 方向
   映射反了，柱高排序会整体倒过来，宁可报未测也不能把 focus 指到邻居身上。

## 边界

- 只做**垂直柱状图**（`column*`）。水平条形、折线、饼图、面积图一律报未测：
  它们在画面上不是「一根柱子占一个 x 带」，切出来的段没有语义。
- 只认**单色柱**。多系列不同配色时，色簇会分裂成多个，先报未测而不是猜。
  真实客户 deck 的图表以单系列单色为主，这条限制是权衡后的取舍。
- 数据标签、轴线、网格线不进任何柱子的包围盒（它们是低饱和或细线，过不了阈值）。
- 图被裁切到图表框外（罕见）时量不到，同样报未测。

## 用法

    python3 scripts/fidelity/split_chart_bars.py \
        --elements tmp/chart/elements.json --png-dir tmp/chart \
        --out tmp/chart/elements-bars.json

页面图按 `--pattern`（默认 `page-{i}.png`，i 从 1 起）对应元素清单里的 page index。
图片可以是任意尺寸：包围盒统一换算回 1920×1080，与两个样本的坐标系一致。

    python3 scripts/fidelity/split_chart_bars.py --self-test
"""
import argparse
import json
import os
import sys

import numpy as np
from PIL import Image

PAGE_W, PAGE_H = 1920, 1080

# 柱色距离阈值。同一块纯色经 PNG 压缩/缩放后与主色差不超过 10，40 留足余量；
# 再大就会把浅灰网格线（与蓝柱差 ~90）也吃进来。
COLOR_TOL = 40
# 一列里至少这么多比例是柱色，才算「这一列有柱子」。柱宽 116px、高 135–246px 时
# 占比 20–35%；细网格线横跨整幅图，每列只有 1–2px，占比 <1%。
COL_MIN_RATIO = 0.03
# 饱和度门槛：max-min。白(0)、灰(0)、黑(0) 都过不了；蓝柱 (79,129,189) 是 110。
SAT_MIN = 24
# 同一根柱子的宽度抖动容忍（抗锯齿会让边缘列差 1–2px）。
WIDTH_SPREAD_MAX = 1.15
# 底边对齐容忍（px，页面图坐标系）。柱底落在基线上，同页必然齐。
BASE_TOL_PX = 3


def dominant_color(patch):
    """色块主色。返回 None = 这块里没有成规模的饱和色（不是柱状图）。"""
    if patch.size == 0:
        return None
    px = patch.reshape(-1, 3).astype(np.int16)
    mx = px.max(axis=1)
    mn = px.min(axis=1)
    cand = px[(mx - mn >= SAT_MIN) & (mx > 40)]
    # 柱子至少占图表面积的 2%：三根柱、每根 9% 宽 20–35% 高，合计 ~7%。留一半余量。
    if len(cand) < max(64, int(0.02 * len(px))):
        return None
    # 量化到 8 级再统计：抗锯齿边缘的过渡色不会各成一簇。
    q = (cand // 8) * 8
    vals, counts = np.unique(q, axis=0, return_counts=True)
    order = counts.argsort()[::-1]
    if len(order) > 1 and counts[order[1]] > 0.35 * counts[order[0]]:
        # 第二大色簇接近主色：多半是多系列配色。猜错比不猜更糟。
        return None
    return vals[order[0]].astype(np.int16)


def segment_columns(mask):
    """列投影 → 连续列段。每段是 (x0, x1)，含端点。"""
    h = mask.shape[0]
    colsum = mask.sum(axis=0)
    thick = colsum >= max(2, int(COL_MIN_RATIO * h))
    segs, start = [], None
    for x, on in enumerate(thick):
        if on and start is None:
            start = x
        elif not on and start is not None:
            segs.append((start, x - 1))
            start = None
    if start is not None:
        segs.append((start, len(thick) - 1))
    return segs


def detect(patch, n_cats, n_series):
    """页面图里 chart 区域 → 每根柱的像素矩形。返回 (bars, reason)。

    bars 为空时 reason 说明为什么没切成——这句话会原样进报告，所以它必须能
    让人判断「是这页本来没柱，还是我的工具有洞」。
    """
    color = dominant_color(patch)
    if color is None:
        return [], "图表区域里没有成块的单色柱（多系列配色或非柱状图）"
    # float32 而不是 int16：差值最大 255，平方 65025 会溢出 int16（32767），
    # 溢出成负数再开方就是 NaN——比较全假、mask 全空，报出来的却是「没找到柱子」。
    # check_chart.py 的 bar_height 栽过同一个坑，这里不是抄错而是重犯。
    px = patch.astype(np.float32)
    mask = np.sqrt(((px - color) ** 2).sum(axis=2)) < COLOR_TOL
    segs = segment_columns(mask)
    # 太窄的段是抗锯齿残留或图例里的小色块，不是柱子。
    segs = [(a, b) for a, b in segs if b - a + 1 >= 4]
    want = n_cats * n_series
    if len(segs) != want:
        return [], ("量到 %d 个色块，类别×系列应为 %d 个（%d×%d）"
                    % (len(segs), want, n_cats, n_series))

    boxes = []
    for a, b in segs:
        ys = np.where(mask[:, a:b + 1].any(axis=1))[0]
        if len(ys) == 0:
            return [], "色块 %d–%d 里没有柱色像素" % (a, b)
        boxes.append([int(a), int(ys.min()), int(b - a + 1), int(ys.max() - ys.min() + 1)])

    widths = [b[2] for b in boxes]
    if max(widths) / float(min(widths)) > WIDTH_SPREAD_MAX:
        return [], ("色块宽度差 %.2f 倍（%d–%dpx），不像同一张图里的柱子"
                    % (max(widths) / float(min(widths)), min(widths), max(widths)))
    bases = [b[1] + b[3] for b in boxes]
    if max(bases) - min(bases) > BASE_TOL_PX:
        return [], ("色块底边差 %dpx，不在同一条基线上，可能量到了图例或背景"
                    % (max(bases) - min(bases)))
    return boxes, ""


def split_page(page_elems, img, px_per_unit_x, px_per_unit_y):
    """一页的元素 → 就地给 chart 元素补 `bars`。返回改动说明列表。

    `px_per_unit_*` 是**页面图每 1 个元素坐标对应多少像素**（= 图宽 / 1920）。
    方向容易写反：从元素到图是乘，从图回元素是除。第一版就是两边都乘了，
    框全部缩到 2/3，而自检里「高度换算」那条正好把它抓出来。
    """
    notes = []
    for e in page_elems:
        chart = e.get("chart")
        if not chart:
            continue
        cats = chart.get("categories") or []
        series = chart.get("series") or []
        ctype = (chart.get("chartType") or "").lower()
        if not ctype.startswith("column"):
            notes.append("%s：图表类型 %s 不是垂直柱状，未切" % (e["id"], ctype or "?"))
            continue
        if not cats or not series:
            notes.append("%s：没有类别或系列，未切" % e["id"])
            continue
        x, y, w, h = e["box"]
        # 元素坐标 → 页面图像素
        rx0 = max(0, int(round(x * px_per_unit_x)))
        ry0 = max(0, int(round(y * px_per_unit_y)))
        rx1 = min(img.shape[1], int(round((x + w) * px_per_unit_x)))
        ry1 = min(img.shape[0], int(round((y + h) * px_per_unit_y)))
        if rx1 - rx0 < 8 or ry1 - ry0 < 8:
            notes.append("%s：包围盒在页面图上不足 8px，未切" % e["id"])
            continue
        patch = img[ry0:ry1, rx0:rx1]
        boxes, reason = detect(patch, len(cats), len(series))
        if not boxes:
            notes.append("%s：%s" % (e["id"], reason))
            continue

        # 段按 x 从左到右 = 类别顺序；同一类别内按系列顺序相邻（簇状图的排法）。
        bars = []
        for k, (bx, by, bw, bh) in enumerate(boxes):
            ci, si = divmod(k, len(series))
            vals = series[si].get("values") or []
            if ci >= len(vals) or vals[ci] is None:
                continue
            # 高度顺序必须与数值顺序一致：反了说明 x 映射方向错了，
            # 这时指过去会指到隔壁那根，比不指更糟。
            bars.append({
                "id": "%s-b%d" % (e["id"], k),
                "category": cats[ci] if ci < len(cats) else str(ci),
                "series": series[si].get("name") or "",
                "value": float(vals[ci]),
                "box": [int(round((rx0 + bx) / px_per_unit_x)),
                        int(round((ry0 + by) / px_per_unit_y)),
                        int(round(bw / px_per_unit_x)), int(round(bh / px_per_unit_y))],
                "heightPx": int(round(bh / px_per_unit_y)),
            })
        if len(bars) < 2:
            notes.append("%s：切出的柱子不足 2 根，未采用" % e["id"])
            continue
        # 数值序与高度序必须同向。相等值不参与比较（排序不稳定会误报）。
        pairs = [(b["value"], b["heightPx"]) for b in bars]
        pairs = [p for p in pairs if p[0] > 0]
        for i in range(len(pairs)):
            for j in range(i + 1, len(pairs)):
                (v1, h1), (v2, h2) = pairs[i], pairs[j]
                if abs(v1 - v2) < 1e-6:
                    continue
                if (v1 - v2) * (h1 - h2) < 0:
                    notes.append("%s：柱高序与数值序相反（%g→%dpx / %g→%dpx），未采用"
                                 % (e["id"], v1, h1, v2, h2))
                    bars = []
                    break
            if not bars:
                break
        if not bars:
            continue
        chart["bars"] = bars
        notes.append("%s：切成 %d 根（%s）" % (e["id"], len(bars),
                                              "/".join(b["category"] for b in bars)))
    return notes


def self_test():
    """造一张和真实图表同样的页面图，验检测 + 三条降级路径。

    CI 跑不了（没有 numpy/PIL 之外的东西也没有成片），但它保证这个脚本错了
    不会「安静地切错」。
    """
    from PIL import ImageDraw

    W, H = 1280, 720
    img = Image.new("RGB", (W, H), (255, 255, 255))
    d = ImageDraw.Draw(img)
    base_y, plot_x0 = 531, 236
    for gy in range(286, base_y, 24):               # 网格线
        d.line([plot_x0, gy, 1100, gy], fill=(135, 135, 135), width=1)
    want = [(174, 246), (131, 185), (96, 135)]
    segs = [(256, 371), (545, 660), (834, 949)]
    for (v, bh), (a, b) in zip(want, segs):
        d.rectangle([a, base_y - bh, b, base_y], fill=(79, 129, 189))
        d.text((a + 30, base_y - bh - 18), str(v), fill=(0, 0, 0))
    arr = np.asarray(img)

    page = [{"id": "p1-s2", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
             "chart": {"chartType": "column_clustered", "dataLabels": True,
                       "categories": ["2 核", "4 核", "8 核"],
                       "series": [{"name": "耗时", "values": [174, 131, 96]}]}}]
    split_page(page, arr, W / float(PAGE_W), H / float(PAGE_H))
    bars = page[0]["chart"].get("bars") or []
    heights = [b["heightPx"] for b in bars]
    cases = [
        ("切出 3 根", len(bars) == 3),
        ("类别对齐", [b["category"] for b in bars] == ["2 核", "4 核", "8 核"]),
        ("数值对齐", [b["value"] for b in bars] == [174.0, 131.0, 96.0]),
        # 页面图 1280 宽 → 元素系 1920：246px 应折成 369px（±2）
        ("坐标换算到 1920 系", bool(bars) and abs(bars[0]["heightPx"] - 369) <= 2
         and abs(heights[0] - heights[-1] - 166) <= 3),
        ("id 可被 focus 引用", bool(bars) and bars[0]["id"] == "p1-s2-b0"),
    ]

    # 降级一：段数对不上（图例色块多一块）→ 未采用，且给出理由
    bad = Image.new("RGB", (W, H), (255, 255, 255))
    db = ImageDraw.Draw(bad)
    for (a, b) in segs:
        db.rectangle([a, base_y - 100, b, base_y], fill=(79, 129, 189))
    db.rectangle([1150, 200, 1200, 230], fill=(79, 129, 189))   # 图例
    page2 = [{"id": "p1-s3", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "categories": ["a", "b", "c"],
                        "series": [{"name": "s", "values": [3, 2, 1]}]}}]
    notes2 = split_page(page2, np.asarray(bad), W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("段数不符则不切", "bars" not in page2[0]["chart"] and "色块" in notes2[0]))

    # 降级二：柱高顺序与数值相反 → 未采用（宁可未测，不可指错）
    page3 = [{"id": "p1-s4", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "categories": ["a", "b", "c"],
                        "series": [{"name": "s", "values": [1, 2, 3]}]}}]
    notes3 = split_page(page3, arr, W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("柱高与数值矛盾则不切",
                  "bars" not in page3[0]["chart"] and "相反" in notes3[0]))

    # 降级三：水平条形图 → 明确不做，不是「没找到柱子」
    page4 = [{"id": "p1-s5", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "bar_clustered", "categories": ["a", "b"],
                        "series": [{"name": "s", "values": [1, 2]}]}}]
    notes4 = split_page(page4, arr, W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("水平条形明确未切", "bars" not in page4[0]["chart"] and "垂直" in notes4[0]))

    bad_names = [n for n, ok in cases if not ok]
    for n, ok in cases:
        print(("  ok   " if ok else "  FAIL ") + n)
    for line in notes2 + notes3 + notes4:
        print("   · " + line)
    print("\n%d/%d 通过" % (len(cases) - len(bad_names), len(cases)))
    return 0 if not bad_names else 1


def main():
    if "--self-test" in sys.argv:
        return self_test()
    ap = argparse.ArgumentParser()
    ap.add_argument("--elements", default="tmp/chart/elements.json")
    ap.add_argument("--png-dir", default="tmp/chart")
    ap.add_argument("--pattern", default="page-{i}.png")
    ap.add_argument("--out", default="")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    pages = json.load(open(args.elements, encoding="utf-8"))
    total, done = 0, 0
    for page in pages:
        i = page.get("index", 0) + 1
        png = os.path.join(args.png_dir, args.pattern.format(i=i))
        if not os.path.exists(png):
            if any(e.get("chart") for e in page.get("elements") or []):
                print("== page %d：缺页面图 %s，本页未切" % (page.get("index"), png))
            continue
        img = np.asarray(Image.open(png).convert("RGB"))
        sx = img.shape[1] / float(PAGE_W)
        sy = img.shape[0] / float(PAGE_H)
        notes = split_page(page.get("elements") or [], img, sx, sy)
        for e in page.get("elements") or []:
            if e.get("chart"):
                total += 1
                if e["chart"].get("bars"):
                    done += 1
        if notes and not args.quiet:
            print("== page %d" % page.get("index"))
            for n in notes:
                print("   " + n)

    out = args.out or args.elements
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    json.dump(pages, open(out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print("\n%d/%d 个图表切出逐柱：→ %s" % (done, total, out))
    return 0 if done or total == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
