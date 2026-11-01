#!/usr/bin/env python3
"""Q7 遗留问题 6.1 的可算部分：把「0.972 是否自证」拆成结构词消融。

## 这个脚本在回答什么

6.1 要的是「一份真人讲稿」，回答 0.972 有没有自证成分。真人讲稿拿不到时，
这个问题可以先拆成能算的一半：

    0.972 里，多少来自字面相似，多少来自讲稿里顺着版面说的结构词？

写 q8 讲稿的人**看着 PPT 写**（虽然 golden 是逐条指定、写时没跑绑定器），
所以讲稿天然会带上「第二部分」「五个方面」「右边那张图」这类结构词——而 v2 的
计数 / 序数 / 方位三条规则吃的正是这些词。真人自由讲则更松：顺序大体还是那个顺序，
但不会替你把编号念出来。

于是做法是：把结构词从讲稿里**删掉**（golden 一字不动），重跑绑定，看掉多少。
掉的部分就是「自证成分」的上界——不是精确值，因为真人不是只少了这几个词，
但它是**可复现、可归因**的量，比「等一份真人讲稿」有用。

## 三组扰动各自打掉什么

| 扰动 | 正则 | 打掉的规则 |
|---|---|---|
| 去序数 | `第N步/条/个/点/项/部分` | `ordinal`（11 条） |
| 去数量词 | `五个/四条/三项` | `count`（10 条） |
| 数量词改「几个」 | `五个` → `几个` | `count`，但句子仍通顺（更接近真人） |
| 去空间指代 | `左边/右侧` + `这张表/看图/图里/中间` | `spatial`（1 条，走的是图表指代分支） |

全部去掉后再跑一档 `--rules base`：此时讲稿只剩「字面 + 顺序」，
它应该落到口语版合成样本那一档（0.400–0.533）。落不到，说明扰动没打准；
落过了头，说明删得太狠（删出空串或语法碎片）。这条是**扰动的自检**，
不是结论。

## 用法

    python3 scripts/fidelity/ablate_structure.py \
        --segments scripts/fidelity/q8-script.json \
        --elements tmp/q8/fid/elements.json \
        --golden scripts/fidelity/q8-script.json \
        --out-dir tmp/q8/ablate

产物（segments 变体 + 各组报告）写在 `--out-dir`，不入库，可随时重跑。
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

# 与 bind_elements.py 里三条规则各自吃的正则**逐字对齐**。
# 不共用常量是有意的：消融脚本必须独立于被测实现，否则改了实现悄悄改了尺子。
PATTERNS = {
    "ordinal": re.compile(r"第[一二三四五六七八九十\d]{1,3}\s*(?:步|条|个|点|项|部分|块|类)"),
    "count": re.compile(
        r"(?<!第)[一二两三四五六七八九十\d]{1,3}\s*(?:步|个|条|项|块|部分|段|层|方面|件事|类)"
    ),
    # 空间指代不止「左边/右侧」：`spatial` 规则同样吃「这张表 / 看图 / 图里」这类
    # 图表指代（TABLE_REF / IMAGE_REF）和「中间」。只删方位后缀会漏掉它们——
    # 第一版就是这样：删了 0 处，误以为方位规则不依赖讲稿。
    "spatial": re.compile(
        r"(?:左|右|上|下)(?:边|侧|方|角|半)"
        r"|这张表|那张表|表格|表里|下表|上表|这张图|那张图|图里|看图|截图"
        r"|示意图|架构图|流程图|照片|画面|右边是|左侧是|中间|正中|中央"
    ),
}

# 数量词换成「几个」：真人不说「五个部分」时说「几个部分」，句子照样通顺，
# 但精确数量消失了。这比整句删掉更接近真实讲稿，也是本实验里信息量最大的一档。
VAGUE_COUNT = re.compile(
    r"(?<!第)([一二两三四五六七八九十\d]{1,3})\s*(步|个|条|项|块|部分|段|层|方面|件事|类)"
)

VARIANTS = [
    ("baseline", "原始讲稿", ()),
    ("no-ordinal", "去序数", ("ordinal",)),
    ("no-count", "去数量词", ("count",)),
    ("count-vague", "数量词改「几个」（真人说法）", ("count-vague",)),
    ("no-spatial", "去空间指代", ("spatial",)),
    ("no-structure", "去全部结构词", ("ordinal", "count", "spatial")),
]


def ablate(text: str, kinds) -> tuple[str, int]:
    """删掉命中的结构词，返回（新文本, 命中次数）。"""
    hits = 0
    out = text
    for kind in kinds:
        if kind == "count-vague":
            out, n = VAGUE_COUNT.subn(r"几\2", out)
        else:
            out, n = PATTERNS[kind].subn("", out)
        hits += n
    # 删词会留下连续空格与悬空的标点，压一下；不补词，避免引入新的字面信号。
    out = re.sub(r"\s{2,}", " ", out)
    out = re.sub(r"^[\s，,、]+", "", out)
    return out.strip(), hits


def build_variant(pages, kinds):
    """只改 segment.text，id / bind / 页序一律不动——golden 与段落身份必须稳定。"""
    out = []
    total_hits = 0
    for page in pages:
        segs = []
        for seg in page.get("segments") or []:
            text = seg.get("text") or ""
            new_text, hits = ablate(text, kinds)
            total_hits += hits
            segs.append({**seg, "text": new_text})
        out.append({**page, "segments": segs})
    return {"pages": out}, total_hits


def run_bind(script, segments_path, elements, golden, rules, out_path):
    cmd = [
        sys.executable,
        str(script),
        "--rules", rules,
        "--segments", str(segments_path),
        "--elements", str(elements),
        "--golden", str(golden),
        "--out", str(out_path),
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8")
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout + proc.stderr)
        raise SystemExit(f"绑定失败：{out_path}")
    with open(out_path, encoding="utf-8") as f:
        return json.load(f)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--segments", default="scripts/fidelity/q8-script.json")
    ap.add_argument("--elements", default="tmp/q8/fid/elements.json")
    ap.add_argument("--golden", default="scripts/fidelity/q8-script.json")
    ap.add_argument("--out-dir", default="tmp/q8/ablate")
    args = ap.parse_args()

    here = Path(__file__).resolve().parent
    bind_script = here / "bind_elements.py"
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    with open(args.segments, encoding="utf-8") as f:
        source = json.load(f)
    pages = source["pages"]

    rows = []
    for key, label, kinds in VARIANTS:
        variant, hits = build_variant(pages, kinds)
        seg_path = out_dir / f"segments-{key}.json"
        with open(seg_path, "w", encoding="utf-8") as f:
            json.dump(variant, f, ensure_ascii=False, indent=1)

        shortest = min(
            (len((s.get("text") or "").strip())
             for p in variant["pages"] for s in p["segments"]),
            default=0,
        )
        for rules in ("v2", "base") if key == "no-structure" else ("v2",):
            report = run_bind(
                bind_script, seg_path, args.elements, args.golden, rules,
                out_dir / f"report-{key}-{rules}.json",
            )
            overall = report["overall"]
            rows.append({
                "variant": key,
                "label": label,
                "rules": rules,
                "hits": hits,
                "shortest": shortest,
                "recall_content": overall["recall_content"],
                "recall": overall["recall"],
                "precision": overall["precision"],
                "exact": overall["exact"],
                "missed": overall["missed"],
                "wrong_seg": overall["wrong_seg"],
                "by_rule": overall["by_rule"],
            })

    # 人读的表
    print(f"\n=== 结构词消融（{Path(args.segments).name}）===")
    print("| 变体 | 规则 | 删词处 | 召回(内容) | 召回(全部) | 精确 | 绑对 | 漏绑 | 绑错段 | 来源分布 |")
    print("|---|---|---|---|---|---|---|---|---|")
    for r in rows:
        dist = " / ".join(
            f"{k}{v}" for k, v in r["by_rule"].items() if v
        )
        print(f"| {r['label']} | {r['rules']} | {r['hits']} | "
              f"{r['recall_content']:.3f} | {r['recall']:.3f} | {r['precision']:.3f} | "
              f"{r['exact']} | {r['missed']} | {r['wrong_seg']} | {dist} |")

    base = next(r for r in rows if r["variant"] == "baseline")
    full = next(r for r in rows if r["variant"] == "no-structure" and r["rules"] == "v2")
    drop = base["recall_content"] - full["recall_content"]
    print(f"\n结构词贡献（内容召回）：{base['recall_content']:.3f} → "
          f"{full['recall_content']:.3f}，掉 {drop:.3f}")
    print(f"最短段长度（去全部结构词后）：{full['shortest']} 字符"
          f"{'  ← 疑似删出碎片，看下面的段文本' if full['shortest'] < 4 else ''}")

    with open(out_dir / "ablation.json", "w", encoding="utf-8") as f:
        json.dump({"segments": args.segments, "rows": rows}, f,
                  ensure_ascii=False, indent=1)
    print(f"\n写入 {out_dir / 'ablation.json'}")


if __name__ == "__main__":
    main()
