#!/usr/bin/env python3
"""单帧核对：把 still.mjs 渲染出的某一帧与源页逐像素比对。

为什么需要它：`npm run studio` 是交互式界面，自动化环境里既打不开也没法人眼看。
但 Studio 预览的画面就是 `src/Timeline.tsx` 这个 composition 在某一帧的输出——
同一份代码、同一个 Chrome、同一套 props。所以「分辨率与颜色对不对」可以换成
更强的问法：**这一帧是否与源页逐像素一致，动效进行中的差异是否只落在该动效的
元素框内**。人眼能看出的色偏是几个色阶，逐像素能看出 1 个色阶。

两种模式：

  --settled            动效已结束的帧，应当与源页几乎相同（只容忍抗锯齿级噪声）
  --confined-to x,y,w,h  动效进行中的帧，与源页的差异必须全部落在该元素框内
                       （框外有差异 = 整页缩放/位移/裁切，正是人眼最容易忽略的错）

    python3 scripts/review/still-check.py --still f.png --page pages/p000.png --settled
    python3 scripts/review/still-check.py --still f10.png --page pages/p000.png \\
        --confined-to 116,288,1320,130

退出码：0 通过，1 不通过，2 输入有问题。
"""

from __future__ import annotations

import argparse
import sys

import numpy as np
from PIL import Image

# 阈值与 scripts/fidelity/compare.py 的判据同源：PNG 没有编码噪声，
# 所以这里收得比 H.264 那套（平均差 ≤2.0 / >32 像素 <1%）更紧。
SETTLED_MEAN_DIFF = 1.0
SETTLED_OUTLIER_RATIO = 0.005  # 差 >32 的像素占比
BOX_SLOP = 8  # 抗锯齿与投影会溢出元素框几个像素


def load(path: str) -> np.ndarray:
    with Image.open(path) as im:
        return np.asarray(im.convert("RGB"), dtype=np.float32)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--still", required=True, help="still.mjs 渲染出的 PNG")
    ap.add_argument("--page", required=True, help="同一时刻应当对应的源页 PNG")
    ap.add_argument("--settled", action="store_true", help="动效已结束：要求与源页几乎一致")
    ap.add_argument("--confined-to", metavar="X,Y,W,H", help="差异必须落在这个元素框内")
    args = ap.parse_args()

    still = load(args.still)
    page = load(args.page)
    if still.shape != page.shape:
        print(f"FAIL 分辨率不一致：still {still.shape[1]}x{still.shape[0]}，"
              f"源页 {page.shape[1]}x{page.shape[0]}（合成器缩放错了？）")
        return 1

    diff = np.abs(still - page).max(axis=2)
    mean_diff = float(np.abs(still - page).mean())
    outliers = float((diff > 32).mean())
    print(f"size={still.shape[1]}x{still.shape[0]} meanDiff={mean_diff:.3f} "
          f"outliers={outliers * 100:.3f}%")

    ys, xs = np.where(diff > 32)
    if xs.size:
        print(f"diff bbox: x[{xs.min()},{xs.max()}] y[{ys.min()},{ys.max()}] "
              f"pixels={xs.size}")
    else:
        print("diff bbox: 无（逐像素相同）")

    failed = False
    if args.settled:
        if mean_diff > SETTLED_MEAN_DIFF:
            print(f"FAIL settled 帧平均差 {mean_diff:.3f} > {SETTLED_MEAN_DIFF}"
                  "（色偏或内容缺失）")
            failed = True
        if outliers > SETTLED_OUTLIER_RATIO:
            print(f"FAIL settled 帧差 >32 的像素 {outliers * 100:.3f}% > "
                  f"{SETTLED_OUTLIER_RATIO * 100:.1f}%（结构性差异）")
            failed = True

    if args.confined_to:
        try:
            x, y, w, h = (int(v) for v in args.confined_to.split(","))
        except ValueError:
            print("--confined-to 需要 X,Y,W,H 四个整数")
            return 2
        x0, y0 = x - BOX_SLOP, y - BOX_SLOP
        x1, y1 = x + w + BOX_SLOP, y + h + BOX_SLOP
        if xs.size:
            outside = ((xs < x0) | (xs > x1) | (ys < y0) | (ys > y1)).sum()
            if outside:
                print(f"FAIL 有 {outside} 个差异像素落在元素框 "
                      f"[{x0},{y0},{x1},{y1}] 之外（整页位移/缩放/裁切）")
                failed = True
            else:
                print(f"OK 差异全部落在元素框 [{x0},{y0},{x1},{y1}] 内（局部动效）")

    if failed:
        return 1
    if not (args.settled or args.confined_to):
        print("（未指定判定模式，仅报告指标）")
    else:
        print("PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
