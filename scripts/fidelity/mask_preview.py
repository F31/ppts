#!/usr/bin/env python3
"""矩形遮罩压暗的**视觉预览**：不是产品实现，是给「逐柱指代该怎么呈现」定样子。

## 为什么是遮罩而不是分层

`stagechart` 已经证明逐柱**分层**在纯位图下没有正确形态：柱区域抹掉后恢复不了
背后的网格线，标签又画在柱 box 之外（要么数字提前出现，要么网格线断）。
但「指某一根柱」并不需要柱子是图层——只需要一个矩形：把整页盖一层白纱，
目标矩形不盖。配色强度与现有 `focusDim` 对齐：非焦点层不透明度 0.55，
等价于叠 45% 白（`shared/motion.ts` 的 `FOCUS_DIM_OPACITY`）。

## 两个必须的细节（都从实测里来）

- **矩形要把数据标签包进去**：标签在柱顶上方约 16–41px。只框柱体时，焦点柱
  自己的数字也被压淡了——图上最该清楚的数字反而灰了。
- **边缘要羽化**：硬边会在网格线上切出「一亮一淡」的接缝，8px 高斯足够。

## 用法

    python3 scripts/fidelity/mask_preview.py --png tmp/chart/hi-1.png \
        --box 385,429,173,370 --label-pad 45 --out tmp/chart/mask-preview.png
"""
import argparse
import os
import re
import sys

import numpy as np
from PIL import Image, ImageFilter

# 与 shared/motion.ts 的 FOCUS_DIM_OPACITY = 0.55 对应：非焦点 = 叠 45% 白。
DEFAULT_DIM = 0.45


def compose(base, box, dim=DEFAULT_DIM, label_pad=45, feather=8):
    """返回预览图（ndarray）。base 是 HxWx3，box 是 (x, y, w, h)。"""
    a = base.astype(np.float32)
    x, y, w, h = box
    # alpha=1 处压暗、0 处保持原样
    alpha = np.ones((a.shape[0], a.shape[1]), dtype=np.float32)
    y0 = max(0, y - label_pad)
    x0 = max(0, x)
    alpha[y0:y + h, x0:x0 + w] = 0
    if feather > 0:
        alpha = np.asarray(
            Image.fromarray((alpha * 255).astype(np.uint8))
            .filter(ImageFilter.GaussianBlur(feather))
        ).astype(np.float32) / 255.0
    out = a * (1 - dim * alpha[..., None]) + 255.0 * (dim * alpha[..., None])
    return out.round().clip(0, 255).astype(np.uint8), alpha


def self_test():
    """三项：比例不变、焦点区不动、其余变淡。"""
    base = np.zeros((200, 300, 3), dtype=np.uint8)
    base[:, :] = (60, 90, 160)                      # 一片实色，方便看系数
    out, alpha = compose(base, (100, 100, 50, 100), dim=0.45, label_pad=40, feather=0)
    focus = out[120, 120].astype(int)
    far = out[10, 10].astype(int)
    cases = [
        ("焦点区保持原色", tuple(focus) == (60, 90, 160)),
        # 60*0.55 + 255*0.45 ≈ 147.75 → 147/148
        ("非焦点按 0.55 不透明度压暗", abs(far[0] - 148) <= 1),
        ("标签带被包进焦点矩形", alpha[70, 120] == 0),
    ]
    # 第四项守的是「两处常量漂移」：这里的 dim 是 1 − FOCUS_DIM_OPACITY 推出来的，
    # 若产品改了 0.55 而预览没改，看起来差不多的两张图其实对不上。
    # 找不到源文件时跳过（脚本可能被单独拷出去用），找到就必须相等。
    src = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                       "..", "..", "shared", "motion.ts")
    try:
        m = re.search(r"FOCUS_DIM_OPACITY\s*=\s*([0-9.]+)", open(src, encoding="utf-8").read())
        if m:
            cases.append(("dim 与 FOCUS_DIM_OPACITY 一致",
                          abs((1 - float(m.group(1))) - DEFAULT_DIM) < 1e-9))
        else:
            print("  skip  motion.ts 里没找到 FOCUS_DIM_OPACITY")
    except OSError:
        print("  skip  没有 motion.ts，常量对齐未验")

    bad = [n for n, ok in cases if not ok]
    for n, ok in cases:
        print(("  ok   " if ok else "  FAIL ") + n)
    print("\n%d/%d 通过" % (len(cases) - len(bad), len(cases)))
    return 0 if not bad else 1


def main():
    if "--self-test" in sys.argv:
        return self_test()
    ap = argparse.ArgumentParser()
    ap.add_argument("--png", required=True)
    ap.add_argument("--box", required=True, help="x,y,w,h（柱子包围盒，元素坐标系）")
    ap.add_argument("--label-pad", type=int, default=45,
                    help="矩形向上扩展的像素，用来把柱顶数据标签包进来")
    ap.add_argument("--dim", type=float, default=DEFAULT_DIM)
    ap.add_argument("--feather", type=int, default=8)
    ap.add_argument("--out", default="tmp/chart/mask-preview.png")
    args = ap.parse_args()

    base = np.asarray(Image.open(args.png).convert("RGB"))
    try:
        x, y, w, h = (int(v) for v in args.box.split(","))
    except ValueError:
        print("--box 要写成 x,y,w,h")
        return 2
    if w <= 0 or h <= 0:
        print("--box 的宽高必须为正")
        return 2
    out, _ = compose(base, (x, y, w, h), args.dim, args.label_pad, args.feather)
    Image.fromarray(out).save(args.out)
    print("遮罩预览：焦点 (%d,%d %dx%d)+向上 %dpx，dim=%.2f，羽化 %dpx → %s"
          % (x, y, w, h, args.label_pad, args.dim, args.feather, args.out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
