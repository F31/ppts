#!/usr/bin/env python3
"""页内切图门禁（V18）：这一页真的换了图、在正确的时刻换、且没有重建图层。

为什么需要它，而不是靠 V13b：
  V13b 只看**末镜**那一帧。把「切镜」整个删掉、只留末镜那张图，V13b 照样过——
  它验的是「末镜画对了」，不是「中间换过」。而「一页多镜」这个功能存在的全部
  证据就是中间那次切换：`slide.image` 仍然是首镜图，删掉切换之后包完全合法。

判据（对每一页里每个 k>0 的镜头）：
  1. 切点前 1 帧：主图**就是**第 k-1 镜那张图，且不透明度 ≥0.95（不是半透明叠加）。
  2. 叠化中点（`crossFadeMs>0` 时）：两张图都在场，顶图的不透明度在 (0.05, 0.95)
     之间。硬切能过第 1、3 条，过不了这条——而 120ms 叠化是拍板的观感决定，
     不是实现细节，掉了必须红。
  3. 叠化结束后 1 帧：主图**就是**第 k 镜那张图。
  4. 三次取样之间，层节点的身份没有变（该页有层时才检）。重建会让元素重新入场，
     而 clips 是页内相对时间——重建一次，整页动画就重播一遍。
  5. 每个取样点上，层的可见性 == 「这一镜是不是特写」。`crop` 非空的镜头里层
     MUST NOT 显示（层的 box 是整页坐标，局部放大后必然错位）；整页镜里 MUST 显示。

「主图」= 当前面板里未 hidden、不透明度最高的那张图。只看第一张会答成「底层图」，
而在叠化期间底层永远是旧图——那个答案在切点前是对的，看起来毫无破绽。

为什么需要微秒定位：滑杆是 0–1000 的千分比，总时长 40 分钟的作业上一步就是
2.4 秒，而这里要取的是「切点前后各 33ms」。所以探针优先用播放器暴露的
`__PPT_SEEK_US__`；没有它时退回滑杆，并把落点偏差报出来——偏差超限即判失败，
因为「截错了时刻」和「切错了图」在报告里必须能分开。

用法：
    python3 scripts/review/shot-gate.py --pkg tmp/pkg
    python3 scripts/review/shot-gate.py --pkg tmp/pkg --json

依赖：node 22+、一个 Chromium（PPT_CHROME 或常见安装路径）。
退出码：0 全部通过；1 有判据不通过；2 环境或输入有问题（无浏览器/包里没有镜头）。
"""

import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from domshot import run_dom_shot  # noqa: E402

FRAME_MS = 33
# 落点容差。微秒钩子下实际是 0–1ms；给 5ms 是留给「等待解码期间音频时钟微动」。
# 超过它就说明定位退化成了滑杆，而滑杆的量化步长随时长变——不拦住的话，
# 「时间没取准」会被记成「图切错了」。
SEEK_TOLERANCE_MS = 5
SETTLED_OPACITY = 0.95


def base_name(path):
    """清单写 `slides/001.png`，探针报 `001.png`（src 的 basename）。

    两边都不该改：清单是包内路径（打包与校验都按它找文件），探针报 basename 是
    因为它读的是浏览器解析后的 src，写成绝对 URL 才是更糟的读数。所以在这里归一。
    """
    return str(path or "").replace("\\", "/").split("/")[-1]


def visible(shot):
    panel = shot.get("panel") or {}
    return [im for im in panel.get("images") or [] if not im.get("hidden") and im.get("src")]


def dominant(shot):
    imgs = visible(shot)
    if not imgs:
        return None
    return max(imgs, key=lambda im: float(im.get("opacity", 0) or 0))


def plan_cases(slides):
    """每页每个切点三个取样时刻，以及各自的期望。

    返回 (cases, skips)：cases 是 (页号, 镜号, label, 绝对毫秒, 期望)，
    skips 是 (页号, 镜号, 原因)——镜头太短、取不到干净窗口时如实记下来，
    而不是悄悄少验一条。
    """
    cases, skips = [], []
    for i, slide in enumerate(slides):
        shots = slide.get("shots") or []
        if len(shots) < 2:
            continue
        base = int(slide["startUs"]) / 1000.0
        page_end = int(slide["endUs"]) / 1000.0
        for k in range(1, len(shots)):
            shot, prev = shots[k], shots[k - 1]
            start = base + int(shot["startMs"])
            fade = int(shot.get("crossFadeMs") or 0)
            end = start + int(shot["durationMs"])
            # 切点前 1 帧，但不能落进上一镜自己的叠化窗口（那里两张图都在场）。
            prev_clean = base + int(prev["startMs"]) + int(prev.get("crossFadeMs") or 0) + FRAME_MS
            before = max(start - FRAME_MS, prev_clean)
            if before >= start:
                skips.append((i, k, f"上一镜只有 {int(prev['durationMs'])}ms，叠化后没有干净的一帧"))
                continue
            after = start + fade + FRAME_MS
            if after >= min(end, page_end):
                skips.append((i, k, f"这一镜只有 {int(shot['durationMs'])}ms，叠化后没有干净的一帧"))
                continue

            cases.append((i, k, "before", int(round(before)),
                          {"kind": "settled", "src": prev["image"]}))
            if fade > 0:
                cases.append((i, k, "fade", int(round(start + fade / 2.0)),
                              {"kind": "fade", "from": prev["image"], "to": shot["image"]}))
            cases.append((i, k, "after", int(round(after)),
                          {"kind": "settled", "src": shot["image"]}))
    return cases, skips


def check_case(page, k, label, expect, shot):
    """单点判定，返回 (ok, detail)。"""
    imgs = visible(shot)
    if not imgs:
        return False, "面板里一张可见的图都没有"

    if expect["kind"] == "settled":
        top = dominant(shot)
        opacity = float(top.get("opacity", 0) or 0)
        if base_name(top.get("src")) != base_name(expect["src"]):
            return False, f"主图是 {top.get('src') or '(无)'}，应为 {expect['src']}"
        if opacity < SETTLED_OPACITY:
            return False, f"主图不透明度只有 {opacity:.2f}：这一帧还在过渡里，不是稳定画面"
        return True, f"{top.get('src')}"

    # 叠化：底层是旧镜（不透明），顶层是新镜（半透明）。两张都得在。
    srcs = {base_name(im.get("src")) for im in imgs}
    want = {base_name(expect["from"]), base_name(expect["to"])}
    if srcs != want:
        return False, f"在场的图是 {sorted(srcs)}，应为 {sorted(want)}"
    # 不能按「不透明度最高」挑新图：叠化是「旧图保持不透明 + 新图按进度叠上来」，
    # 所以底层旧图的不透明度恒为 1，永远比新图高——按最高挑会答成旧图，
    # 而那个答案与「根本没淡入」看起来一模一样。
    fading_in = next((im for im in imgs
                      if base_name(im.get("src")) == base_name(expect["to"])), None)
    if fading_in is None:
        return False, f"在场但没有淡入中的 {expect['to']}"
    opacity = float(fading_in.get("opacity", 0) or 0)
    if not 0.05 < opacity < SETTLED_OPACITY:
        return False, f"叠化中点 {expect['to']} 的不透明度是 {opacity:.2f}：这不像是交叉淡入"
    return True, f"{expect['from']} → {expect['to']} @ {opacity:.2f}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pkg", required=True, help="成品包目录（含 index.html 与 manifest.json）")
    ap.add_argument("--out", default="", help="截图输出目录（默认 pkg 同级 shot-gate）")
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

    cases, skips = plan_cases(slides)
    if not cases:
        # 没有镜头的包不是「通过」，是「这条门禁对它不适用」。用 0 退出会让
        # CI 里的一次参数写错变成一次绿灯。
        print("[V18] 这个包里没有页内镜头，无从判断切图", file=sys.stderr)
        return 2

    out_dir = args.out or os.path.join(os.path.dirname(os.path.abspath(args.pkg)), "shot-gate")
    os.makedirs(out_dir, exist_ok=True)

    try:
        report = run_dom_shot(args.pkg, [c[3] for c in cases], out_dir, stage=None)
    except (EnvironmentError, FileNotFoundError) as exc:
        print(f"[V18] 无法执行浏览器截图：{exc}", file=sys.stderr)
        return 2

    shots = {s.get("requestedMs"): s for s in report.get("shots") or []}
    checks, rows = [], []
    for page, k, label, at_ms, expect in cases:
        shot = shots.get(at_ms)
        if shot is None:
            checks.append({"code": "V18", "page": page, "shot": k, "point": label,
                           "status": "fail", "detail": "没有取到这一帧"})
            continue
        drift = abs(float(shot.get("actualMs", -1e9)) - at_ms)
        if drift > SEEK_TOLERANCE_MS:
            checks.append({"code": "V18", "page": page, "shot": k, "point": label,
                           "status": "fail",
                           "detail": f"落点偏差 {drift:.0f}ms > {SEEK_TOLERANCE_MS}ms"
                                     f"（定位{'钩子' if shot.get('precise') else '退化成了滑杆'}）"})
            continue
        ok, detail = check_case(page, k, label, expect, shot)
        rows.append((page, k, label, at_ms, ok, detail))
        checks.append({"code": "V18", "page": page, "shot": k, "point": label,
                       "status": "pass" if ok else "fail",
                       "atMs": at_ms, "detail": detail})

    # 跨点的判据：同一页的几次取样之间，层节点必须还是同一批节点；且每个点上
    # 层的可见性必须与「这一镜是不是特写」一致。
    by_page = {}
    for page, k, label, at_ms, expect in cases:
        by_page.setdefault(page, []).append((k, label, at_ms))
    for page, points in by_page.items():
        slide = slides[page]
        if not (slide.get("layers") or []):
            continue
        shots_of_page = slide.get("shots") or []
        seen = []
        for k, label, at_ms in points:
            panel = (shots.get(at_ms) or {}).get("panel") or {}
            seen.append((k, label, panel.get("layerIds", ""), panel.get("layersVisible")))

        base = seen[0][2]
        for k, label, got, _vis in seen:
            if got != base:
                checks.append({"code": "V18", "page": page, "shot": k, "point": label,
                               "status": "fail",
                               "detail": f"层节点被重建了（{base!r} → {got!r}）："
                                         "切镜只该换图，不该让元素重新入场"})
                break

        for k, label, _got, vis in seen:
            # before 那一帧显示的是上一镜，可见性按上一镜的 crop 判。
            which = k - 1 if label == "before" else k
            if which >= len(shots_of_page):
                continue
            crop = bool(shots_of_page[which].get("crop"))
            if bool(vis) == crop:
                checks.append({
                    "code": "V18", "page": page, "shot": k, "point": label, "status": "fail",
                    "detail": ("特写镜里层仍然显示：层的 box 是整页坐标，局部放大后必然错位"
                               if crop else
                               "整页镜里层被藏起来了：这一镜应当照常显示元素动画"),
                })

    for page, k, detail in skips:
        checks.append({"code": "V18", "page": page, "shot": k, "point": "-",
                       "status": "skip", "detail": detail})

    failed = [c for c in checks if c["status"] == "fail"]
    skipped = [c for c in checks if c["status"] == "skip"]

    if args.json:
        print(json.dumps({"package": args.pkg, "browser": report.get("chrome"),
                          "precise": (report.get("shots") or [{}])[0].get("precise"),
                          "checks": checks}, ensure_ascii=False, indent=2))
    else:
        print(f"成品包：{args.pkg}")
        print(f"浏览器：{report.get('chrome')}")
        first = (report.get("shots") or [{}])[0]
        print(f"定位：{'微秒钩子' if first.get('precise') else '滑杆（千分比，精度有限）'}")
        print()
        print(f"{'page':<6}{'shot':>5}{'point':>8}{'atMs':>8}  result")
        for page, k, label, at_ms, ok, detail in rows:
            print(f"{page:<6}{k:>5}{label:>8}{at_ms:>8}  {'ok' if ok else 'FAIL'}  {detail}")
        print()
        for c in skipped:
            print(f"SKIP [V18] page {c['page']} shot {c['shot']}: {c['detail']}")
        if failed:
            for c in failed:
                print(f"FAIL [V18] page {c['page']} shot {c['shot']} {c['point']}: {c['detail']}")
            return 1
        n = len({(c["page"], c["shot"]) for c in checks if c["status"] != "fail"})
        print(f"PASS: {n} 个切点在正确时刻发生了切图"
              + (f"，{len(skipped)} 处跳过" if skipped else ""))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
