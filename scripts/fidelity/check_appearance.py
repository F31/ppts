#!/usr/bin/env python3
"""每个元素**在整页范围内**什么时候开始变、什么时候到位，以及开口那一刻它到位了没有。

为什么另写一个：`check_alignment.py` 只看段落开口前 400ms（`t0 = max(page_start, at-400)`）。
如果元素在更早的时候就出现了（页首一起铺、或跟着上一段出来），窗口里它是「已经在的」，
相对窗口首帧的灰度差恒为 0 → `preMax = 0`、`delay = 0`，工具报「不提前、准时」，
而人看到的是「话还没讲，东西已经摆好了」。400ms 之外的提前，这个工具结构上量不到。

## 参照物换了三次，前两次都被片子推翻

1. **该页最后一帧**（第一版）。页级焦点把非焦点元素压暗，页末那一帧恰好是
   「最后一条亮、前面几条暗」——前面几条在被压暗的那一刻被判成「刚到位」，
   `p1-item0` 的 6.03s 被报成 12.43s。
2. **框内对比度能量**（第二版，像素偏离自身中位数）。对压暗免疫，因为压暗是仿射变换。
   但它对**实心块**是瞎的：一根柱子长满之后，框里是一整块纯色，内部几乎没有偏离，
   能量在「长了一半、上半截还是卡片底」时最高、在终态最低。于是柱子被报成
   「整页未稳定出现」——片子是对的，量法错了。
3. **出场前那一帧**（本版）。信号改为「框内与**它出现之前**差多少」：
   柱子从 0 长到满，这个差从 0 升到峰值；淡入是 0→峰值；压暗按比例缩小到约 0.55 倍，
   仍在一半以上。参照点在**之前**而不是之后，是这一版的关键——元素的「终态」会被
   后来的编排改写（压暗、二次强调），而「出场前」不会被改写。

   顺带记一个中间版为什么不行：先试的是「与出场前不同的**像素占比**」（阈值化），
   它对压暗不免疫——页 3 的流程块是浅色卡片，压到 0.55 后与白底的差从 17 灰度掉到 8，
   低于阈值就不计数了，于是 `p3-flow` 被报成「整页未稳定出现」。连续量（平均绝对差）
   保留了「差多少」的信息，压暗只是等比缩放，不会归零。

三样输出：

- `settledMs`：填充率达到峰值 85% 的最后一帧的下一帧，即它**到位**的时刻。带淡入的
  元素这个值比开口晚整整一个淡入时长（220–450ms），**不能拿它判「滞后」**。
- `onsetMs`：填充率第一次达到峰值 30% 的时刻。诊断用：若背后有同步淡入的容器，
  容器一变，坐在它上面的元素框里也跟着变，读数会一起塌到容器出现的那一刻。
- `preSegLevel`：**开口前 100ms 那一帧的填充率相对峰值**（0 = 框里空的，1 = 已到位）。
  这是「不提前」这条判据本来在问的事，单帧比较，不看曲线。

用法：
  python3 scripts/fidelity/check_appearance.py [film] [segments.json] [elements.json]
输出：tmp/q7/appearance.json
"""
import json
import os
import subprocess
import sys

import numpy as np

FPS = 30
FRAME_MS = 1000 // FPS
W, H = 1920, 1080

# 「不提前」的取样点：开口前 3 帧（与判据「3 帧内出现」同量级）。取 1 帧的话，
# 瞬时出现（无淡入）的元素会正好落在边界上被误判成提前。
PRE_SEG_MS = 100

# 与「出场前」的平均绝对差低于这个灰度值，就认为框里自始至终没变（纯底板、容器描边）。
CONTENT_MIN = 1.0

# 变化量达到峰值的这个比例，就算「到位」。0.5 是必需的余量：焦点的非目标元素
# 会整体压到 0.55 不透明度，变化量随之落到约 0.55——仍在半程以上，不会被判成
# 「又变了一次」。取 0.85 的话，压暗那一刻就会被当成新的到位点（页 3 的流程块踩过）。
LEVEL_SETTLED = 0.5

# 开口前那一帧的变化量达到峰值的这个比例，就算「话还没讲、东西已经摆好了」。
LEVEL_EARLY = 0.5


def change_level(arr, ref):
    """框内相对「出场前」的平均绝对差。

    连续量，不是阈值化的占比：压暗是仿射变换（a·x+(1−a)·底色），差按 a 等比缩小、
    不会归零；而「有多少像素超过阈值」会在压暗后整块掉到 0。
    """
    return float(np.mean(np.abs(arr.astype(np.int16) - ref.astype(np.int16))))


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


def frame_gray(film, t_ms):
    """抽一帧灰度图。用于取「出场前」的参照。"""
    cmd = [ffmpeg_bin(), "-v", "error", "-ss", "%.3f" % (t_ms / 1000.0),
           "-i", film, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-"]
    out = subprocess.run(cmd, capture_output=True).stdout
    if len(out) < W * H:
        return None
    return np.frombuffer(out[:W * H], np.uint8).reshape(H, W)


def main():
    # 与 check_alignment 同口径：先把 --out 摘出去，C/D 两版各写各的报告。
    raw = sys.argv[1:]
    out_path = "tmp/q7/appearance.json"
    if "--out" in raw:
        k = raw.index("--out")
        out_path = raw[k + 1]
        del raw[k:k + 2]

    film = raw[0] if len(raw) > 0 else "tmp/q7/film_C_narrated.mp4"
    seg_path = raw[1] if len(raw) > 1 else "tmp/q7/segments.json"
    elems_path = raw[2] if len(raw) > 2 else "tmp/q7/fid/elements.json"
    threshold = float(raw[3]) if len(raw) > 3 else 2.0

    script = json.load(open(seg_path, encoding="utf-8"))
    elems = json.load(open(elems_path, encoding="utf-8"))

    # 页起点用实测段时长累计（与 check_alignment.py 同一口径，否则两边的 t 对不上）
    page_start, starts = 0, {}
    for page in script["pages"]:
        starts[page["index"]] = page_start
        seg_file = "tmp/q7/out/segment_%03d.mp4" % page["index"]
        page_start += probe_ms(seg_file) if os.path.exists(seg_file) else page["durationMs"]

    # 元素 → 它被绑到的那一段（段号、绝对时刻、段内错落）
    bind_at = {}
    for page in script["pages"]:
        for seg in page["segments"]:
            for ref in seg.get("bind", []):
                bind_at[ref["elementId"]] = (
                    seg["id"], starts[page["index"]] + seg["startMs"], ref.get("offsetMs", 0))

    def clip(box):
        x, y, w, h = [int(round(v)) for v in box]
        x0, y0 = max(0, x), max(0, y)
        x1, y1 = min(W, x + w), min(H, y + h)
        return y0, y1, x0, x1

    rows = []
    for page in script["pages"]:
        base = starts[page["index"]]
        nxt = starts.get(page["index"] + 1)
        dur = (nxt - base) if nxt is not None else page["durationMs"]
        page_els = elems[page["index"]]["elements"]
        boxes = {e["id"]: clip(e["box"]) for e in page_els}

        # 「出场前」参照：取该元素**设计起点前一帧**的框内画面。
        # 参照点必须在之前——终态会被后来的编排改写（压暗、二次强调），而之前不会。
        refs = {}
        for eid, b in boxes.items():
            if eid in bind_at:
                _, at, off = bind_at[eid]
                t_ref = at + off - FRAME_MS
            else:
                t_ref = base
            t_ref = max(base, min(base + dur - FRAME_MS, t_ref))
            fr = frame_gray(film, t_ref)
            if fr is None:
                continue
            refs[eid] = fr[b[0]:b[1], b[2]:b[3]].copy()

        seq = []          # [(t_ms, {eid: 填充率})]
        for t, frame in page_frames(film, base, dur):
            # 注意别用上一个循环残留的 b：按元素逐个取自己的框（第一版写错过，
            # 所有元素都拿最后一个元素的框去裁，形状对不上直接抛异常）。
            seq.append((t, {eid: change_level(frame[b[0]:b[1], b[2]:b[3]], refs[eid])
                            for eid, b in boxes.items() if eid in refs}))
        if not seq:
            continue
        boxes = {eid: b for eid, b in boxes.items() if eid in refs}

        for eid, b in boxes.items():
            vals = [v[eid] for _, v in seq]
            peak = max(vals) if vals else 0.0
            if peak < CONTENT_MIN:
                # 框里自始至终没有变化：纯底色、容器描边、或这一页就没画它。
                # 对「什么时候出现」而言，它与「页首就在」同义。
                settled, always, onset = seq[0][0], True, None
                row_norm = [0.0] * min(12, len(vals))
            else:
                norm = [v / peak for v in vals]
                # 取**最后一次**低于半程的帧的下一个：那之前它还没到位，之后一直在。
                # 不能取第一次越线——元素在页首是「不存在」，第一次越线会被量成
                # 「页首就出现」（第一版写反过，全页都报页首）。
                last = max((i for i, v in enumerate(norm) if v < LEVEL_SETTLED), default=None)
                if last is None:
                    settled, always = seq[0][0], True
                else:
                    settled = seq[last + 1][0] if last + 1 < len(seq) else None
                    always = False
                # 开始变：能量第一次达到峰值的 30%。不用「相邻帧跳幅」——淡入是渐进的，
                # 逐帧增幅可能一直低于阈值，反而在**被点亮**（压暗 → 全亮）的那一帧
                # 抓到最大的跳变，报出一个比 settled 还晚的 onset（p3-hero 真踩过）。
                onset = next((seq[i][0] for i, v in enumerate(norm) if v >= 0.3), None)
                row_norm = [round(v, 2) for v in norm[:12]]
            row = {
                "element": eid,
                "kind": next(e["kind"] for e in page_els if e["id"] == eid),
                "page": page["index"],
                "pageStart": base,
                "settledMs": settled,
                "onsetMs": onset,
                "always": always,
                "series": row_norm,
                "peak": round(peak, 1),
            }
            # 「开口那一刻到位了没有」：取 at-PRE_SEG_MS 最近的一帧，看它的能量
            # 相对峰值有多少。0 = 框里空的，1 = 已经是最终状态。
            # 注意扣掉段内设计错落：offset=+700 的元素本来就该晚 700ms 才动。
            if eid in bind_at:
                sid, at, off = bind_at[eid]
                target = at + off - PRE_SEG_MS
                near_t, near = min(seq, key=lambda s: abs(s[0] - target))
                row["preSegLevel"] = round(near[eid] / peak, 2) if peak > 0 else 0.0
                row["preSegAt"] = near_t
            else:
                row["preSegLevel"] = None
            rows.append(row)
        print("page %d ok: %d 元素 / %d 帧" % (page["index"], len(boxes), len(seq)))

    # 与段落开口对账：元素 → 它被绑到的那一段（没有绑定就无 lead）
    for r in rows:
        if r["element"] in bind_at:
            sid, at, off = bind_at[r["element"]]
            r["seg"], r["at"], r["offsetMs"] = sid, at, off
            if r["settledMs"] is not None:
                # lead 扣掉设计的段内错落：offset=+700 时，它晚 700ms 到位是照设计来的
                r["leadMs"] = (at + off) - r["settledMs"]
        else:
            r["seg"], r["at"], r["leadMs"] = None, None, None

    print()
    print("%-12s %-5s %-6s %7s %8s %8s %6s  %s" % (
        "element", "kind", "seg", "at", "onset", "settled", "pre", "判定"))
    for r in sorted(rows, key=lambda x: (x["page"], x["settledMs"] or 0)):
        early = r["preSegLevel"] is not None and r["preSegLevel"] >= LEVEL_EARLY
        if r["settledMs"] is None:
            verdict = "整页未稳定出现"
        elif early:
            verdict = "**开口前已到位（剧透）**"
        elif r["always"]:
            verdict = "页首就在（常驻）" if r["seg"] is None else "页首就在（**未绑定也常驻**）"
        elif r["seg"] is None:
            verdict = "未绑定，%.1fs 出" % (r["settledMs"] / 1000)
        elif r["leadMs"] < -600:
            verdict = "到位晚 %.0fms（含淡入）" % -r["leadMs"]
        else:
            verdict = "开口后到位"
        print("%-12s %-5s %-6s %7s %8s %8s %6s  %s" % (
            r["element"], r["kind"], r["seg"] or "-", r["at"] if r["at"] else "-",
            r["onsetMs"], r["settledMs"], r["preSegLevel"], verdict))

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    json.dump(rows, open(out_path, "w", encoding="utf-8"),
              ensure_ascii=False, indent=1)
    print("\n→ %s" % out_path)


if __name__ == "__main__":
    main()
