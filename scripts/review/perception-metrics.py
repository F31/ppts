#!/usr/bin/env python3
"""观感度量：把设计方案 §19.6 里「能机器判」的几项算出来，剩下的才交给人。

为什么需要它：`tasks.md` 5.5 是人工盲评，但人眼一旦被客观问题干扰（元素堆太满、
每秒动效过密），打出来的分就不可比。先把客观项过一遍，人工只对「像不像参考片」负责。

能算的（来自 `plan.json`，与渲染实现无关）：

| 度量 | §19.6 阈值 | 依据 |
|---|---|---|
| 每页主要元素数 | 7 ± 2（上界 9） | 每个 layer = 一个可动画元素 |
| 节奏密度 | clip 数/秒 ≤ 3 | 一秒内起效的 clip 数，按页均摊 |
| 单页 clip 总量 | — | 仅记录，用来解释密度 |
| clip 时长区间 | §19.4 速度分层 | 直接透传编译器的 `duration_tier_conflict` 告警 |

算不了的（不要在这里假装算得了）：

- **正文最小字号 / 文本对比度**：`plan.json` 里只有 box，没有字号与颜色。字号属于底图
  （`scripts/fidelity/gen_pages.py` 的画布常量），对比度属于设计令牌。要度量它们得读底图
  像素或令牌表，不是读计划——换一套底图，同一个计划会得出不同结论。
- **与参考片像不像**：这是 5.5 人工盲评的对象，任何脚本都给不出。

用法：

    python3 scripts/review/perception-metrics.py tmp/5.5/job/plan.json
    python3 scripts/review/perception-metrics.py plan.json --json   # 机器可读

任一阈值越界时退出码为 1，可直接串进 CI 或出片脚本。
"""
import argparse
import json
import sys

# §19.6：每页主要元素数 7 ± 2
ELEMENTS_CENTER, ELEMENTS_TOL = 7, 2
# §19.6：节奏密度 clip 数/秒 ≤ 3
MAX_CLIPS_PER_SEC = 3.0


def page_metrics(page):
    """单页度量。page 是 plan.json 的 pages[i]，字段见 internal/render 的契约。"""
    duration_ms = page.get("durationMs") or 0
    layers = page.get("layers") or []
    clips = [c for layer in layers for c in (layer.get("clips") or [])]
    seconds = duration_ms / 1000.0 if duration_ms > 0 else 0.0
    starts = sorted({int(c.get("startMs", 0)) for c in clips})
    durations = [int(c.get("durationMs", 0)) for c in clips if c.get("durationMs")]

    return {
        "index": page.get("index"),
        "durationS": round(seconds, 3),
        "elements": len(layers),
        "clips": len(clips),
        "clipsPerSec": round(len(clips) / seconds, 2) if seconds else 0.0,
        "startsMs": starts,
        "minClipMs": min(durations) if durations else 0,
        "maxClipMs": max(durations) if durations else 0,
    }


def evaluate(m):
    """按 §19.6 判每一项。返回 (问题列表)。空列表表示全部在阈值内。"""
    problems = []
    hi = ELEMENTS_CENTER + ELEMENTS_TOL
    if m["elements"] > hi:
        problems.append(
            f"页面 {m['index']}：{m['elements']} 个元素 > {hi}（§19.6 建议拆镜）"
        )
    if m["clipsPerSec"] > MAX_CLIPS_PER_SEC:
        problems.append(
            f"页面 {m['index']}：{m['clipsPerSec']} clip/秒 > {MAX_CLIPS_PER_SEC}"
            "（§19.6 节奏密度，容易产生「每个元素都在动」的廉价感）"
        )
    return problems


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("plan", help="plan.json 路径（脚本 compile 的产物）")
    ap.add_argument("--json", action="store_true", help="输出 JSON 而不是表格")
    args = ap.parse_args()

    with open(args.plan, encoding="utf-8") as f:
        plan = json.load(f)

    metrics = [page_metrics(p) for p in plan.get("pages") or []]
    problems = []
    for m in metrics:
        problems.extend(evaluate(m))

    warnings = plan.get("warnings") or []
    tier = [w for w in warnings if w.get("code") == "duration_tier_conflict"]

    if args.json:
        print(json.dumps(
            {"planHash": plan.get("planHash"), "pages": metrics,
             "problems": problems, "warnings": warnings},
            ensure_ascii=False, indent=2))
    else:
        print(f"{'页':<3}{'元素':>5}{'clip':>6}{'clip/秒':>9}{'时长s':>8}"
              f"{'clip区间ms':>15}  起播点ms")
        for m in metrics:
            print(f"{m['index']:<3}{m['elements']:>5}{m['clips']:>6}"
                  f"{m['clipsPerSec']:>9}{m['durationS']:>8}"
                  f"{str(m['minClipMs']) + '–' + str(m['maxClipMs']):>15}  "
                  f"{m['startsMs']}")

        print()
        if tier:
            # 透传而不是重算：速度分层是编译器的判定，这里再实现一遍必然漂移
            print(f"§19.4 速度分层告警 {len(tier)} 条（编译期已报，此处仅透传）：")
            for w in tier:
                print(f"  - 页面 {w.get('page')}：{w.get('message')}")
        if problems:
            print("§19.6 阈值越界：")
            for p in problems:
                print(f"  - {p}")
        else:
            print("§19.6 可机器判的项全部在阈值内"
                  "（字号/对比度需读底图，不在此脚本范围）。")

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
