#!/usr/bin/env python3
"""画面连续性：**讲着话的时候，画面有没有长时间不动。**

## 为什么需要它

前面五组判据量的都是「元素对不对」：不提前、不滞后、有层次、不空转、有内容覆盖。
D 片（把三处编排改对之后）这五组全过——但页 1 在第 1.4s 就把四件事铺满了，
之后约 10 秒画面一动不动，而旁白还在逐条念。**没有任何一条判据问过这件事。**

「空」和「静」是两种病：前者是元素没出现（V7 管），后者是元素全出现了、
却再没有任何变化（本脚本管）。量表缺这一维，最优解就会被推向「所有内容页首铺满」。

## 怎么量

每 100ms 抽一帧缩到 320×180 灰度，算相邻帧的平均绝对差。静止画面的差接近 0
（H.264 对静止区域的残差几乎为零），有变化时远高于它。

再把「画面不动」的区间与**讲解区间**求交：页首等待、句间停顿里的静止不算问题，
观众在这时候本来就该喘口气。只有当旁白正在说话、而画面连续不变时才计。

用法：
  python3 scripts/fidelity/check_motion.py [film] [segments.json]
输出：tmp/q7/motion.json
"""
import json
import os
import subprocess
import sys

import numpy as np

FPS = 10                      # 采样 10Hz：100ms 分辨率足够判「一句一变」，也够便宜
FRAME_MS = 1000 // FPS
W, H = 320, 180

# 相邻帧的平均绝对差低于此值就算「没变」。静止区域的编码残差在 0.0x 量级，
# 一个 1 像素的位移在缩略图上也有 0.5 以上。0.25 取在两者之间。
SILENT_DIFF = 0.25


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def block_max(d, bs=40):
    """切成 40x40 的块，取块均值差的最大值。

    全幅平均会稀释小面积变化：页 2 上压暗三根柱子时全幅 mean 只有 6.4，但被压的
    那块差 48.8。V14 要判「切换是渐变还是硬跳」，必须看得见局部。
    """
    h, w = d.shape
    best = 0.0
    for y in range(0, h - bs + 1, bs):
        for x in range(0, w - bs + 1, bs):
            v = float(d[y:y + bs, x:x + bs].mean())
            if v > best:
                best = v
    return best


def frames(film):
    """流式解出缩略灰度帧，逐帧 yield (t_ms, ndarray)。"""
    cmd = [ffmpeg_bin(), "-v", "error", "-i", film,
           "-vf", "fps=%d,scale=%d:%d,format=gray" % (FPS, W, H),
           "-f", "rawvideo", "-"]
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    size = W * H
    i = 0
    try:
        while True:
            buf = proc.stdout.read(size)
            if not buf or len(buf) < size:
                break
            yield i * FRAME_MS, np.frombuffer(buf, np.uint8).reshape(H, W)
            i += 1
    finally:
        proc.stdout.close()
        proc.wait()


def speech_intervals(script):
    """讲解区间（绝对毫秒）。页起点按 segments 的页长累计——与其它测量同口径。"""
    out, base = [], 0
    for page in script["pages"]:
        for seg in page["segments"]:
            out.append((base + seg["startMs"], base + seg["endMs"]))
        base += page["durationMs"]
    return out


def overlap(a, b, lo, hi):
    return max(0, min(b, hi) - max(a, lo))


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_C_narrated.mp4"
    seg_path = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/segments.json"
    out_path = "tmp/q7/motion.json"
    if "--out" in sys.argv:
        out_path = sys.argv[sys.argv.index("--out") + 1]

    script = json.load(open(seg_path, encoding="utf-8"))
    speech = speech_intervals(script)

    # 1) 逐帧差 → 标记「这一帧画面变了没有」
    changed = []          # [(t_ms, diff, block)]
    prev = None
    for t, f in frames(film):
        if prev is None:
            changed.append((t, 255.0, 255.0))   # 第一帧视为「有变化」，不作为静默起点
        else:
            d = np.abs(f.astype(np.int16) - prev.astype(np.int16))
            changed.append((t, float(d.mean()), block_max(d)))
        prev = f
    if not changed:
        print("没有解出任何帧", file=sys.stderr)
        return 1

    # 2) 合并连续静止帧为区间
    spans = []
    start = None
    for t, d, _b in changed:
        if d < SILENT_DIFF:
            if start is None:
                start = t
        else:
            if start is not None:
                spans.append((start, t))
                start = None
    if start is not None:
        spans.append((start, changed[-1][0] + FRAME_MS))

    # 3) 只保留「讲着话」的部分
    talk_silent = []
    for a, b in spans:
        covered = sum(overlap(a, b, s, e) for s, e in speech)
        if covered >= 500:                  # 半秒以内的停顿不算
            talk_silent.append({"startMs": a, "endMs": b, "durMs": b - a,
                                "speechMs": covered})
    longest = max(talk_silent, key=lambda x: x["durMs"], default=None)

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    # block 序列给 V14 用：判「一次切换是跨了几帧，还是一帧跳完」。
    json.dump({"silentDiff": SILENT_DIFF, "sampleMs": FRAME_MS,
               "spans": talk_silent, "longestMs": longest["durMs"] if longest else 0,
               "steps": [{"tMs": t, "block": round(b, 3)} for t, _d, b in changed]},
              open(out_path, "w", encoding="utf-8"), ensure_ascii=False, indent=1)

    print("画面静止且正在讲解：%d 段" % len(talk_silent))
    for s in sorted(talk_silent, key=lambda x: -x["durMs"])[:6]:
        print("  %6.2fs–%6.2fs  %5.2fs（其中讲解 %.2fs）"
              % (s["startMs"] / 1000, s["endMs"] / 1000,
                 s["durMs"] / 1000, s["speechMs"] / 1000))
    if longest:
        print("最长 %.2fs" % (longest["durMs"] / 1000))
    print("\n→ %s" % out_path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
