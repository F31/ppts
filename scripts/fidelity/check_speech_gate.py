#!/usr/bin/env python3
"""讲稿绑定预填的开口门禁（changes/animation-narration-binding tasks 5.1）。

回答一个问题：**讲解说到第 N 段时，第 N 条才出现吗？**

三条判据，全都在**成片像素**上量：

  V20.1 开口前 400ms 内区域变化为 0 —— 元素包围盒相对「窗口首帧」的平均绝对差
        在这段时间里不超过阈值。超了就是提前出现（页首铺满、跟着上一段出来、
        或者位移把静态副本露了出来）。
  V20.2 出现不早于开口 —— 首次越过阈值的帧不早于段开口所在帧。
        与 V20.1 分开是因为它们会在不同实现上分别失败：V20.1 卡的是「一直亮着」，
        V20.2 卡的是「窗口首帧恰好已经是它出现之后」。
  V20.3 出现不晚于开口 + 预算 —— 默认 900ms。段内错落（同段第二、三个元素）
        是正常编排，不是迟到；但绑到了这一段却等到下一段才出现，是错的。

与 check_alignment.py 的分工：那个工具回答「元素什么时候出现」，用于人工评审，
读的是 segments.json 里**手写**的 bind；本工具回答「预填产物是否守时」，读的是
plan 编译时写回的**实测** bind（segments-prefill.json），并且给退出码。

为什么每个元素只调一次 ffmpeg：按帧调用在小样片上是 800 次进程启动（十几分钟），
一次抽完整窗并裁到包围盒只要一次解码。判据不变，量法不该成为瓶颈。

用法：
  python3 scripts/fidelity/check_speech_gate.py \
      --film tmp/prefill-sample/film_prefill_narrated.mp4 \
      --segments tmp/prefill-sample/job/segments-prefill.json \
      --elements tmp/q7/fid/elements.json \
      --out tmp/prefill-sample/speech-gate.json

负对照（证明这把尺子量得出提前）：
  python3 scripts/fidelity/check_speech_gate.py ... --negative-control
  把每个段的开口**后移** 400ms 再判：真有元素在那 400ms 里出现的话，
  它就该被判成「提前」。若负对照下一条都不红，说明阈值宽到没有判别力。
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile

from _segtime import page_starts

FPS = 30
FRAME_MS = 1000.0 / FPS
LEAD_MS = 400.0     # 「开口前」的观察窗
THRESHOLD = 2.0     # 灰度平均绝对差阈值（与 check_alignment 一致：编码噪声在 0.5–1.3）
MAX_DELAY_MS = 900  # 段内错落的预算


def ffmpeg_bin(explicit):
    if explicit:
        return explicit
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def probe_ms(path, ffmpeg):
    err = subprocess.run([ffmpeg, "-hide_banner", "-i", path],
                         capture_output=True, text=True, errors="replace").stderr
    for line in err.splitlines():
        if "Duration:" in line:
            h, m, s = line.split("Duration:")[1].split(",")[0].strip().split(":")
            return round((int(h) * 3600 + int(m) * 60 + float(s)) * 1000)
    return 0


def crop_gray(film, start_ms, count, box, ffmpeg):
    """一次解码抽出 count 帧、裁到 box、转灰度，返回 [bytes]（每帧 w*h）。

    裁切参数必须是偶数对齐的整数：odd 宽高会让 gray 的 stride 与 w*h 不一致，
    算出来的 MAD 就是「两张图错位比较」，看起来永远不为 0。
    """
    x, y, w, h = box
    w -= w % 2
    h -= h % 2
    if w <= 0 or h <= 0:
        return [], (0, 0)
    raw = subprocess.check_output([
        ffmpeg, "-v", "error", "-ss", "%.3f" % (start_ms / 1000.0), "-i", film,
        "-frames:v", str(max(1, count)),
        "-vf", "crop=%d:%d:%d:%d,format=gray" % (w, h, x, y),
        "-f", "rawvideo", "-pix_fmt", "gray", "-",
    ])
    size = w * h
    return [raw[i:i + size] for i in range(0, len(raw) - size + 1, size)], (w, h)


def mad(a, b):
    if not a or not b or len(a) != len(b):
        return 0.0
    return sum(abs(x - y) for x, y in zip(a, b)) / len(a)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--film", required=True)
    ap.add_argument("--segments", required=True)
    ap.add_argument("--elements", required=True)
    ap.add_argument("--out", default="")
    ap.add_argument("--ffmpeg", default="")
    ap.add_argument("--lead-ms", type=float, default=LEAD_MS)
    ap.add_argument("--threshold", type=float, default=THRESHOLD)
    ap.add_argument("--max-delay-ms", type=float, default=MAX_DELAY_MS)
    ap.add_argument("--negative-control", action="store_true",
                    help="把开口后移 400ms 再判：尺子有牙时，这会让「提前」判据集体报红")
    args = ap.parse_args()

    ffmpeg = ffmpeg_bin(args.ffmpeg)
    script = json.load(open(args.segments, encoding="utf-8"))
    elems = json.load(open(args.elements, encoding="utf-8"))

    boxes, kinds = {}, {}
    for page in elems:
        for e in page["elements"]:
            boxes[e["id"]] = e["box"]
            kinds[e["id"]] = e["kind"]

    starts, _ = page_starts(script, lambda p: probe_ms(p, ffmpeg))

    rows = []
    with tempfile.TemporaryDirectory() as tmp:
        del tmp  # 本工具不落帧：一次解码直接算，不需要中间文件
        for page in script["pages"]:
            base = starts[page["index"]]
            for seg in page["segments"]:
                at = base + seg["startMs"]
                if args.negative_control:
                    # 负对照：假装这一段晚 400ms 开口。元素若真在 at 出现，
                    # 那它就在「假开口」之前的 400ms 内变了——判据必须把它抓出来。
                    at += args.lead_ms
                binds = seg.get("bind") or []
                if not binds:
                    continue
                for ref in binds:
                    eid = ref["elementId"]
                    box = boxes.get(eid)
                    if not box:
                        continue
                    t0 = max(base, at - args.lead_ms)
                    t_end = at + args.max_delay_ms
                    count = int((t_end - t0) / FRAME_MS) + 1
                    frames, size = crop_gray(args.film, t0, count, box, ffmpeg)
                    if len(frames) < 2:
                        rows.append({"element": eid, "kind": kinds.get(eid, ""), "seg": seg["id"],
                                     "at": at, "verdict": "no_frames"})
                        continue
                    base_frame = frames[0]
                    series = [mad(base_frame, f) for f in frames]
                    pre = [v for i, v in enumerate(series)
                           if t0 + i * FRAME_MS < at - FRAME_MS / 2]
                    pre_max = max(pre) if pre else 0.0
                    onset_i = next((i for i, v in enumerate(series)
                                    if v > args.threshold), None)
                    delay = None if onset_i is None else round(t0 + onset_i * FRAME_MS - at)
                    fails = []
                    # 峰值都不过阈值 = 这个包围盒里根本没有可测的变化。此时「preMax=0」
                    # 不是「没提前」，而是「什么都没发生」——空跑的通过不算通过。
                    if max(series) <= args.threshold:
                        fails.append("no_signal")
                    if pre_max > args.threshold:
                        fails.append("early")
                    if onset_i is None:
                        fails.append("missing")
                    elif delay < -FRAME_MS / 2:
                        fails.append("early")
                    elif delay > args.max_delay_ms:
                        fails.append("late")
                    rows.append({
                        "element": eid, "kind": kinds.get(eid, ""), "seg": seg["id"],
                        "at": round(at), "preMax": round(pre_max, 1),
                        "delayMs": delay, "peak": round(max(series), 1),
                        "fails": sorted(set(fails)),
                    })

    ok = [r for r in rows if not r.get("fails")]
    bad = [r for r in rows if r.get("fails")]

    print("%-12s %-6s %8s %7s %7s %7s  %s" % (
        "element", "seg", "at", "pre", "delay", "peak", "判定"))
    for r in rows:
        mark = "ok" if not r.get("fails") else "FAIL " + ",".join(r["fails"])
        print("%-12s %-6s %8s %7s %7s %7s  %s" % (
            r["element"], r["seg"], r.get("at", ""), r.get("preMax", ""),
            r.get("delayMs", ""), r.get("peak", ""), mark))

    if args.out:
        os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
        json.dump({"rows": rows, "threshold": args.threshold,
                   "leadMs": args.lead_ms, "maxDelayMs": args.max_delay_ms,
                   "negativeControl": args.negative_control},
                  open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)

    if args.negative_control:
        # 尺子有牙 = 元素在「假开口」之前就被看见。阈值 80% 而不是 100%，理由是
        # 有两条慢动画落在窗口尾部：chart 容器 500ms 淡入、柱子 500ms 生长，
        # 在「假开口」那一刻只走了 60–80%，灰度差还没越过 2.0。它们不是量不到
        # （peak 8.6 / 108），只是那一刻还没显形——把阈值收紧到能抓它们，
        # 正常判据就会开始误报。
        caught = [r for r in rows if "early" in r.get("fails", [])]
        ratio = len(caught) / len(rows) if rows else 0.0
        print("\n负对照：%d/%d 条被判提前（开口后移 %.0fms），比例 %.2f" % (
            len(caught), len(rows), args.lead_ms, ratio))
        for r in rows:
            if "early" not in r.get("fails", []):
                print("  未被判提前：%s（peak=%s pre=%s）" % (
                    r["element"], r.get("peak"), r.get("preMax")))
        return 0 if rows and ratio >= 0.8 else 1

    print("\n%d 条绑定：%d 通过 / %d 不过" % (len(rows), len(ok), len(bad)))
    for r in bad:
        print("  不过：%s（%s）pre=%s delay=%s" % (
            r["element"], r["seg"], r.get("preMax"), r.get("delayMs")))
    return 0 if rows and not bad else 1


if __name__ == "__main__":
    sys.exit(main())
