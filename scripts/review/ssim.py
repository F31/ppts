#!/usr/bin/env python3
"""SSIM 比对：量化「两张图有多像」，用于 DOM 动画与 MP4 帧的一致性门禁。

为什么不引 skimage：它为了这个 30 行的公式带进 scipy 与一大堆编译依赖，
而本仓库的门禁脚本只允许 numpy + Pillow（见 scripts/review/ 的既有约定）。
公式按 Wang et al. 2004 原式实现：11×11 高斯窗（σ=1.5）、K1=0.01、K2=0.03、L=255。

用法：
    python3 scripts/review/ssim.py a.png b.png
    python3 scripts/review/ssim.py a.png b.png --worst        # 另报最差窗口
    python3 scripts/review/ssim.py a.png b.png --grid 8       # 按 8×8 分块，指出哪块最差

退出码：>=0.95 → 0；否则 1（门禁阈值默认 0.95，可用 --min 覆盖）。
"""

import argparse
import sys

import numpy as np
from PIL import Image

WINDOW = 11
SIGMA = 1.5
C1 = (0.01 * 255) ** 2
C2 = (0.03 * 255) ** 2


def _gaussian_kernel(size: int, sigma: float) -> np.ndarray:
    coords = np.arange(size, dtype=np.float64) - (size - 1) / 2
    kernel = np.exp(-(coords**2) / (2 * sigma**2))
    return kernel / kernel.sum()


def _blur(img: np.ndarray, kernel: np.ndarray) -> np.ndarray:
    """可分离高斯模糊（边缘用 reflect 填充，避免边框被当作「差异」）。"""
    pad = len(kernel) // 2
    tmp = np.pad(img, ((0, 0), (pad, pad)), mode="reflect")
    tmp = np.apply_along_axis(lambda row: np.convolve(row, kernel, mode="valid"), 1, tmp)
    tmp = np.pad(tmp, ((pad, pad), (0, 0)), mode="reflect")
    return np.apply_along_axis(lambda col: np.convolve(col, kernel, mode="valid"), 0, tmp)


def _load_gray(path: str) -> np.ndarray:
    with Image.open(path) as im:
        return np.asarray(im.convert("L"), dtype=np.float64)


def ssim_map(a: np.ndarray, b: np.ndarray) -> np.ndarray:
    kernel = _gaussian_kernel(WINDOW, SIGMA)
    mu_a, mu_b = _blur(a, kernel), _blur(b, kernel)
    mu_a2, mu_b2 = mu_a * mu_a, mu_b * mu_b
    mu_ab = mu_a * mu_b
    sigma_a2 = _blur(a * a, kernel) - mu_a2
    sigma_b2 = _blur(b * b, kernel) - mu_b2
    sigma_ab = _blur(a * b, kernel) - mu_ab
    num = (2 * mu_ab + C1) * (2 * sigma_ab + C2)
    den = (mu_a2 + mu_b2 + C1) * (sigma_a2 + sigma_b2 + C2)
    return num / den


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("a")
    ap.add_argument("b")
    ap.add_argument("--min", type=float, default=0.95, help="门禁阈值（默认 0.95）")
    ap.add_argument("--worst", action="store_true", help="报告最差窗口的位置与数值")
    ap.add_argument("--grid", type=int, default=0, help="按 N×N 分块报告最差块")
    args = ap.parse_args()

    a, b = _load_gray(args.a), _load_gray(args.b)
    if a.shape != b.shape:
        print(f"尺寸不一致: {a.shape} vs {b.shape}", file=sys.stderr)
        return 2

    m = ssim_map(a, b)
    score = float(m.mean())
    print(f"ssim={score:.5f} min={float(m.min()):.4f} shape={a.shape[1]}x{a.shape[0]}")

    # 最差窗口：定位「哪里不像」。整片均值掩盖局部塌陷，位置信息是排查的入口。
    if args.worst:
        idx = np.unravel_index(int(np.argmin(m)), m.shape)
        print(f"worst_window y={idx[0]} x={idx[1]} ssim={float(m[idx]):.4f}")

    if args.grid > 1:
        h, w = m.shape
        gh, gw = h // args.grid, w // args.grid
        worst = (1.0, 0, 0)
        for gy in range(args.grid):
            for gx in range(args.grid):
                block = m[gy * gh : (gy + 1) * gh, gx * gw : (gx + 1) * gw]
                v = float(block.mean())
                if v < worst[0]:
                    worst = (v, gx, gy)
        print(f"worst_cell ssim={worst[0]:.4f} col={worst[1]} row={worst[2]} grid={args.grid}")

    ok = score >= args.min
    print("PASS" if ok else f"FAIL (<{args.min})")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
