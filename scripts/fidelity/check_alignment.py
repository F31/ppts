#!/usr/bin/env python3
"""量「元素到底在什么时候出现」。

判据不用肉眼：对每个绑定到段落的元素，取段落开口前后的一段连续帧，裁出元素
包围盒，算它与「开口前 400ms 那一帧」的灰度平均绝对差（MAD）。曲线第一次
越过阈值的帧，就是元素真正开始出现的那一帧。

为什么不用 PSNR/SSIM：这些图在开口前是纯底色（渲染器把元素挖掉了），
任何整体指标都会被大面积纯色压住；只裁包围盒才能把 2% 面积的变化放大出来。
为什么自己算 MAD：不引 numpy/PIL，`ffmpeg -f rawvideo -pix_fmt gray` 直接给
灰度字节，标准库足够，也就不用给这个一次性检查装依赖。
"""

import json
import os
import subprocess
import sys
import tempfile

FPS = 30
FRAME_MS = 1000.0 / FPS


def ffmpeg_bin():
    """ffmpeg 未必在 PATH 上（本机就只有 imageio-ffmpeg 自带的那一份）。
    硬写 "ffmpeg" 会让这个脚本在自动重跑时直接 FileNotFoundError，
    而调用方看到的只是「测量失败」——所以统一走这里。"""
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def probe_ms(path):
    """时长从 `ffmpeg -i` 的 Duration 行读：ffprobe 是独立二进制，更容易缺。"""
    err = subprocess.run([ffmpeg_bin(), "-hide_banner", "-i", path],
                         capture_output=True, text=True, errors="replace").stderr
    for line in err.splitlines():
        if "Duration:" in line:
            h, m, s = line.split("Duration:")[1].split(",")[0].strip().split(":")
            return round((int(h) * 3600 + int(m) * 60 + float(s)) * 1000)
    return 0


def extract(film, start_ms, count, outdir):
    """按「解码后 seek」抽连续帧，返回文件列表（第 i 个 ≈ start_ms + i*33.3ms）。"""
    # 目录是复用的：上一次多抽的尾巴必须清掉，否则会把上一段的帧算进这一段。
    for name in os.listdir(outdir):
        if name.startswith("f_"):
            os.remove(os.path.join(outdir, name))
    subprocess.run(
        [ffmpeg_bin(), "-y", "-loglevel", "error", "-i", film,
         "-ss", "%.3f" % (start_ms / 1000.0), "-frames:v", str(count),
         "-vsync", "0", os.path.join(outdir, "f_%03d.png")],
        check=True,
    )
    names = sorted(n for n in os.listdir(outdir) if n.startswith("f_"))
    return [os.path.join(outdir, n) for n in names]


def gray_box(png, box):
    x, y, w, h = box
    # 裁包围盒后转灰度原始字节；-f rawvideo 走 stdout，不落盘。
    return subprocess.check_output(
        [ffmpeg_bin(), "-v", "error", "-i", png,
         "-vf", "crop=%d:%d:%d:%d,format=gray" % (w, h, x, y),
         "-f", "rawvideo", "-"],
    )


def mad(a, b):
    if len(a) != len(b) or not a:
        return 0.0
    return sum(abs(x - y) for x, y in zip(a, b)) / len(a)


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_C_narrated.mp4"
    seg_path = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/segments.json"
    elems_path = sys.argv[3] if len(sys.argv) > 3 else "tmp/q7/fid/elements.json"
    threshold = float(sys.argv[4]) if len(sys.argv) > 4 else 2.0

    script = json.load(open(seg_path, encoding="utf-8"))
    elems = json.load(open(elems_path, encoding="utf-8"))
    boxes, kinds = {}, {}
    for page in elems:
        for e in page["elements"]:
            boxes[e["id"]] = e["box"]
            kinds[e["id"]] = e["kind"]

    # 页起点用**实测段时长**累计：渲染器按帧输出，每页会比计划短几毫秒，
    # 用计划值当起点会把误差一路累加到最后几页。
    page_start, starts = 0, []
    for page in script["pages"]:
        starts.append(page_start)
        seg_file = "tmp/q7/out/segment_%03d.mp4" % page["index"]
        page_start += probe_ms(seg_file) if os.path.exists(seg_file) else page["durationMs"]

    rows = []
    with tempfile.TemporaryDirectory() as tmp:
        for page in script["pages"]:
            base_t = starts[page["index"]]
            for seg in page["segments"]:
                for ref in seg.get("bind", []):
                    eid = ref["elementId"]
                    box = boxes[eid]
                    at = base_t + seg["startMs"]
                    # 窗口不能跨页：上一页的底色与这一页不同（渐变 vs 纯白），
                    # 页切换本身就会让包围盒的灰度差越过阈值，量出来是「提前 200ms」，
                    # 实际是页边界，不是元素提前出现。
                    t0 = max(base_t + FRAME_MS, at - 400)
                    # 窗口要覆盖段内的错落偏移（同段第二个元素可能 +700ms 才动）。
                    count = int((at + 1100 - t0) / FRAME_MS) + 1
                    files = extract(film, t0, max(2, count), tmp)
                    if not files:
                        continue
                    base = gray_box(files[0], box)
                    series = [mad(base, gray_box(f, box)) for f in files]
                    onset = None
                    for i, v in enumerate(series):
                        if v > threshold:
                            onset = i
                            break
                    delay = None if onset is None else round(t0 + onset * FRAME_MS - at)
                    show = series[: max(12, (onset or 0) + 3)]
                    rows.append({
                        "element": eid, "kind": kinds.get(eid, ""), "seg": seg["id"],
                        "at": at, "onset": None if onset is None else round(t0 + onset * FRAME_MS),
                        "delayMs": delay, "peak": round(max(series), 1),
                        "series": [round(v, 1) for v in show],
                        "offsetMs": ref.get("offsetMs", 0),
                        "preMax": round(max(series[: max(1, int((at - t0) / FRAME_MS))]), 1),
                    })

    print("%-12s %-6s %-6s %6s %7s %8s %6s  %s" % (
        "element", "seg", "kind", "at", "onset", "delay", "pre", "series"))
    for r in rows:
        print("%-12s %-6s %-6s %6d %7s %8s %6.1f  %s" % (
            r["element"], r["seg"], r["kind"], r["at"], r["onset"], r["delayMs"],
            r["preMax"], r["series"]))
    json.dump(rows, open("tmp/q7/alignment.json", "w", encoding="utf-8"),
              ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
