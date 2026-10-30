#!/usr/bin/env python3
"""版面体检：查「观感为什么不对」，不是「元素是否准时」。

## 为什么另写一个

前面几个脚本量的都是**元素的时间**（开口前有没有提前、延迟几帧、错落对不对）。
5.5 和 5.6 两轮人工评分都收不了口，查下来片子在这几项上全过 —— 但人看着仍然
不对。差异不在时间，在**每一刻画面上有多少内容**：

- 卡片底板 5.9s 就淡入，四条要点依次到 16.6s 才填满 → 中间 10 秒是「一个空盒子」；
- 架构页右半边的图片框 24s 时只有一个空边框，内容 27.6s 才进 → 空框挂了 3 秒半；
- 谢幕页的「生僻字样张」30.27s 才出现，30.78s 就切走 → 507ms，看不清，等于没有。

这三件事,**元素级的时间测量一件事都测不到**：元素全都准时、全都只出现一次、
谁也没提前。所以它们必须用「画面」而不是「元素」来量。

## 三项检查

- `blankWindow`：本页终态内容的到位率随时间怎么走。到位率 = 当前帧与终态帧在
  「终态有内容的像素」上一致的比例。观众看到的「本页最终内容」的百分之多少。
  报最低值、最低时刻、以及低于阈值的总时长 —— 这就是「空盒子挂了几秒」。
- `flashTooLate`：元素出现到本页结束不足 `FLASH_MS`。看不清 = 没出现，
  但它能让「未提及元素不在页首」这条判据变绿（V5.4 的漏洞：挡了剧透，没挡白闪）。
- `containerAhead`：容器的「静默区」（框内没有任何子元素的部分）比子元素早到位
  超过 `AHEAD_MS`。这是「空框先出、内容后填」的可计算形式。

## 测不到的部分（不要以为跑绿了就没事）

- **元素内部**的分层：架构页的图片框是先画边框再填内容，元素级数据里
  `p3-hero` 只有一个 box，框和内容是一件事。只有 `blankWindow` 能间接看见。
- **元素之间**的关系：柱状图没有 y 轴基线、注释压在卡片底边上 —— 图表是
  一个整体 `chart` 元素，里面几根柱子、有没有刻度，数据里都没有。
- 审美。缓动曲线顺不顺、位移距离合不合适，这三项都判不了。

用法：
  python3 scripts/fidelity/audit_layout.py [film] [segments.json] [elements.json]
输出：tmp/q7/layout-audit.json（退出码 1 = 有可疑项）
"""
import json
import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from check_appearance import ffmpeg_bin, page_frames, probe_ms, W, H   # noqa: E402

# 内容覆盖低于这个比例就算「画面上本页的东西不到一半」。
# 50% 是刻意选的：一页里一半内容还没出来时，观众会认为「这页还没加载完」，
# 而 70% 会把「最后一件还在淡入」的正常尾巴算成问题。
COVER_MIN = 0.50
# 低覆盖持续多久算问题。1.5s 是肉眼察觉「这里是不是没加载出来」的量级，
# 也够观众看两眼。
HOLD_MS = 1500
# 判定「看得清」的下限。500ms 在 30fps 下是 15 帧，读一行 20 字的中文不够。
FLASH_MS = 1000
# 静默区比内容早到位多少算「空框悬挂」。卡片自身的淡入是 220ms 量级，
# 留一倍余量。
AHEAD_MS = 600
# 终态内容覆盖率的**下限**只用来判断「这页值不值得查」，不是「这页够不够满」。
# 正常幻灯片内容占页面 15–50%（大片留白是版式，不是缺陷），定 70% 会把所有页
# 都判成「内容少」而全部跳过——第一版就定错了。10% 以下才是真没东西。
MIN_FINAL_COVERAGE = 0.10


def coverage_series(film, base, dur, grid=3):
    """按 3×3 区块算到位率，返回 (series, final)。

    `series` 是 [(t_ms, [每格到位率…])]，`final` 是 [每格终态内容占比…]。

    为什么按格而不是整页：整页像素率会被大片纯底色压住。页 1 在 10.15s
    只有 2 条要点，卡片里空着 2/3，但卡片底色已经占了页面很大面积，
    整页到位率仍是 93% —— 指标对「少了几行字」完全不敏感。分区看才有分辨率。

    背景色取**四角**而不是直方图众数：卡片底、浅色色块与白底的差只有 5–10 级，
    众数法会把它们和背景算作一类，而观众看到的「一个空盒子」恰恰就是那块底色。
    """
    seq = [f for _, f in page_frames(film, base, dur)]
    if len(seq) < 2:
        return None
    ref = seq[-1]
    k = 40
    corners = np.concatenate([ref[:k, :k].ravel(), ref[:k, -k:].ravel(),
                              ref[-k:, :k].ravel(), ref[-k:, -k:].ravel()])
    bg = float(np.median(corners))
    mask = np.abs(ref.astype(np.int16) - bg) > 4
    if int(mask.sum()) == 0:
        return None

    hs = [int(H * i / grid) for i in range(grid + 1)]
    ws = [int(W * i / grid) for i in range(grid + 1)]
    cells = [(hs[r], hs[r + 1], ws[c], ws[c + 1])
             for r in range(grid) for c in range(grid)]
    tot = [int(mask[a:b, c2:d].sum()) for a, b, c2, d in cells]
    share = [t / float(W * H) for t in tot]

    series = []
    for i, f in enumerate(seq):
        same = (np.abs(f.astype(np.int16) - ref.astype(np.int16)) <= 12) & mask
        cov = [float(same[a:b, c2:d].sum()) / t if t else 1.0
               for (a, b, c2, d), t in zip(cells, tot)]
        series.append((base + i * (1000 // 30), cov))
    return series, share


def silent_region(box, children):
    """容器框内、没有被任何子元素盖住的部分，拆成若干个矩形。

    返回 [(y0, y1, x0, x1)]。分割方式：先按子元素的纵向区间切带，
    每一条带里再把子元素的横向区间挖掉。不做通用矩形分解——
    容器里的孩子都是上下排的，按带切已经够用，且不会切出碎条。
    """
    x, y, w, h = box
    x0, y0, x1, y1 = int(x) + 8, int(y) + 8, int(x + w) - 8, int(y + h) - 8
    kids = [c for c in children
            if c[0] >= x - 1 and c[1] >= y - 1
            and c[0] + c[2] <= x + w + 1 and c[1] + c[3] <= y + h + 1]
    if not kids:
        return [], (y0, y1, x0, x1)
    # 纵向切带：以孩子上下边为界
    cut = sorted({y0, y1} | {max(y0, min(y1, int(c[1]))) for c in kids}
                 | {max(y0, min(y1, int(c[1] + c[3]))) for c in kids})
    rects = []
    for a, b in zip(cut, cut[1:]):
        if b - a < 6:
            continue
        span = [(max(x0, int(c[0])), min(x1, int(c[0] + c[2])))
                for c in kids if int(c[1]) < b and int(c[1] + c[3]) > a]
        span = [(s, e) for s, e in span if e > s]
        span.sort()
        cur = x0
        for s, e in span:
            if s - cur >= 6:
                rects.append((a, b, cur, s))
            cur = max(cur, e)
        if x1 - cur >= 6:
            rects.append((a, b, cur, x1))
    return rects, None


def arrive_ms(film, base, dur, rects):
    """一组矩形区域「到位」的时刻：最后一次与终态不同之后的那一帧。"""
    seq = [f for t, f in page_frames(film, base, dur)]
    if len(seq) < 2:
        return None
    ref = seq[-1]
    diff = [float(np.mean([np.abs(f[a:b, c:d].astype(np.int16)
                            - ref[a:b, c:d].astype(np.int16)).mean()
                           for a, b, c, d in rects])) for f in seq]
    last = max((i for i, v in enumerate(diff) if v > 1.5), default=None)
    if last is None or last + 1 >= len(seq):
        return base
    return base + (last + 1) * (1000 // 30)


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_C_narrated.mp4"
    seg_path = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/segments.json"
    elems_path = sys.argv[3] if len(sys.argv) > 3 else "tmp/q7/fid/elements.json"

    script = json.load(open(seg_path, encoding="utf-8"))
    elems = json.load(open(elems_path, encoding="utf-8"))

    starts, page_start = {}, 0
    for page in script["pages"]:
        starts[page["index"]] = page_start
        sf = "tmp/q7/out/segment_%03d.mp4" % page["index"]
        page_start += probe_ms(sf) if os.path.exists(sf) else page["durationMs"]
    ends = {p["index"]: starts.get(p["index"] + 1, page_start) for p in script["pages"]}

    findings = []

    # —— 一、内容覆盖：此刻画面上有本页内容的百分之多少 ——
    #
    # 按**元素面积**算，不按像素。像素法在页 1 上是瞎的：10.15s 时四条要点只出了
    # 两条，卡片里空着三分之二，但卡片底色已经铺满，整页像素到位率 93%。分区
    # （九宫格）也不够——底色同样占满每一格。只有「这一页的内容元素到位了几个、
    # 占多大面积」才说得出「少了几件事」。
    #
    # 容器（包住别的元素的 shape）不计入分母：卡片底板到位不等于内容到位，
    # 把它算进去会让「空盒子」看起来是满的。
    print("== 内容覆盖（面积加权，低于 %d%% 且持续 ≥ %.1fs） ==" % (
        int(COVER_MIN * 100), HOLD_MS / 1000.0))
    app = ({r["element"]: r for r in json.load(open("tmp/q7/appearance.json", encoding="utf-8"))}
           if os.path.exists("tmp/q7/appearance.json") else {})
    for page in script["pages"]:
        idx = page["index"]
        if idx in (0, len(script["pages"]) - 1) or not app:
            continue
        els = elems[idx]["elements"]
        # 容器 = 完整包住至少一个别的元素的元素
        boxes = {e["id"]: e["box"] for e in els}
        is_box = set()
        for eid, (x, y, w, h) in boxes.items():
            for oid, (ox, oy, ow, oh) in boxes.items():
                if oid != eid and ox >= x - 1 and oy >= y - 1 \
                        and ox + ow <= x + w + 1 and oy + oh <= y + h + 1:
                    is_box.add(eid)
                    break
        info = [e for e in els if e["id"] not in is_box
                and app.get(e["id"], {}).get("settledMs")]
        if len(info) < 2:
            continue
        area = {e["id"]: float(e["box"][2] * e["box"][3]) for e in info}
        total = sum(area.values())
        if total <= 0:
            continue
        # 覆盖曲线折点：每个元素到位的时刻。区间 [t_i, t_{i+1}) 的覆盖 = 前 i 个的面积和。
        # 页首那一段（页起点 → 第一件到位）覆盖记 0，它也是真实存在的空窗，
        # 但只有「本页已经开始出东西之后还在空」才是缺陷——所以起点从第一件算。
        times = sorted({app[e["id"]]["settledMs"] for e in info})
        spans = []                       # (t0, t1, coverage, 已到位元素集合)
        for i, t in enumerate(times):
            got = {e["id"] for e in info if app[e["id"]]["settledMs"] <= t}
            end = times[i + 1] if i + 1 < len(times) else ends[idx]
            spans.append((t, end, sum(area[k] for k in got) / total, got))
        # 合并连续低覆盖的区间，取最长的一段
        best = (0.0, None, None, set())
        run = None
        for t0, t1, c, got in spans:
            if c < COVER_MIN:
                if run is None:
                    run = [t0, t1, got]
                else:
                    run[1] = t1
                    run[2] = got
            elif run is not None:
                if run[1] - run[0] > best[0]:
                    best = (run[1] - run[0], run[0], run[1], run[2])
                run = None
        if run is not None and run[1] - run[0] > best[0]:
            best = (run[1] - run[0], run[0], run[1], run[2])

        dur, t_start, t_end, seen = best
        if dur < HOLD_MS:
            lowest = min((sp[2], sp[0]) for sp in spans)
            print("  p%-2d ok（最低 %.0f%% @ %.2fs，低覆盖最长 %.2fs）" % (
                idx, lowest[0] * 100, lowest[1] / 1000, dur / 1000))
            continue
        missing = [e["id"] for e in info if e["id"] not in seen]
        print("  p%-2d %6.2fs→%6.2fs 覆盖 < %d%% 共 %.1fs · 缺 %s  ** 空 %.1fs **" % (
            idx, t_start / 1000, t_end / 1000, int(COVER_MIN * 100), dur / 1000,
            "、".join(missing[:4]) or "-", dur / 1000))
        findings.append({
            "code": "lowCoverage", "page": idx,
            "atMs": t_start, "holdMs": dur, "missing": missing,
            "note": "%.2fs–%.2fs（%.1fs）画面上只有本页内容的 %d%% 以下，缺 %s"
                    % (t_start / 1000, t_end / 1000, dur / 1000,
                       int(COVER_MIN * 100), "、".join(missing[:4])),
        })

    # —— 二、页末闪现：出现了，但来不及看清 ——
    # 需要每元素到位时刻。这里不复用 appearance.json（它可能过期），
    # 用的是同一条片、同一份脚本，两边口径一致。
    print()
    print("== 页末闪现（出现到本页结束 < %.1fs） ==" % (FLASH_MS / 1000.0))
    appear_path = "tmp/q7/appearance.json"
    if os.path.exists(appear_path):
        app = {r["element"]: r for r in json.load(open(appear_path, encoding="utf-8"))}
        for page in script["pages"]:
            idx = page["index"]
            for e in elems[idx]["elements"]:
                r = app.get(e["id"])
                if not r or r.get("settledMs") is None:
                    continue
                left = ends[idx] - r["settledMs"]
                if left >= FLASH_MS:
                    continue
                print("  p%-2d %-12s %6.2fs 出 · 剩 %.2fs · %r" % (
                    idx, e["id"], r["settledMs"] / 1000, left / 1000,
                    (e.get("text") or "")[:26]))
                findings.append({
                    "code": "flashTooLate", "page": idx, "element": e["id"],
                    "atMs": r["settledMs"], "leftMs": left,
                    "note": "%.2fs 才出现，本页 %.2fs 就结束，只剩 %.2fs"
                            % (r["settledMs"] / 1000, ends[idx] / 1000, left / 1000),
                })
    else:
        print("  跳过：没有 tmp/q7/appearance.json（先跑 check_appearance.py）")

    # —— 三、空框悬挂：容器的静默区比内容早到位 ——
    print()
    print("== 空框悬挂（容器静默区比内容早 > %.1fs） ==" % (AHEAD_MS / 1000.0))
    app = ({r["element"]: r for r in json.load(open(appear_path, encoding="utf-8"))}
           if os.path.exists(appear_path) else {})
    for page in script["pages"]:
        idx = page["index"]
        els = elems[idx]["elements"]
        for e in els:
            if e["kind"] not in ("shape", "image", "chart", "table"):
                continue
            x, y, w, h = e["box"]
            if w * h < 0.03 * W * H:
                continue
            kids = [k["box"] for k in els if k["id"] != e["id"]]
            rects, whole = silent_region(e["box"], kids)
            seq_rects = [whole] if whole else rects
            if not seq_rects:
                continue
            a = arrive_ms(film, starts[idx], ends[idx] - starts[idx], seq_rects)
            # 只留真正被这个容器包住的子元素的到位时刻
            inside = []
            for k in els:
                if k["id"] == e["id"]:
                    continue
                kx, ky, kw, kh = k["box"]
                if kx >= x - 1 and ky >= y - 1 and kx + kw <= x + w + 1 and ky + kh <= y + h + 1:
                    t = app.get(k["id"], {}).get("settledMs")
                    if t:
                        inside.append(t)
            if not inside or a is None:
                continue
            gap = max(inside) - a
            mark = "** 空框挂了 %.1fs **" % (gap / 1000) if gap > AHEAD_MS else "ok"
            print("  p%-2d %-12s 框 %6.2fs 到位 · 最后一件内容 %6.2fs · %s" % (
                idx, e["id"], a / 1000, max(inside) / 1000, mark))
            if gap > AHEAD_MS:
                findings.append({
                    "code": "containerAhead", "page": idx, "element": e["id"],
                    "atMs": a, "gapMs": gap,
                    "note": "容器 %.2fs 就到位，最后一件内容 %.2fs 才到，空框挂了 %.1fs"
                            % (a / 1000, max(inside) / 1000, gap / 1000),
                })

    print()
    # 阻断项 = 观众会在某一刻看到「内容不到一半」或「一个空框挂着」。
    # 页末闪现只算警告：它不影响任何一刻的画面完整度，影响的是「信息没被看到」，
    # 而那取决于讲稿是否本来就打算讲它。
    blocking = [f for f in findings if f["code"] in ("lowCoverage", "containerAhead")]
    print("共 %d 项可疑，其中 %d 项阻断（覆盖不足/空框悬挂）" % (len(findings), len(blocking)))
    json.dump({"findings": findings}, open("tmp/q7/layout-audit.json", "w", encoding="utf-8"),
              ensure_ascii=False, indent=1)
    print("→ tmp/q7/layout-audit.json")
    return 1 if blocking else 0


if __name__ == "__main__":
    sys.exit(main())
