#!/usr/bin/env python3
"""出片保真检查：逐页比对「视频末帧」与源底图，回答「信息有没有丢」。

判据（ADR-18）：
  - 整页平均绝对差 ≤ 2.0
  - 差 >32 的像素占比 < 1%
  - 任一元素区域内「墨量」不低于源图的 80%

末帧的取法有讲究：入场动画在页末已回到恒等，此时画面应当等于源底图。
取页中任意一帧都会把动画状态算进差异里。

用法：

    python3 scripts/fidelity/compare.py \
        --pages /tmp/fidelity/pages \
        --elements /tmp/fidelity/elements.json \
        --segments /tmp/job            # 含 segment_*.mp4 的目录
"""
import argparse
import glob
import json
import os
import subprocess
import sys
import tempfile

import numpy as np
from PIL import Image


def last_frame(seg, tmpdir):
    dur = float(subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", seg],
        capture_output=True, text=True, check=True).stdout.strip())
    out = os.path.join(tmpdir, os.path.basename(seg) + ".png")
    subprocess.run(["ffmpeg", "-v", "error", "-ss", f"{max(0.0, dur - 0.05):.3f}",
                    "-i", seg, "-frames:v", "1", "-y", out], check=True)
    return out


def ink(img, med):
    """偏离基准色 >40 的像素占比。基准色只从源图取：两边各取中位会把渐变区域的
    基准漂移算成差异（第一版就是这么虚报了 3.7 个百分点）。"""
    return float((np.abs(img - med).max(axis=2) > 40).mean() * 100)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pages", required=True, help="源页 PNG 目录")
    ap.add_argument("--elements", required=True, help="gen_pages.py 产出的 elements.json")
    ap.add_argument("--segments", required=True, help="含 segment_*.mp4 的目录")
    args = ap.parse_args()

    meta = json.load(open(args.elements, encoding="utf-8"))
    segs = sorted(f for f in glob.glob(os.path.join(args.segments, "**", "segment_*.mp4"), recursive=True))
    if len(segs) != len(meta):
        print(f"段数 {len(segs)} 与页数 {len(meta)} 不一致", file=sys.stderr)
        return 2

    worst_mean = 0.0
    worst_big = 0.0
    worst_ink_ratio = float("inf")
    with tempfile.TemporaryDirectory() as tmp:
        for m, seg in zip(meta, segs):
            frame = last_frame(seg, tmp)
            a = np.asarray(Image.open(frame).convert("RGB"), dtype=np.int16)
            b = np.asarray(Image.open(os.path.join(args.pages, f"p{m['index']:03d}.png")).convert("RGB"),
                           dtype=np.int16)
            if a.shape != b.shape:
                print(f"page {m['index']}: 尺寸不一致 {a.shape} vs {b.shape}", file=sys.stderr)
                return 2
            d = np.abs(a - b)
            mean = float(d.mean())
            big = float((d.max(axis=2) > 32).mean() * 100)

            worst_elem, worst_ratio, worst_region = "", float("inf"), 0.0
            for e in m["elements"]:
                x, y, w, h = e["box"]
                sa, sb = a[y:y + h, x:x + w], b[y:y + h, x:x + w]
                med = np.median(sb.reshape(-1, 3), axis=0)
                ia, ib = ink(sa, med), ink(sb, med)
                # 源图该区域几乎没有图形（纯底色）时，比值没有意义，跳过。
                if ib < 0.5:
                    continue
                ratio = ia / ib
                if ratio < worst_ratio:
                    worst_elem, worst_ratio = e["id"], ratio
                    worst_region = float((np.abs(sa - sb).max(axis=2) > 32).mean() * 100)

            worst_mean = max(worst_mean, mean)
            worst_big = max(worst_big, big)
            worst_ink_ratio = min(worst_ink_ratio, worst_ratio)
            elem = worst_elem if worst_elem else "（无非空元素区域）"
            ratio_text = "n/a" if worst_ratio == float("inf") else f"{worst_ratio:.2f}"
            print(f"page {m['index']}: mean={mean:.2f} >32={big:.3f}% "
                  f"最差元素={elem} 墨量比={ratio_text} 该区域>32={worst_region:.3f}%")

    ratio_ok = worst_ink_ratio == float("inf") or worst_ink_ratio >= 0.8
    ratio_text = "n/a" if worst_ink_ratio == float("inf") else f"{worst_ink_ratio:.2f}"
    print(f"\n汇总: max(mean)={worst_mean:.2f} max(>32)={worst_big:.3f}% min(墨量比)={ratio_text}")
    ok = worst_mean <= 2.0 and worst_big < 1.0 and ratio_ok
    print("判定:", "PASS" if ok else "FAIL（结构性丢失，别当成编码噪声）")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
