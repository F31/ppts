#!/usr/bin/env python3
"""每个元素**在整页范围内**第一次出现在什么时候。

为什么另写一个：`check_alignment.py` 只看段落开口前 400ms（`t0 = max(page_start, at-400)`）。
如果元素在更早的时候就出现了（页首一起铺、或跟着上一段出来），窗口里它是「已经在的」，
相对窗口首帧的灰度差恒为 0 → `preMax = 0`、`delay = 0`，工具报「不提前、准时」，
而人看到的是「话还没讲，东西已经摆好了」。400ms 之外的提前，这个工具结构上量不到。

这里的参照物反过来：以**该页最后一帧**为终态，逐帧量每个包围盒与终态的差，
第一次越过阈值的时刻就是它「真正出现」的时刻。与它所属段落的开口比较：
- `lead > 0`：比段开口早（提前，剧透）
- `lead < 0`：比段开口晚（滞后）
整页范围内算，不设 400ms 窗口。

用法：
  python3 scripts/fidelity/check_appearance.py [film] [segments.json] [elements.json] [阈值]
输出：tmp/q7/appearance.json

一条已知局限：**容器类元素（卡片底板、整块 shape）的读数会被子元素污染**——
参照物是终态，子元素陆续出现会改变这个大盒子内部的均值，于是读数落到「最后一个
子元素到位」的时刻（本片 `p1-card` 报 12.5s，而它自己在 5.9s 就淡入了）。判读时按
`kind` 区分：`shape` 这种大容器只看「有没有提前」，不要拿它的绝对值对段落。
"""
import json
import os
import subprocess
import sys

import numpy as np

FPS = 30
FRAME_MS = 1000 // FPS
W, H = 1920, 1080


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
    """页长取实测：渲染端按帧输出，每页比计划短几毫秒，用计划值会一路累加。"""
    out = subprocess.run(
        [ffmpeg_bin(), "-hide_banner", "-i", path],
        capture_output=True, text=True, errors="replace").stderr
    for line in out.splitlines():
        if "Duration:" in line:
            h, m, s = line.split("Duration:")[1].split(",")[0].strip().split(":")
            return round((int(h) * 3600 + int(m) * 60 + float(s)) * 1000)
    return 0


def page_frames(film, start_ms, dur_ms):
    """把一页解成 gray 帧，逐帧 yield (t_ms, ndarray HxW)。流式，不落盘。"""
    cmd = [ffmpeg_bin(), "-v", "error", "-ss", "%.3f" % (start_ms / 1000),
           "-t", "%.3f" % (dur_ms / 1000), "-i", film,
           "-vf", "fps=%d" % FPS, "-f", "rawvideo", "-pix_fmt", "gray", "-"]
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    size = W * H
    i = 0
    try:
        while True:
            buf = proc.stdout.read(size)
            if not buf or len(buf) < size:
                break
            yield start_ms + i * FRAME_MS, np.frombuffer(buf, np.uint8).reshape(H, W)
            i += 1
    finally:
        proc.stdout.close()
        proc.wait()


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_C_narrated.mp4"
    seg_path = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/segments.json"
    elems_path = sys.argv[3] if len(sys.argv) > 3 else "tmp/q7/fid/elements.json"
    threshold = float(sys.argv[4]) if len(sys.argv) > 4 else 2.0

    script = json.load(open(seg_path, encoding="utf-8"))
    elems = json.load(open(elems_path, encoding="utf-8"))

    # 页起点用实测段时长累计（与 check_alignment.py 同一口径，否则两边的 t 对不上）
    page_start, starts = 0, []
    for page in script["pages"]:
        starts.append(page_start)
        seg_file = "tmp/q7/out/segment_%03d.mp4" % page["index"]
        page_start += probe_ms(seg_file) if os.path.exists(seg_file) else page["durationMs"]

    def clip(box):
        x, y, w, h = [int(round(v)) for v in box]
        x0, y0 = max(0, x), max(0, y)
        x1, y1 = min(W, x + w), min(H, y + h)
        return y0, y1, x0, x1

    rows = []
    for page in script["pages"]:
        base = starts[page["index"]]
        dur = (starts[page["index"] + 1] - base) if page["index"] + 1 < len(starts) else None
        if dur is None:
            dur = page["durationMs"]
        page_els = elems[page["index"]]["elements"]
        boxes = {e["id"]: clip(e["box"]) for e in page_els}

        # 先攒一遍：每帧只留各盒子的小图，内存可控（一页 ~180 帧 × 15 个盒子）
        seq = []          # [(t_ms, {eid: array})]
        for t, frame in page_frames(film, base, dur):
            seq.append((t, {eid: frame[b[0]:b[1], b[2]:b[3]].copy()
                            for eid, b in boxes.items()}))
        if not seq:
            continue
        ref = seq[-1][1]                     # 终态 = 该页最后一帧
        for eid, b in boxes.items():
            r = ref[eid].astype(np.int16)
            series = [float(np.abs(v[eid].astype(np.int16) - r).mean()) for _, v in seq]
            # 取**最后一次**与终态不同的帧的下一个：那之前它还没到位，之后一直是终态。
            # 不能取第一次不同——元素在页首是「不存在」，与终态本来就不同，
            # 第一次不同会被量成「页首就出现」（第一版就写反了，全页都报页首）。
            last = None
            for i, v in enumerate(series):
                if v > threshold:
                    last = i
            if last is None:
                # 整页与终态一致：要么页首就在（常驻底板/阴影），要么根本没画。
                # 两者对「什么时候出现」的答案是同一个——页首。
                appear = seq[0][0]
                always = True
            else:
                appear = seq[last + 1][0] if last + 1 < len(seq) else None
                always = False
            rows.append({
                "element": eid,
                "kind": next(e["kind"] for e in page_els if e["id"] == eid),
                "page": page["index"],
                "pageStart": base,
                "appearMs": appear,
                "always": always,
                "series": [round(v, 1) for v in series[:12]],
                "peak": round(max(series), 1),
            })
        print("page %d ok: %d 元素 / %d 帧" % (page["index"], len(boxes), len(seq)))

    # 与段落开口对账：元素 → 它被绑到的那一段（没有绑定就无 lead）
    bind_at = {}
    for page in script["pages"]:
        for seg in page["segments"]:
            for ref_ in seg.get("bind", []):
                bind_at[ref_["elementId"]] = (seg["id"], starts[page["index"]] + seg["startMs"],
                                              ref_.get("offsetMs", 0))
    for r in rows:
        if r["element"] in bind_at:
            sid, at, off = bind_at[r["element"]]
            r["seg"], r["at"], r["offsetMs"] = sid, at, off
            if r["appearMs"] is not None:
                # lead 扣掉设计的段内错落：offset=+700 时，它晚 700ms 出是照设计来的
                r["leadMs"] = (at + off) - r["appearMs"]
        else:
            r["seg"], r["at"], r["leadMs"] = None, None, None

    print()
    print("%-12s %-5s %-6s %7s %8s %8s  %s" % (
        "element", "kind", "seg", "at", "appear", "lead", "判定"))
    for r in sorted(rows, key=lambda x: (x["page"], x["appearMs"] or 0)):
        if r["appearMs"] is None:
            verdict = "整页未稳定出现"
        elif r["always"]:
            verdict = "页首就在（常驻）" if r["seg"] is None else "页首就在（**未绑定也常驻**）"
        elif r["seg"] is None:
            verdict = "未绑定，%.1fs 出" % (r["appearMs"] / 1000)
        elif r["leadMs"] > 200:
            verdict = "提前 %.0fms（剧透）" % r["leadMs"]
        elif r["leadMs"] < -120:
            verdict = "滞后 %.0fms" % -r["leadMs"]
        else:
            verdict = "准时"
        print("%-12s %-5s %-6s %7s %8s %8s  %s" % (
            r["element"], r["kind"], r["seg"] or "-", r["at"] if r["at"] else "-",
            r["appearMs"], r["leadMs"] if r["leadMs"] is not None else "-", verdict))

    os.makedirs("tmp/q7", exist_ok=True)
    json.dump(rows, open("tmp/q7/appearance.json", "w", encoding="utf-8"),
              ensure_ascii=False, indent=1)
    print("\n→ tmp/q7/appearance.json")


if __name__ == "__main__":
    main()
