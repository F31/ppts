#!/usr/bin/env python3
"""终态帧门禁（V13b）：成品包在每页结尾画出来的那一帧，必须等于源页。

为什么需要它，而且它与 V13（DOM vs MP4）不是一回事：
  * V13 比的是「包端与渲染端画得一样不一样」，需要真实成片（MP4 分段），
    也就是需要渲染镜像。它能发现两端的分歧，却跑不进 CI。
  * 本脚本比的是「包端有没有把这一页画对」——参照物是**源页图**，不需要成片。
    两端的插值都由 shared/motion.ts 承担，那是同一份代码；而「层图放在哪个框里、
    底板有没有把该露的地方露出来、画面有没有被缩放/裁切」只有包端单独会错。
  * 这两件事互补：V13 管「一致」，V13b 管「对不对」。一个包可以两端都一致地错。

判据（逐页取结尾前 250ms 的一帧，此时动画已收尾，画面就是该页的终态）：
  1. 落点准确：实际定位与请求相差 ≤ 50ms（滑杆是千分比，量化误差远小于此）。
  2. 舞台比例与页图一致，且尺寸 1:1 —— 见下面「为什么要求 1:1」。
  3. 逐页 SSIM ≥ 阈值（默认 0.99），且平均差、结构性差异像素占比都在阈值内。

为什么要求 1:1：
  一旦掺入重采样，「画错了」和「只是缩放了」就分不开——实测同一份正确包在
  1280 与 1920 下比对，SSIM 分别是 0.99995 与 0.969。把阈值放宽到 0.96 会把
  这两种情况混为一谈，而这种混同正是门禁最该避免的。所以本脚本把舞台定成
  **源页自己的尺寸**（dom-shot 的 --stage 去掉播放器 1280 的卡片上限，层位置
  本来就按画布百分比落位，去掉上限只是按原生尺寸再画一遍）。实测 1920 页在
  1920 舞台下与源页的 meanDiff 是 0.0007。

用法：
    python3 scripts/review/settled-gate.py --pkg tmp/pkg --pages tmp/pages
    python3 scripts/review/settled-gate.py --pkg tmp/pkg --pages tmp/pages --json

依赖：node 22+、一个 Chromium（PPT_CHROME 或常见安装路径）、numpy、Pillow。
退出码：0 全部通过；1 有页不通过；2 环境或输入有问题（找不到浏览器/页图/播放器）。
"""

import argparse
import json
import os
import sys

import numpy as np
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from domshot import run_dom_shot  # noqa: E402
from ssim import ssim_map  # noqa: E402

# 结尾取样点：页尾前 250ms。入场动画通常在前 1s 内结束，页尾前这段时间
# 一定是静止的——停在这里，取样对时钟误差不敏感（±100ms 都是同一帧）。
SETTLE_BACK_MS = 250
SEEK_TOLERANCE_MS = 50
# 阈值按「正确包实测值」留一个数量级余量：正确包是 SSIM 0.99994 / 平均差 0.002 /
# 结构性差异 0.000%，所以 0.995 / 0.5 / 0.05% 不是拍脑袋的宽松线，而是仍然能
# 抓住「图层框错 40px」（实测 0.52%）这种真实错位的紧线。
MIN_SSIM = 0.995
MAX_MEAN_DIFF = 0.5
MAX_OUTLIER_RATIO = 0.0005  # 差 >32 的像素占比


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pkg", required=True, help="成品包目录（含 index.html 与 manifest.json）")
    ap.add_argument("--pages", required=True, help="源页图目录（p000.png …），与清单按序号对齐")
    ap.add_argument("--out", default="", help="截图与报告输出目录（默认 pkg 同级 settled-gate）")
    ap.add_argument("--min-ssim", type=float, default=MIN_SSIM)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    manifest_path = os.path.join(args.pkg, "manifest.json")
    if not os.path.exists(manifest_path):
        print(f"包里没有 manifest.json：{manifest_path}", file=sys.stderr)
        return 2
    manifest = json.load(open(manifest_path, encoding="utf-8"))
    slides = manifest.get("slides") or []
    if not slides:
        print("清单没有页面", file=sys.stderr)
        return 2

    pages = []
    for i in range(len(slides)):
        name = os.path.join(args.pages, f"p{i:03d}.png")
        if not os.path.exists(name):
            print(f"缺少源页图 {name}", file=sys.stderr)
            return 2
        pages.append(name)

    out_dir = args.out or os.path.join(os.path.dirname(os.path.abspath(args.pkg)), "settled-gate")
    os.makedirs(out_dir, exist_ok=True)

    # 舞台尺寸取自源页本身，而不是命令行：判据要比的就是「这一页」，舞台跟着页走，
    # 就不存在「参数写错了一直比不过」这种自己给自己设的坑。
    with Image.open(pages[0]) as first:
        stage = (first.width, first.height)
    for i, name in enumerate(pages):
        with Image.open(name) as im:
            if (im.width, im.height) != stage:
                print(f"[V13b] 源页尺寸不一致：p{i:03d} 是 {im.width}×{im.height}，"
                      f"p000 是 {stage[0]}×{stage[1]}——没法定一套舞台", file=sys.stderr)
                return 2

    # 包的画布必须与页图同尺寸。舞台可以按任意尺寸画（--stage 会去掉卡片上限），
    # 但「包是 1920、页图是 1280」时它只能把 1920 缩到 1280——那一层重采样正是
    # 这条判据最该拒绝的东西，所以在这里挡住，而不是让它悄悄比过去。
    video = manifest.get("video") or {}
    pack = (int(video.get("width", 0)), int(video.get("height", 0)))
    if pack != stage:
        print(f"[V13b] 包与页图不是同一尺寸：包 {pack[0]}×{pack[1]}，"
              f"页图 {stage[0]}×{stage[1]}——缩放会把「画错了」和「只是缩放了」混为一谈",
              file=sys.stderr)
        return 2

    # 每页取一个点：页尾前 250ms；页面太短时退到页中点，避免落到上一页。
    cases = []
    for i, slide in enumerate(slides):
        start, end = int(slide["startUs"]) / 1000, int(slide["endUs"]) / 1000
        at = end - SETTLE_BACK_MS
        if at <= start:
            at = (start + end) / 2
        cases.append((i, int(round(at))))

    try:
        report = run_dom_shot(args.pkg, [c[1] for c in cases], out_dir, stage=stage)
    except (EnvironmentError, FileNotFoundError) as exc:
        print(f"[V13b] 无法执行浏览器截图：{exc}", file=sys.stderr)
        return 2

    shots = {s["requestedMs"]: s for s in report.get("shots", [])}
    checks = []
    rows = []
    for index, at_ms in cases:
        shot = shots.get(at_ms)
        if shot is None:
            checks.append({"code": "V13b", "page": index, "status": "fail", "detail": "没有取到这一帧"})
            continue

        drift = abs(float(shot.get("actualMs", -1e9)) - at_ms)
        if drift > SEEK_TOLERANCE_MS:
            checks.append({
                "code": "V13b", "page": index, "status": "fail",
                "detail": f"落点偏差 {drift:.0f}ms > {SEEK_TOLERANCE_MS}ms（定位失效，比对无意义）",
            })
            continue
        if not shot.get("ok"):
            checks.append({"code": "V13b", "page": index, "status": "fail",
                           "detail": "页面里还有未解码完的图（层图可能是空白）"})
            continue

        stage_box = shot.get("stage")
        if not stage_box:
            checks.append({"code": "V13b", "page": index, "status": "fail", "detail": "没有舞台元素"})
            continue

        page = Image.open(pages[index]).convert("RGB")
        if (page.width, page.height) != (stage_box["w"], stage_box["h"]):
            # 走到这里说明 dom-shot 的 --stage 没兑现。这不是「包画错了」，
            # 是截图管线的问题，按输入问题退出（2），不报一条不合格——
            # 后者会让人去查渲染，而问题根本不在画面上。
            print(
                f"[V13b] page {index}: 舞台 {stage_box['w']}×{stage_box['h']} 与源页 "
                f"{page.width}×{page.height} 不一致：--stage 没有按源页尺寸生效",
                file=sys.stderr,
            )
            return 2

        shot_img = Image.open(os.path.join(out_dir, f"t{at_ms}.png")).convert("RGB")
        crop = shot_img.crop((stage_box["x"], stage_box["y"],
                              stage_box["x"] + stage_box["w"], stage_box["y"] + stage_box["h"]))
        a = np.asarray(crop.convert("L"), dtype=np.float64)
        b = np.asarray(page.convert("L"), dtype=np.float64)
        diff = np.abs(np.asarray(crop, dtype=np.int16) - np.asarray(page, dtype=np.int16)).max(axis=2)
        score = float(ssim_map(a, b).mean())
        mean_diff = float(diff.mean())
        outliers = float((diff > 32).mean())

        rows.append((index, at_ms, score, mean_diff, outliers))
        bad = []
        if score < args.min_ssim:
            bad.append(f"SSIM {score:.5f} < {args.min_ssim}")
        if mean_diff > MAX_MEAN_DIFF:
            bad.append(f"平均差 {mean_diff:.3f} > {MAX_MEAN_DIFF}")
        if outliers > MAX_OUTLIER_RATIO:
            bad.append(f"差 >32 的像素 {outliers * 100:.3f}% > {MAX_OUTLIER_RATIO * 100:.3f}%")
        checks.append({
            "code": "V13b", "page": index, "status": "fail" if bad else "pass",
            "detail": "；".join(bad) if bad else f"SSIM {score:.5f}，平均差 {mean_diff:.3f}",
            "ssim": round(score, 6), "meanDiff": round(mean_diff, 4),
            "outliers": round(outliers, 6), "atMs": at_ms,
        })

    failed = [c for c in checks if c["status"] != "pass"]
    if args.json:
        print(json.dumps({"package": args.pkg, "browser": report.get("chrome"),
                          "checks": checks}, ensure_ascii=False, indent=2))
    else:
        print(f"成品包：{args.pkg}")
        print(f"浏览器：{report.get('chrome')}")
        print(f"舞台：{report['shots'][0]['stage']['w']}×{report['shots'][0]['stage']['h']}"
              if report.get("shots") else "")
        print()
        print(f"{'page':<6}{'atMs':>8}{'ssim':>11}{'meanDiff':>11}{'outlier%':>11}")
        for index, at_ms, score, mean_diff, outliers in rows:
            print(f"{index:<6}{at_ms:>8}{score:>11.5f}{mean_diff:>11.4f}{outliers * 100:>10.3f}%")
        print()
        if failed:
            for c in failed:
                print(f"FAIL [V13b] page {c.get('page')}: {c['detail']}")
        else:
            worst = min(r[2] for r in rows)
            print(f"PASS: {len(rows)} 页终态帧与源页一致（最差 SSIM {worst:.5f}）")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
