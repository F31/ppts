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

1. 在 chart 元素的包围盒内找「柱色」：高饱和、成规模的色簇。白底、灰网格线、
   黑标签全是低饱和，天然被排除。**每个系列一个色**：有几个系列就取几个互不
   相近的大色簇（簇状图的系列配色由主题给定，同系列同色）。
2. 每个色簇的掩码做投影切段 —— 垂直柱按列、水平条按行，取「这一列/行有 ≥3% 是
   柱色」的连续段 = 一根柱。
3. 每个色簇的段数必须等于类别数；所有柱**厚/宽一致**，垂直柱底边同高、水平条
   左端同起点——否则说明量到的不是柱子（图例色块、背景块、色带），直接报未测。
4. 按主轴的先后把柱排开，每 `系列数` 个一组：一组就是一个类别，组内的颜色次序
   必须**每组都一样**。这同时校验了「系列是按声明顺序画的」这条版面前提，也让
   我们不必猜哪个颜色对应哪个系列。
5. 长度顺序必须与数值顺序一致。这是唯一一条「几何对数据」的自校验：如果主轴
   映射反了，长度排序会整体倒过来，宁可报未测也不能把 focus 指到邻居身上。

## 边界

- 做**垂直柱状**（`column*`）与**水平条形**（`bar*`）。折线、饼图、面积图一律
  报未测：它们在画面上不是「一根柱占一个带」，切出来的段没有语义。
- 水平条形只做单系列。多系列时组内次序随坐标系翻转（Excel 会把整个绘图区转
  180°，类别轴也因此自下而上），没有样本可验就不猜——报未测。
- 水平条形的类别是**自下而上**的（类别 0 在最底下），不是自左向右；这是
  Excel/PowerPoint 的默认，也是「指错比不指更糟」最容易被踩到的地方。
- 数值为负时柱会翻到基线另一侧，长度不再反映大小，报未测。
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


def color_clusters(patch, n_series):
    """面积最大的至多 `n_series` 个互不相近的饱和色簇。返回 list 或 None。

    一个系列一个色：单系列时它就是原来那个「柱色」；多系列时按面积取前几个。
    取完还要看**有没有剩下的大色簇**——剩下说明这张图不止这么多种颜色（渐变、
    多主题强调色、或者是别的东西被当成了柱子），填不满或填过头都不猜。
    """
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

    chosen, rest = [], 0.0
    for k in order:
        # 太小的簇是抗锯齿残留、图例描边，不足以撑起一根柱子。
        if counts[k] < 0.05 * len(cand):
            continue
        c = vals[k].astype(np.int16)
        if all(int(np.sqrt(((c - ch) ** 2).sum())) >= COLOR_TOL for ch in chosen):
            if len(chosen) < n_series:
                chosen.append(c)
                continue
            rest += counts[k]
    if len(chosen) < n_series:
        return None
    if rest > 0.15 * len(cand):
        # 选够之后仍有成规模的另一种颜色：图比「类别×系列」复杂（或量错了）。
        return None
    return chosen


def segment_axis(mask, horizontal):
    """沿主轴投影 → 连续段。每段是 (a, b)，含端点。

    垂直柱按列投影（段是 x 区间），水平条按行投影（段是 y 区间）。
    """
    proj = mask.sum(axis=1 if horizontal else 0)
    span = mask.shape[1] if horizontal else mask.shape[0]
    thick = proj >= max(2, int(COL_MIN_RATIO * span))
    segs, start = [], None
    for i, on in enumerate(thick):
        if on and start is None:
            start = i
        elif not on and start is not None:
            segs.append((start, i - 1))
            start = None
    if start is not None:
        segs.append((start, len(thick) - 1))
    return segs


def detect(patch, n_cats, n_series, horizontal=False):
    """页面图里 chart 区域 → 每根柱的像素矩形。返回 (boxes, reason)。

    boxes 按「类别主序、系列次序」排好，元素是 `(x, y, w, h, cat, ser)`。
    bars 为空时 reason 说明为什么没切成——这句话会原样进报告，所以它必须能
    让人判断「是这页本来没柱，还是我的工具有洞」。
    """
    if horizontal and n_series > 1:
        return [], "水平条形只做单系列（多系列的组内次序随坐标翻转，没有样本可验）"
    colors = color_clusters(patch, n_series)
    if colors is None:
        return [], ("图表区域里找不到 %d 个成规模且互不相同的色簇"
                    "（多系列配色、渐变，或非柱状图）" % n_series)
    # float32 而不是 int16：差值最大 255，平方 65025 会溢出 int16（32767），
    # 溢出成负数再开方就是 NaN——比较全假、mask 全空，报出来的却是「没找到柱子」。
    # check_chart.py 的 bar_height 栽过同一个坑，这里不是抄错而是重犯。
    px = patch.astype(np.float32)
    masks = [np.sqrt(((px - c) ** 2).sum(axis=2)) < COLOR_TOL for c in colors]

    # 每个色簇各自切段：段数都必须等于类别数。
    per_color = []
    for si, m in enumerate(masks):
        segs = [s for s in segment_axis(m, horizontal) if s[1] - s[0] + 1 >= 4]
        # 图例里的色块与柱子同色，会在同一色簇里多切出一段。它比柱子薄得多
        # （12px 方块 vs 130px 柱宽），按厚度滤掉——柱宽在同一张图里是一致的，
        # 这条不靠猜。滤完仍对不上才报未测。
        if segs:
            top = max(b - a + 1 for a, b in segs)
            segs = [s for s in segs if (s[1] - s[0] + 1) >= 0.55 * top]
        if len(segs) != n_cats:
            return [], ("第 %d 个色簇量到 %d 段，应为类别数 %d"
                        % (si, len(segs), n_cats))
        per_color.append(segs)

    bars = []   # (主轴起点, 色簇号, x, y, w, h)
    for si, segs in enumerate(per_color):
        for (a, b) in segs:
            if horizontal:
                sub = masks[si][a:b + 1, :]
                xs = np.where(sub.any(axis=0))[0]
                if len(xs) == 0:
                    return [], "色段 %d–%d 里没有柱色像素" % (a, b)
                bars.append((a, si, int(xs.min()), a, int(xs.max() - xs.min() + 1),
                             b - a + 1))
            else:
                sub = masks[si][:, a:b + 1]
                ys = np.where(sub.any(axis=1))[0]
                if len(ys) == 0:
                    return [], "色段 %d–%d 里没有柱色像素" % (a, b)
                bars.append((a, si, a, int(ys.min()), b - a + 1,
                             int(ys.max() - ys.min() + 1)))
    bars.sort(key=lambda t: t[0])
    if len(bars) != n_cats * n_series:
        return [], ("量到 %d 根柱，类别×系列应为 %d 根（%d×%d）"
                    % (len(bars), n_cats * n_series, n_cats, n_series))

    # 组内颜色次序必须每组一致：这是「系列按声明顺序画」的唯一可验形式。
    # 不一致说明量到的不是「一类别一组」，硬编下去会把系列张冠李戴。
    order = None
    for g in range(n_cats):
        chunk = bars[g * n_series:(g + 1) * n_series]
        got = [c[1] for c in chunk]
        if len(set(got)) != n_series:
            return [], "第 %d 组里有重复配色，不像「一类别一组」" % g
        if order is None:
            order = got
        elif got != order:
            return [], "各组内配色次序不一致，系列归属无法确定"

    # 柱的「厚」是垂直于主轴的那一维：垂直柱看宽，水平条看高。
    thick = [b[5] if horizontal else b[4] for b in bars]
    if max(thick) / float(min(thick)) > WIDTH_SPREAD_MAX:
        return [], ("柱厚/宽差 %.2f 倍（%d–%dpx），不像同一张图里的柱子"
                    % (max(thick) / float(min(thick)), min(thick), max(thick)))
    if horizontal:
        # 水平条从左端同一条基线出发。
        left = [b[2] for b in bars]
        if max(left) - min(left) > BASE_TOL_PX:
            return [], ("柱起点差 %dpx，不在同一条基线上，可能量到了图例或背景"
                        % (max(left) - min(left)))
    else:
        bases = [b[3] + b[5] for b in bars]
        if max(bases) - min(bases) > BASE_TOL_PX:
            return [], ("色块底边差 %dpx，不在同一条基线上，可能量到了图例或背景"
                        % (max(bases) - min(bases)))

    out = []
    for g in range(n_cats):
        # 水平条形的类别轴自下而上：组按 y 升序排开，但第 0 组在最底下。
        cat = (n_cats - 1 - g) if horizontal else g
        chunk = bars[g * n_series:(g + 1) * n_series]
        for pos, (_, si, x, y, w, h) in enumerate(chunk):
            ser = order.index(si) if order else pos
            out.append((x, y, w, h, cat, ser))
    # 产物一律按「类别主序、系列次序」排：两种朝向的 id 编号才对得上，
    # 下游拿 b0 当「第一个类别」时不用先问这张图是横的还是竖的。
    out.sort(key=lambda t: (t[4], t[5]))
    return out, ""


def measure_label(patch, bx, by, bw, bh, horizontal):
    """数据标签离柱体有多远（像素）。量不到返回 0。

    标签画在柱体包围盒**之外**：垂直柱在柱顶上方，水平条在条的右端。焦点矩形
    只框柱体时，这根自己的数字会被压在纱下面——「指谁谁灰」，图上最该清楚的数
    反而看不见。所以把标签带一起框进来。

    量而不是用常数：垂直柱实测 42px（旧实现的 45 是量完又加了 3px 余量），
    水平条的标签宽度则随数字位数变（"174" 71px、"96" 48px），一个常数盖不住。
    阈值 110 只认黑字：网格线是 135 的灰，基线更浅，都不会被算进来。
    """
    g = patch.max(axis=2)
    margin = 3
    if horizontal:
        x0 = bx + bw
        band = g[by:by + bh, x0:x0 + 140]
        if band.size == 0:
            return 0
        dark = np.where((band < 110).any(axis=0))[0]
        return int(dark.max()) + 1 + margin if len(dark) else 0
    y0 = by - 70
    top = max(0, y0)
    band = g[top:by, bx:bx + bw]
    if band.size == 0 or by <= top:
        return 0
    dark = np.where((band < 110).any(axis=1))[0]
    return int(by - (top + dark.min())) + margin if len(dark) else 0


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
        if ctype.startswith("column"):
            horizontal = False
        elif ctype.startswith("bar"):
            horizontal = True
        else:
            notes.append("%s：图表类型 %s 不是柱状/条形，未切" % (e["id"], ctype or "?"))
            continue
        if not cats or not series:
            notes.append("%s：没有类别或系列，未切" % e["id"])
            continue
        # 负值会让柱翻到基线另一侧，长度不再反映大小——长度序校验会失效。
        if any((v is not None and v < 0) for s in series for v in (s.get("values") or [])):
            notes.append("%s：含负值，柱会翻到基线另一侧，未切" % e["id"])
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
        boxes, reason = detect(patch, len(cats), len(series), horizontal)
        if not boxes:
            notes.append("%s：%s" % (e["id"], reason))
            continue

        # boxes 已经按「类别主序、系列次序」排好（水平条形的类别已翻回自下而上）。
        bars = []
        for k, (bx, by, bw, bh, ci, si) in enumerate(boxes):
            vals = series[si].get("values") or []
            if ci >= len(vals) or vals[ci] is None:
                continue
            bar = {
                "id": "%s-b%d" % (e["id"], k),
                "category": cats[ci] if ci < len(cats) else str(ci),
                "series": series[si].get("name") or "",
                "value": float(vals[ci]),
                # 校验用，写进产物前会删掉：系列名可以重名，按名字找会串。
                "_si": si,
                "box": [int(round((rx0 + bx) / px_per_unit_x)),
                        int(round((ry0 + by) / px_per_unit_y)),
                        int(round(bw / px_per_unit_x)), int(round(bh / px_per_unit_y))],
                # 垂直柱的「长度」是高，水平条的「长度」是宽；两个都留着，
                # 下游要的是「跟数值同向的那个数」，不该由它去猜图的方向。
                "heightPx": int(round(bh / px_per_unit_y)),
                "widthPx": int(round(bw / px_per_unit_x)),
                "lengthPx": int(round((bw if horizontal else bh)
                                      / (px_per_unit_x if horizontal else px_per_unit_y))),
            }
            if chart.get("dataLabels"):
                pad = measure_label(patch, bx, by, bw, bh, horizontal)
                if pad:
                    bar["labelPad"] = {
                        "dir": "right" if horizontal else "up",
                        "px": int(round(pad / (px_per_unit_x if horizontal
                                               else px_per_unit_y))),
                    }
            bars.append(bar)
        if len(bars) < 2:
            notes.append("%s：切出的柱子不足 2 根，未采用" % e["id"])
            continue
        # 长度序必须与数值序同向（同一系列内比）。相等值不参与比较（排序不稳定会误报）。
        length_key = "lengthPx"
        for si in range(len(series)):
            pairs = [(b["value"], b[length_key]) for b in bars if b["_si"] == si]
            pairs = [p for p in pairs if p[0] > 0]
            bad = None
            for i in range(len(pairs)):
                for j in range(i + 1, len(pairs)):
                    (v1, h1), (v2, h2) = pairs[i], pairs[j]
                    if abs(v1 - v2) < 1e-6:
                        continue
                    if (v1 - v2) * (h1 - h2) < 0:
                        bad = (v1, h1, v2, h2)
                        break
                if bad:
                    break
            if bad:
                notes.append("%s：%s 的长度序与数值序相反（%g→%dpx / %g→%dpx），未采用"
                             % (e["id"], series[si].get("name") or "系列 %d" % si, *bad))
                bars = []
                break
        if not bars:
            continue
        for b in bars:
            b.pop("_si", None)
        chart["orientation"] = "horizontal" if horizontal else "vertical"
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
        ("垂直柱量到标签带（%s）" % (bars[0].get("labelPad") if bars else None,),
         bool(bars) and bars[0].get("labelPad", {}).get("dir") == "up"
         and bars[0]["labelPad"]["px"] >= 10),
    ]

    # 降级一：图例里同色的方块会被多切一段。它比柱子薄得多，按厚度滤掉之后
    # 仍然切出 3 根——不因为「数多了一段」就整页放弃，那会把带图例的图全判死。
    bad = Image.new("RGB", (W, H), (255, 255, 255))
    db = ImageDraw.Draw(bad)
    for (a, b) in segs:
        db.rectangle([a, base_y - 100, b, base_y], fill=(79, 129, 189))
    db.rectangle([1150, 200, 1200, 230], fill=(79, 129, 189))   # 图例
    page2 = [{"id": "p1-s3", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "categories": ["a", "b", "c"],
                        "series": [{"name": "s", "values": [3, 2, 1]}]}}]
    notes2 = split_page(page2, np.asarray(bad), W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("图例色块按厚度滤掉",
                  len(page2[0]["chart"].get("bars") or []) == 3))

    # 降级一b：柱宽不一致 → 说明量到的不是同一张图里的柱子
    uneven = Image.new("RGB", (W, H), (255, 255, 255))
    du = ImageDraw.Draw(uneven)
    for a, b in ((256, 371), (545, 660), (834, 994)):    # 最后一根 160 宽
        du.rectangle([a, base_y - 100, b, base_y], fill=(79, 129, 189))
    page2b = [{"id": "p1-s10", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
               "chart": {"chartType": "column_clustered", "categories": ["a", "b", "c"],
                         "series": [{"name": "s", "values": [3, 2, 1]}]}}]
    notes2b = split_page(page2b, np.asarray(uneven), W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("柱宽不一致则不切",
                  "bars" not in page2b[0]["chart"] and "不像同一张图" in notes2b[0]))

    # 降级二：柱高顺序与数值相反 → 未采用（宁可未测，不可指错）
    page3 = [{"id": "p1-s4", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "categories": ["a", "b", "c"],
                        "series": [{"name": "s", "values": [1, 2, 3]}]}}]
    notes3 = split_page(page3, arr, W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("柱高与数值矛盾则不切",
                  "bars" not in page3[0]["chart"] and "相反" in notes3[0]))

    # 降级三：饼图/折线之类 → 说清楚「不是柱状/条形」，不是「没找到柱子」
    page4 = [{"id": "p1-s5", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "pie", "categories": ["a", "b"],
                        "series": [{"name": "s", "values": [1, 2]}]}}]
    notes4 = split_page(page4, arr, W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("非柱状图明确未切",
                  "bars" not in page4[0]["chart"] and "不是柱状" in notes4[0]))

    # 降级四：含负值 → 柱会翻到基线另一侧，长度不再反映大小
    page5 = [{"id": "p1-s6", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "categories": ["a", "b"],
                        "series": [{"name": "s", "values": [3, -2]}]}}]
    notes5 = split_page(page5, arr, W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("含负值明确未切",
                  "bars" not in page5[0]["chart"] and "负值" in notes5[0]))

    # ---- 水平条形：类别自下而上，长度沿 x ----
    # 真实导出（LibreOffice）里 174 在最下面，96 在最上面——这是默认的类别轴方向。
    hb = Image.new("RGB", (W, H), (255, 255, 255))
    dh = ImageDraw.Draw(hb)
    hx0, bars_y = 236, [(300, 341), (420, 461), (540, 581)]   # 自上而下：96 / 131 / 174
    for gy in range(200, 620, 40):
        dh.line([hx0, gy, 1100, gy], fill=(135, 135, 135), width=1)
    for (v, length), (y0, y1) in zip([(96, 135), (131, 185), (174, 246)], bars_y):
        dh.rectangle([hx0, y0, hx0 + length, y1], fill=(79, 129, 189))
        dh.text((hx0 + length + 8, y0 + 6), str(v), fill=(0, 0, 0))
    pageH = [{"id": "p1-s7", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "bar_clustered", "dataLabels": True,
                        "categories": ["华东", "华北", "华南"],
                        "series": [{"name": "耗时", "values": [174, 131, 96]}]}}]
    notesH = split_page(pageH, np.asarray(hb), W / float(PAGE_W), H / float(PAGE_H))
    hbars = pageH[0]["chart"].get("bars") or []
    cases.append(("水平条切出 3 根", len(hbars) == 3))
    cases.append(("水平条类别自下而上 → %s"
                  % ([b["category"] for b in hbars],),
                  [b["category"] for b in hbars] == ["华东", "华北", "华南"]))
    cases.append(("水平条长度与数值同向 → %s"
                  % ([b["lengthPx"] for b in hbars],),
                  bool(hbars) and [b["lengthPx"] for b in hbars] == sorted(
                      [b["lengthPx"] for b in hbars], reverse=True)
                  and abs(hbars[0]["lengthPx"] - 369) <= 2))
    cases.append(("水平条也写 heightPx（兼容旧字段）→ %s"
                  % ([b["heightPx"] for b in hbars],),
                  bool(hbars) and abs(hbars[0]["heightPx"] - 62) <= 2))
    # 水平条的标签在条的右端，宽度随位数变（174 比 96 宽）——这正是「用常数盖不住」的证据。
    cases.append(("水平条标签带向右 → %s"
                  % ([(b["value"], (b.get("labelPad") or {}).get("px")) for b in hbars],),
                  bool(hbars) and all((b.get("labelPad") or {}).get("dir") == "right"
                                      for b in hbars)
                  and hbars[0]["labelPad"]["px"] > hbars[2]["labelPad"]["px"]))

    # ---- 多系列：每系列一色，组内次序每组一致；长度序按**系列内**校验 ----
    ms = Image.new("RGB", (W, H), (255, 255, 255))
    dm = ImageDraw.Draw(ms)
    blue, red = (79, 129, 189), (192, 80, 72)
    groups = [(256, 386), (566, 696), (876, 1006)]             # 每个类别两根，各 130 宽
    # 系列 A 的数值随类别递减，系列 B 的数值**不是**同一个序（96/174/131）——
    # 全局排序校验会误报，只有「按系列分开校验」才过得去。这一条就是为它写的。
    valsA, valsB = [174, 131, 96], [96, 174, 131]
    for (a, b), va, vb in zip(groups, valsA, valsB):
        dm.rectangle([a, base_y - int(va * 246 / 174), b - 66, base_y], fill=blue)
        dm.rectangle([a + 64, base_y - int(vb * 246 / 174), b, base_y], fill=red)
    pageM = [{"id": "p1-s8", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
              "chart": {"chartType": "column_clustered", "dataLabels": True,
                        "categories": ["2 核", "4 核", "8 核"],
                        "series": [{"name": "2025", "values": valsA},
                                   {"name": "2026", "values": valsB}]}}]
    notesM = split_page(pageM, np.asarray(ms), W / float(PAGE_W), H / float(PAGE_H))
    mbars = pageM[0]["chart"].get("bars") or []
    cases.append(("多系列切出 6 根", len(mbars) == 6))
    cases.append(("多系列按类别成组 → %s"
                  % ([b["category"] for b in mbars],),
                  [b["category"] for b in mbars] == ["2 核", "2 核", "4 核", "4 核",
                                                     "8 核", "8 核"]))
    cases.append(("多系列按声明顺序 → %s" % ([b["series"] for b in mbars],),
                  [b["series"] for b in mbars] == ["2025", "2026"] * 3))
    cases.append(("多系列数值与几何一致 → %s"
                  % ([(b["value"], b["lengthPx"]) for b in mbars],),
                  all(abs(b["lengthPx"] - int(b["value"] * 246 / 174) * 1.5) <= 3
                      for b in mbars)))
    cases.append(("多系列 id 仍按类别主序 → %s"
                  % ([b["id"] for b in mbars][:3],),
                  [b["id"] for b in mbars][:3] == ["p1-s8-b0", "p1-s8-b1", "p1-s8-b2"]))

    # 降级五：水平 + 多系列 → 组内次序随坐标翻转，没有样本就不猜
    pageHM = [{"id": "p1-s9", "kind": "chart", "box": [0, 0, PAGE_W, PAGE_H],
               "chart": {"chartType": "bar_clustered", "categories": ["a", "b"],
                         "series": [{"name": "x", "values": [1, 2]},
                                    {"name": "y", "values": [2, 1]}]}}]
    notesHM = split_page(pageHM, np.asarray(hb), W / float(PAGE_W), H / float(PAGE_H))
    cases.append(("水平多系列明确未切",
                  "bars" not in pageHM[0]["chart"] and "单系列" in notesHM[0]))

    bad_names = [n for n, ok in cases if not ok]
    for n, ok in cases:
        print(("  ok   " if ok else "  FAIL ") + n)
    for line in notes2 + notes3 + notes4 + notes5 + notesH + notesM + notesHM:
        if line.strip():
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
