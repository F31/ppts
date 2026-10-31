#!/usr/bin/env python3
"""两端同帧 SSIM 门禁（M6-2 / V13）：比较「成品包的 DOM 截图」与「MP4 同帧解码图」。

为什么要这个脚本，而不是"打开看一眼"：
  * 只看末帧能轻松过 0.99——那时动画早已结束，两端都是静态页，这条门禁什么都没验。
    所以取样点必须包含**动画进行中**的时刻（第一段 clip 的中点）。
  * 页内有镜头切片时，还必须取**切点之后**的时刻：两端最容易分叉的地方正是
    「这一页什么时候换图」，而它只在切点附近的一两帧上表现出来。
  * 只看全片平均会掩盖单页的失败：20 页里一页崩了，均值仍然很好看。
    所以判定用**逐页最差帧**。
  * 两端比较必须解码 MP4。编码本身有损，所以先要确认天花板够高
    （实测 CRF18/yuv420p 的编码损失 SSIM 在 0.9986 以上）。

它与 V13b（settled-gate.py）的分工：
  * V13（本脚本）比「包端与渲染端画得一样不一样」，需要真实成片，能发现两端分歧；
  * V13b 比「包端有没有把这一页画对」，参照物是源页图，不需要成片。
  一个包可以两端都一致地错——所以两条都要有。

截图半边用 `dom-shot.mjs`（与 V13b 同一份实现）：按 seek 落点、等图解码完、摘掉
播放器外壳，然后按 `--stage` 把舞台定成页图尺寸取那一块。以前这半边走 docker
里的 chrome-headless-shell，而 docker 只存在于 WSL、镜像要另外构建——一个「比两张
图」的门禁因此长期跑不起来（本地没有成片也就无从验证）。现在它只需要一个 Chromium。

用法：
    python3 scripts/review/dom-ssim.py \
        --plan tmp/5.5/job/plan.json \
        --pkg  tmp/m62/pkg \
        --segments tmp/5.5/out \
        --out  tmp/m62/gate

依赖：ffmpeg、node 22+、一个 Chromium（PPT_CHROME 或常见安装路径）、numpy、Pillow。
退出码：全部达阈值 → 0；有页不达标 → 1；环境或输入有问题 → 2。
"""

import argparse
import json
import os
import shutil
import subprocess
import sys

import numpy as np
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from domshot import run_dom_shot  # noqa: E402
from ssim import ssim_map  # noqa: E402


def page_starts(pages):
    """每页的绝对起点（毫秒）。页首尾相接，与 time 轴一致。"""
    starts, cursor = [], 0
    for page in pages:
        starts.append(cursor)
        cursor += int(page.get("durationMs", 0))
    return starts


def sample_points(page, fps):
    """一页的取样时刻（页内毫秒）：首帧、第一段 clip 的中点、每个切点之后、末帧前一帧。

    中点取「第一条带 props 的 clip」而不是「最长的那条」：第一条 clip 通常是
    标题入场，它是每页最早开始运动的元素，也是最容易被做错的一处
    （位置或透明度算错时，这里最明显）。

    切点取「startMs + crossFadeMs + 1 帧」而不是切点本身：混合窗口只有 120ms，
    两端时钟差 20ms 就会让同一时刻的颜色不同——那个差会被记成「两端不一致」，
    其实只是取样时刻不同。混合**期间**的一致性由 V18 单独验（两侧各取一帧），
    这里只验「切完之后两端画的是不是同一张」。
    """
    duration = int(page.get("durationMs", 0))
    frame = max(1, int(round(1000.0 / max(int(fps or 30), 1))))
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
    for shot in page.get("shots") or []:
        k = int(shot.get("index", 0))
        if k == 0:
            continue
        at = int(shot.get("startMs", 0)) + int(shot.get("crossFadeMs", 0)) + frame
        if 0 < at < duration:
            points.append((f"shot{k}", at))
    if duration > 40:
        points.append(("last", duration - 34))  # 末帧前一帧，避免落到下一页

    # 同一毫秒只取一次：首帧与某个切点重合时，两次截图会写同一个文件、算两遍。
    seen, unique = set(), []
    for label, ms in points:
        if ms in seen:
            continue
        seen.add(ms)
        unique.append((label, ms))
    return unique


def grab_mp4_frame(segment, local_ms, dst):
    # Windows 上 ffmpeg 常常只装在 WSL 里；本机没有就走 WSL，并把路径一并转换。
    use_wsl = os.name == "nt" and shutil.which("ffmpeg") is None
    cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
           "-ss", f"{local_ms / 1000:.3f}", "-i",
           to_posix(segment) if use_wsl else segment,
           "-frames:v", "1",
           to_posix(dst) if use_wsl else dst]
    subprocess.run(wsl_wrap(cmd) if use_wsl else cmd, check=True)


def to_posix(path):
    """Windows 路径 → WSL 内的 /mnt/e/... 路径；非 Windows 原样返回。"""
    if os.name != "nt":
        return path
    # 传进来的可能是相对路径（脚本参数就是相对的）；wslpath 只认绝对路径。
    out = subprocess.run(["wsl", "-e", "wslpath", "-a", "-u", os.path.abspath(path)],
                         check=True, capture_output=True, text=True)
    return out.stdout.strip()


def wsl_wrap(cmd):
    """Windows 上 ffmpeg 可能只在 WSL 里：把命令转成 bash -lc 执行的字符串。"""
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
    args = ap.parse_args()

    plan = json.load(open(args.plan, encoding="utf-8"))
    pages = plan.get("pages") or []
    if not pages:
        print("plan 没有页面", file=sys.stderr)
        return 2

    manifest_path = os.path.join(args.pkg, "manifest.json")
    if not os.path.exists(manifest_path):
        print(f"包里没有 manifest.json：{manifest_path}", file=sys.stderr)
        return 2
    video = json.load(open(manifest_path, encoding="utf-8")).get("video") or {}
    stage = (int(video.get("width", 0)), int(video.get("height", 0)))
    if stage[0] <= 0 or stage[1] <= 0:
        print("清单没有画布尺寸（video.width/height）", file=sys.stderr)
        return 2
    # 时间轴与包必须是同一块画布，否则「同一时刻」本身就没有意义：
    # 页码、时长、尺寸对不上时，比出来的分数只反映两个不同的东西。
    for key, want in (("width", stage[0]), ("height", stage[1])):
        got = plan.get(key)
        if got is not None and int(got) != want:
            print(f"时间轴 {key}={got} 与包的 {want} 不一致：不是同一份作业", file=sys.stderr)
            return 2

    os.makedirs(args.out, exist_ok=True)
    starts = page_starts(pages)

    fps = int(plan.get("fps") or 30)
    cases = []
    for i, page in enumerate(pages):
        for label, local_ms in sample_points(page, fps):
            cases.append((i, label, local_ms, starts[i] + local_ms))

    # 先把材料点齐再开浏览器：缺一段成片是几句话就能说清的输入问题，
    # 不该先花几十秒截图、再在循环里才报出来。
    for i in range(len(pages)):
        segment = os.path.join(args.segments, f"segment_{i:03d}.mp4")
        if not os.path.exists(segment):
            print(f"缺少分段 {segment}", file=sys.stderr)
            return 2

    try:
        report = run_dom_shot(args.pkg, [c[3] for c in cases], args.out, stage=stage)
    except (EnvironmentError, FileNotFoundError) as exc:
        print(f"[V13] 无法执行浏览器截图：{exc}", file=sys.stderr)
        return 2
    shots = {s["requestedMs"]: s for s in report.get("shots", [])}

    rows, worst = [], (1.0, None)
    for index, label, local_ms, abs_ms in cases:
        shot = shots.get(abs_ms)
        if shot is None:
            print(f"没有取到 {abs_ms}ms 的截图", file=sys.stderr)
            return 2
        if not shot.get("stage") or not shot.get("ok"):
            print(f"{abs_ms}ms 的截图不可用：舞台={shot.get('stage')} ok={shot.get('ok')}",
                  file=sys.stderr)
            return 2
        segment = os.path.join(args.segments, f"segment_{index:03d}.mp4")

        frame = os.path.join(args.out, f"mp4_p{index:03d}_{label}.png")
        grab_mp4_frame(segment, local_ms, frame)
        a = np.asarray(Image.open(frame).convert("L"), dtype=np.float64)
        shot_img = Image.open(os.path.join(args.out, f"t{abs_ms}.png")).convert("L")
        box = shot["stage"]
        b = np.asarray(shot_img.crop((box["x"], box["y"],
                                      box["x"] + box["w"], box["y"] + box["h"])), dtype=np.float64)
        if a.shape != b.shape:
            # 成片与包不是同一尺寸：这不是「画错了」，是两份材料对不上。
            # 按输入问题退出（2），否则下一步只能靠缩放糊过去——而缩放正是
            # 这条门禁最该拒绝的东西。
            print(f"尺寸不一致：成片 {a.shape[1]}×{a.shape[0]} vs 舞台 "
                  f"{b.shape[1]}×{b.shape[0]}（{frame}）", file=sys.stderr)
            return 2
        score = float(ssim_map(a, b).mean())
        rows.append((index, label, local_ms, score))
        if score < worst[0]:
            worst = (score, f"page={index} {label}@{local_ms}ms")

    print(f"成品包：{args.pkg}")
    print(f"浏览器：{report.get('chrome')}")
    print(f"舞台：{stage[0]}×{stage[1]}")
    print()
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
