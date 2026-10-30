#!/usr/bin/env python3
"""同一时刻亮着几个：压暗是互斥关系，所以每帧最多只能有一个元素是「满墨」。

这条判据是被一个活了五版的 bug 逼出来的。`shared/motion.ts` 的 `focusDim` 里有一行

    out[focus[0].elementId] = 0;      // 0 = 焦点 = 全亮

它漏了 `i === 0` 的条件。于是从**第 3 个焦点**开始，每帧都先把第 1 个元素重新点成
全亮，然后再没人改回去——页 2 从 6.0s 起，`bar0`（174s）与 `bar2`（943s）两根柱子
同时满墨，压暗失效了一半。页 1 同理：`p1-item0` 在第 3 条要点开讲后重新亮起。

为什么没有任何一条判据抓到它：

- V5 问的是「元素该动时动了没」——四个元素都动了，只是该暗的没暗。
- V7/V8 问的是「画面空不空、静不静」——画面不空也不静。
- V11 问的是「焦点什么时候切」——切换时刻是对的。
- 压暗本身从来没有判据：它是取景，不是内容。

所以这里只问一件事：**有人被压暗的那些帧里，满墨的必须恰好一个。** 过渡帧（两者都在
0.70~0.90 之间）不算——那时候本来就该有两个「半亮」，一个是让出去的、一个是接过来的。

## 墨量怎么量

框内 `255 − 均灰度`，除以**本页该元素的最大值**。压暗是 opacity 0.55，比值会落到 0.55；
满墨是 1.00。除的是自己页内的最大值而不是一个全局基线：四根柱子的底色不同（蓝紫 / 深蓝 /
青绿），拿同一个绝对值比会把颜色差异读成压暗差异。

采样降到 10fps、画面缩到 480×270：这条判据看的是「谁是亮的」，不是像素细节，
一个 160×519 的柱子在 480 宽下还有 40×130，够用。全片 35s 解 350 帧，约 20 秒。

用法：
  python3 scripts/fidelity/check_focus.py [film] [segments.json] [elements.json] [plan.json]
输出：tmp/q7/focus.json
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

# 满墨 / 压暗的判线。**两端的值不是同一个数**，因为压暗后的比值不是常数 0.55：
# 实心柱子（层把框填满）压到 0.55；卡片上的文字要点只有 0.75——框里还有卡片底色，
# 那部分不随层的不透明度变，比值被稀释。所以判线取「离 1 足够远」而不是「等于 0.55」：
# 0.85 以下算退后、0.95 以上算满墨，中间留给过渡与噪声。
BRIGHT_AT = 0.95
DIMMED_AT = 0.85


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
    """把一页解成缩小后的 gray 帧，逐帧 yield (页内 ms, ndarray)。流式，不落盘。"""
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


def ink(frame, box):
    k = SCALE_W / BASE_W
    x, y, w, h = [int(round(v * k)) for v in box]
    x, y = max(0, x), max(0, y)
    patch = frame[y:y + max(1, h), x:x + max(1, w)]
    return float((255.0 - patch.astype(np.float32)).mean())


def runs(hits):
    """把逐采样点的命中合并成区间：报告要给人看，350 行采样点没人读。"""
    out = []
    for t, bright, dimmed in hits:
        if out and t - out[-1]["toMs"] <= 1000 // SAMPLE_FPS + 1:
            out[-1]["toMs"] = t
            out[-1]["frames"] += 1
            for b in bright:
                if b not in out[-1]["bright"]:
                    out[-1]["bright"].append(b)
        else:
            out.append({"fromMs": t, "toMs": t, "frames": 1,
                        "bright": list(bright), "dimmed": list(dimmed)})
    return out


def main():
    argv = sys.argv[1:]
    out_path = os.environ.get("PPTS_FOCUS_OUT", "tmp/q7/focus.json")
    if "--out" in argv:
        k = argv.index("--out")
        out_path = argv[k + 1]
        argv = argv[:k] + argv[k + 2:]
    film = argv[0] if len(argv) > 0 else "tmp/q7/film_H_narrated.mp4"
    seg_path = argv[1] if len(argv) > 1 else "tmp/q7/segments.json"
    el_path = argv[2] if len(argv) > 2 else "tmp/q7/fid/elements.json"
    plan_path = argv[3] if len(argv) > 3 else "tmp/q7/job/plan.json"

    script = json.load(open(seg_path, encoding="utf-8"))
    elems = json.load(open(el_path, encoding="utf-8"))
    plan = json.load(open(plan_path, encoding="utf-8"))
    boxes = {p["index"]: {e["id"]: e["box"] for e in p["elements"]} for p in elems}
    starts, total = page_starts(script, probe_ms)

    report, checked = [], 0
    for page in script["pages"]:
        idx = page["index"]
        pl = next((p for p in plan.get("pages") or [] if p.get("index") == idx), None)
        spans = (pl or {}).get("focus") or []
        ids = [f["elementId"] for f in spans]
        if len(ids) < 2:
            continue
        missing = [i for i in ids if i not in boxes.get(idx, {})]
        if missing:
            raise SystemExit("页 %d 的焦点元素在 elements.json 里找不到：%s" % (idx, missing))
        checked += 1

        # 先整页收完再算比值：分母是「本页最大值」，边收边算的话，前面还没长到
        # 满高的帧会把自己当成基准，比值恒为 1，判据静悄悄地全绿。
        frames = list(page_frames(film, starts[idx], page["durationMs"]))
        full = {i: max(ink(f, boxes[idx][i]) for _, f in frames) for i in ids}
        first_ms = spans[0]["startMs"]
        hits, prev = [], None
        for t, frame in frames:
            ratio = {i: (ink(frame, boxes[idx][i]) / full[i] if full[i] > 1 else 1.0)
                     for i in ids}
            # 两个必须排除的时段，理由都不是「宽容」，而是「这时的比值不代表压暗」：
            # ① 第一个焦点之前不算——那时还没有让位关系，元素在长高（grow），
            #    没长满的柱子比值会掉到 0.1，被读成「被压暗了」。
            # ② 比值正在变的帧不算——过渡帧里本来就该有两个「半亮」：一个让出去的
            #    正在变暗、一个接过来的正在变亮。判「同亮」只看稳态。
            moving = prev is not None and any(abs(ratio[i] - prev[i]) > 0.15 for i in ids)
            prev = ratio
            # ③ 任何元素落在过渡带里都不判——那时它正在变亮或变暗，「亮/暗」二选一
            #    本来就没有意义。只看两端都落定的帧。
            between = any(DIMMED_AT < ratio[i] < BRIGHT_AT for i in ids)
            if t < first_ms or moving or between:
                continue
            bright = [i for i in ids if ratio[i] >= BRIGHT_AT]
            dimmed = [i for i in ids if ratio[i] <= DIMMED_AT]
            # 没人被压暗 = 空档（放手之后），这时全亮是正常的。
            if dimmed and len(bright) > 1:
                hits.append((t, bright, dimmed))

        page_report = {"index": idx, "pageStartMs": starts[idx], "violations": runs(hits)}
        # 区间同时给页内与全片两种时间：页内便于对着编排读，全片便于在播放器里定位。
        for v in page_report["violations"]:
            v["fromAbsMs"] = starts[idx] + v["fromMs"]
            v["toAbsMs"] = starts[idx] + v["toMs"]
        report.append(page_report)
        n = sum(v["frames"] for v in page_report["violations"])
        if n:
            v = page_report["violations"][0]
            print("页 %d：%d 个采样点上亮了不止一个（%.2fs 起，%s）"
                  % (idx, n, (starts[idx] + v["fromMs"]) / 1000, " / ".join(v["bright"])))
        else:
            print("页 %d：%d 个焦点元素，没有同亮" % (idx, len(ids)))

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump({"film": os.path.basename(film), "pages": report,
                   "checked": checked, "totalMs": total}, fh, ensure_ascii=False, indent=1)
    print("→ %s（%d 页有焦点）" % (out_path, checked))
    # 退出码一律 0：这是**测量**，判红是门禁（V13.1）的事。测量脚本非零退出会让
    # --rerun 把「测出问题了」当成「工具坏了」而中断，两种失败长得一样就没法分工。
    return 0


if __name__ == "__main__":
    sys.exit(main())
