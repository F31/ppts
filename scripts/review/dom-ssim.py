#!/usr/bin/env python3
"""两端同帧 SSIM 门禁（M6-2）：比较「成品包的 DOM 截图」与「MP4 同帧解码图」。

为什么要这个脚本，而不是"打开看一眼"：
  * 只看末帧能轻松过 0.99——那时动画早已结束，两端都是静态页，这条门禁什么都没验。
    所以取样点必须包含**动画进行中**的时刻（第一段 clip 的中点）。
  * 只看全片平均会掩盖单页的失败：20 页里一页崩了，均值仍然很好看。
    所以判定用**逐页最差帧**。
  * 两端比较必须解码 MP4。编码本身有损，所以先要确认天花板够高
    （实测 CRF18/yuv420p 的编码损失 SSIM 在 0.9986 以上）。

用法：
    python3 scripts/review/dom-ssim.py \
        --plan tmp/5.5/job/plan.json \
        --pkg  tmp/m62/pkg \
        --segments tmp/5.5/out \
        --out  tmp/m62/gate

依赖：ffmpeg、docker（镜像需含 chrome-headless-shell）、numpy、Pillow。
退出码：全部达阈值 → 0；否则 1。
"""

import argparse
import json
import os
import shutil
import subprocess
import sys

import numpy as np
from PIL import Image

# 与 ssim.py 保持同一实现，避免两套口径。
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ssim import ssim_map  # noqa: E402

DEFAULT_CHROME = "/opt/chrome/chrome-headless-shell-linux64/chrome-headless-shell"


def page_starts(pages):
    """每页的绝对起点（毫秒）。页首尾相接，与 time 轴一致。"""
    starts, cursor = [], 0
    for page in pages:
        starts.append(cursor)
        cursor += int(page.get("durationMs", 0))
    return starts


def sample_points(page):
    """一页的三个取样时刻（页内毫秒）：首帧、第一段 clip 的中点、末帧前一帧。

    中点取「第一条带 props 的 clip」而不是「最长的那条」：第一条 clip 通常是
    标题入场，它是每页最早开始运动的元素，也是最容易被做错的一处
    （位置或透明度算错时，这里最明显）。
    """
    duration = int(page.get("durationMs", 0))
    points = [("first", 0)]
    first_clip = None
    for layer in page.get("layers") or []:
        for clip in layer.get("clips") or []:
            if not clip.get("props"):
                continue
            if first_clip is None or clip.get("startMs", 0) < first_clip.get("startMs", 0):
                first_clip = clip
    if first_clip:
        mid = int(first_clip.get("startMs", 0)) + int(first_clip.get("durationMs", 0)) // 2
        if 0 < mid < duration:
            points.append(("clip-mid", mid))
    if duration > 40:
        points.append(("last", duration - 34))  # 末帧前一帧，避免落到下一页
    return points


def grab_mp4_frame(segment, local_ms, dst):
    # Windows 上 ffmpeg 常常只装在 WSL 里；本机没有就走 WSL，并把路径一并转换。
    use_wsl = os.name == "nt" and shutil.which("ffmpeg") is None
    cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
           "-ss", f"{local_ms / 1000:.3f}", "-i",
           to_posix(segment) if use_wsl else segment,
           "-frames:v", "1",
           to_posix(dst) if use_wsl else dst]
    subprocess.run(wsl_wrap(cmd) if use_wsl else cmd, check=True)


def shoot_dom(pkg_dir, container_name, chrome, times_ms, out_dir):
    """一次容器内跑完全部截图：每次 docker run 都要 1s 上下，逐张起容器太慢。"""
    # -v 的宿主机路径必须是 Linux 侧路径：Windows 路径（E:\...）传给 WSL 里的
    # docker 会直接 exit 125（invalid mount path），且报错被吞掉时很难看出原因。
    shots_dir = to_posix(os.path.abspath(out_dir))
    pkg_abs = to_posix(os.path.abspath(pkg_dir))
    script = "; ".join(
        f"{chrome} --no-sandbox --disable-gpu --hide-scrollbars "
        f"--force-device-scale-factor=1 --window-size=1920,1080 "
        # 没有这个参数，截图会抢在光栅化完成之前：层数多的页（含转场时两个面板）
        # 会拍成黑屏——实测 page3 首帧亮度 8/255、SSIM 0.037，而 DOM 快照完全正常。
        # 这是截图管线的竞态，不是播放器的 bug。
        f"--run-all-compositor-stages-before-draw --virtual-time-budget=8000 "
        f'--screenshot=/shots/t{t}.png "file:///pkg/index.html?t={t}"'
        for t in times_ms
    )
    cmd = [
        "docker", "run", "--rm", "--entrypoint", "sh",
        "-v", f"{pkg_abs}:/pkg:ro", "-v", f"{shots_dir}:/shots",
        container_name, "-c", script,
    ]
    # WSL 下 docker 在 Linux 侧，路径要用 /mnt/...；Windows 侧直接调 docker 也能工作。
    subprocess.run(wsl_wrap(cmd), check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def to_posix(path):
    """Windows 路径 → WSL 内的 /mnt/e/... 路径；非 Windows 原样返回。"""
    if os.name != "nt":
        return path
    # 传进来的可能是相对路径（脚本参数就是相对的）；wslpath 只认绝对路径。
    out = subprocess.run(["wsl", "-e", "wslpath", "-a", "-u", os.path.abspath(path)],
                         check=True, capture_output=True, text=True)
    return out.stdout.strip()


def wsl_wrap(cmd):
    """Windows 上 docker 只存在于 WSL：把命令转成 bash -lc 执行的字符串。"""
    if os.name != "nt":
        return cmd
    inner = " ".join(shell_quote(part) for part in cmd)
    return ["wsl", "-e", "bash", "-lc", inner]


def shell_quote(s):
    return "'" + s.replace("'", "'\\''") + "'"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--plan", required=True, help="CompiledTimeline（plan.json）")
    ap.add_argument("--pkg", required=True, help="成品包目录（含 index.html）")
    ap.add_argument("--segments", required=True, help="MP4 分段目录（segment_000.mp4 ...）")
    ap.add_argument("--out", required=True, help="输出目录（截图、抽帧、报告）")
    ap.add_argument("--min", type=float, default=0.95, help="逐帧阈值（默认 0.95）")
    ap.add_argument("--image", default="ppts/renderer:dev", help="含 chrome 的镜像")
    ap.add_argument("--chrome", default=DEFAULT_CHROME)
    args = ap.parse_args()

    plan = json.load(open(args.plan, encoding="utf-8"))
    pages = plan.get("pages") or []
    if not pages:
        print("plan 没有页面", file=sys.stderr)
        return 2

    os.makedirs(args.out, exist_ok=True)
    starts = page_starts(pages)

    cases = []
    for i, page in enumerate(pages):
        for label, local_ms in sample_points(page):
            cases.append((i, label, local_ms, starts[i] + local_ms))

    shoot_dom(args.pkg, args.image, args.chrome, [c[3] for c in cases], args.out)

    rows, worst = [], (1.0, None)
    for index, label, local_ms, abs_ms in cases:
        segment = os.path.join(args.segments, f"segment_{index:03d}.mp4")
        if not os.path.exists(segment):
            print(f"缺少分段 {segment}", file=sys.stderr)
            return 2
        frame = os.path.join(args.out, f"mp4_p{index:03d}_{label}.png")
        shot = os.path.join(args.out, f"t{abs_ms}.png")
        grab_mp4_frame(segment, local_ms, frame)
        a = np.asarray(Image.open(frame).convert("L"), dtype=np.float64)
        b = np.asarray(Image.open(shot).convert("L"), dtype=np.float64)
        if a.shape != b.shape:
            print(f"尺寸不一致: {frame} {a.shape} vs {shot} {b.shape}", file=sys.stderr)
            return 2
        score = float(ssim_map(a, b).mean())
        rows.append((index, label, local_ms, score))
        if score < worst[0]:
            worst = (score, f"page={index} {label}@{local_ms}ms")

    print(f"{'page':<5}{'point':<10}{'local_ms':>9}{'ssim':>9}")
    for index, label, local_ms, score in rows:
        flag = "" if score >= args.min else "  <-- 未达标"
        print(f"{index:<5}{label:<10}{local_ms:>9}{score:>9.5f}{flag}")

    # 逐页最差：一页里任一取样点不达标就判该页失败，不拿别的页平均掉它。
    per_page = {}
    for index, _, _, score in rows:
        per_page[index] = min(per_page.get(index, 1.0), score)
    print()
    for index in sorted(per_page):
        print(f"page {index} worst = {per_page[index]:.5f}")

    bad = [i for i, v in per_page.items() if v < args.min]
    print(f"\n最差帧: {worst[1]} = {worst[0]:.5f}")
    if bad:
        print(f"FAIL: 页 {bad} 低于 {args.min}")
        return 1
    print(f"PASS: 所有页最差帧 ≥ {args.min}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
