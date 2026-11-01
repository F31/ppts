#!/usr/bin/env python3
"""自引用召回：把页面自己的文字当讲稿，看绑定器能不能把它绑回**自己**。

## 这个脚本在回答什么

绑定器现有的两个样本都有讲稿：5 页造样本（19 元素）和 14 页客户 deck。
真实客户 PPT 常常**没有讲稿**——`大模型Token私有化运营解决方案` 那份 33 页里
只有 6 页有备注，且是资料出处不是口播稿。

没有讲稿就不能测 fidelity（那是「讲稿说 A、元素是 A，绑对没有」），
但可以测一件更基础的事：**字面完全相同的段，能不能绑回它自己的元素**。
这是 fidelity 的上界——如果连原文照抄都绑不回去，说明问题出在绑定器
（单调约束、装饰块抢占、相似文本混淆），不在讲稿写得像不像人话。

## 两种造稿方式

| 模式 | 一段 = 什么 | 测的是 |
|---|---|---|
| `per-element` | 一个元素一段，原文照抄 | 身份保持：1:1 该全中，掉一条就是错绑或漏绑 |
| `per-row` | 同一行的元素合成一段（用「；」连） | 真实形态：一段带出好几个元素，测覆盖 |

真实讲解更接近 `per-row`，但 `per-element` 是更硬的下界：它把「段与段抢元素」
这个变量消掉了，剩下的错全是绑定器自己的。

## 用法

    python3 scripts/fidelity/selfrecall.py \
        --elements tmp/q9/fid/elements.json \
        --out tmp/q9/fid/selfrecall.json

    # 两种模式一起，并列出最差的页
    python3 scripts/fidelity/selfrecall.py --elements ... --mode both --worst 8
"""
import argparse
import json
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
BANDCHECK = "./scripts/fidelity/bandcheck"

# 辅助元素：绑定器的白名单会主动拒绝它们。gold 里留着会让召回天然掉一截，
# 所以主统计排除，单独报一组「含辅助」的数。
AUX_KINDS = {"eyebrow", "note", "page_number", "marker", "footer", "image"}


def read_order(els):
    """按阅读顺序排：先按顶边分行，行内按左边。"""
    return sort_reading([e for e in els if (e.get("text") or "").strip()])


def sort_reading(els):
    """把元素按阅读顺序（行、行内从左到右）排好。

    `align` 的单调 DP 假设「元素顺序就是讲解顺序」。产品链路
    (`internal/app/bind_elements.go` 的 `walk(page.Shapes)`) 给的是形状树顺序，
    也就是 z-order——作者先放背景框、后放标题时，这个顺序和阅读顺序是反的。
    自引用测试必须能把两种情况分开：顺序不对造成的失败，不能算到讲稿头上。
    """
    if not els:
        return []
    hs = sorted(e["box"][3] for e in els if e["box"][3] > 0)
    h_med = hs[len(hs) // 2] if hs else 40
    tol = max(12.0, h_med * 0.6)
    rows, cur = [], []
    for e in sorted(els, key=lambda e: (e["box"][1], e["box"][0])):
        if not cur:
            cur = [e]
            continue
        y0 = min(x["box"][1] for x in cur)
        if abs(e["box"][1] - y0) <= tol:
            cur.append(e)
        else:
            rows.append(cur)
            cur = [e]
    if cur:
        rows.append(cur)
    out = []
    for r in rows:
        out.extend(sorted(r, key=lambda e: e["box"][0]))
    return out


def inversion_rate(els):
    """z-order 里「后面的元素跑到上面去了」的比例，用来量化这份 deck 顺序有多乱。"""
    txt = [e for e in els if (e.get("text") or "").strip()]
    if len(txt) < 2:
        return 0.0
    ys = [e["box"][1] for e in txt]
    inv = sum(1 for a, b in zip(ys, ys[1:]) if b < a - 20)
    return round(inv / (len(ys) - 1), 3)


def build_segments(page, mode):
    """返回 [(segment_id, text, [element_id...])]，按阅读顺序。"""
    order = read_order(page["elements"])
    segs = []
    if mode == "per-element":
        for i, e in enumerate(order, 1):
            segs.append((f"{page['index']}-s{i}", e["text"].strip(), [e["id"]]))
        return segs
    # per-row：复用 read_order 的分行结果
    rows, cur = [], []
    tol = 1.0
    hs = sorted(e["box"][3] for e in order if e["box"][3] > 0)
    if hs:
        tol = max(12.0, hs[len(hs) // 2] * 0.6)
    for e in order:
        if not cur:
            cur = [e]
            continue
        y0 = min(x["box"][1] for x in cur)
        if abs(e["box"][1] - y0) <= tol:
            cur.append(e)
        else:
            rows.append(sorted(cur, key=lambda x: x["box"][0]))
            cur = [e]
    if cur:
        rows.append(sorted(cur, key=lambda x: x["box"][0]))
    for i, r in enumerate(rows, 1):
        segs.append((f"{page['index']}-s{i}", "；".join(x["text"].strip() for x in r),
                     [x["id"] for x in r]))
    return segs


def run_bandcheck(elements_path, segs_path, rows_path):
    cmd = ["go", "run", BANDCHECK,
           "-elements", str(elements_path),
           "-segments", str(segs_path),
           "-out", str(rows_path),
           "-rows"]
    p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True)
    if p.returncode != 0:
        sys.stderr.write(p.stdout + p.stderr)
        raise SystemExit(f"bandcheck 失败（{p.returncode}）")


def score_page(page, gold, rows):
    """gold: {segment_id: set(element_id)}；rows: bandcheck 的绑定明细。"""
    els = {e["id"]: e for e in page["elements"]}
    main_ids = {e["id"] for e in page["elements"]
                if (e.get("text") or "").strip() and e["kind"] not in AUX_KINDS}
    hit_main, hit_all = set(), set()
    wrong = []
    for r in rows:
        g = gold.get(r["segmentId"])
        if g and r["elementId"] in g:
            hit_all.add(r["elementId"])
            if r["elementId"] in main_ids:
                hit_main.add(r["elementId"])
        else:
            wrong.append(r)
    return {
        "index": page["index"],
        "bound": len(rows),
        "goldMain": len(main_ids),
        "hitMain": len(hit_main),
        "recallMain": round(len(hit_main) / len(main_ids), 3) if main_ids else None,
        "precision": round((len(rows) - len(wrong)) / len(rows), 3) if rows else None,
        "wrong": wrong[:5],
        "missed": sorted(main_ids - hit_main)[:5],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--elements", required=True)
    ap.add_argument("--mode", default="per-element",
                    choices=["per-element", "per-row", "both"])
    ap.add_argument("--out", default="")
    ap.add_argument("--worst", type=int, default=5)
    ap.add_argument("--order", default="reading", choices=["reading", "asis"],
                    help="喂给绑定器的元素顺序：reading=按阅读序重排，asis=保持形状树（z-order）")
    ap.add_argument("--keep", default="", help="保留中间文件目录（默认 tmp 下临时）")
    args = ap.parse_args()

    pages = json.load(open(args.elements, encoding="utf-8"))
    work = Path(args.keep) if args.keep else Path(args.out or "tmp/selfrecall").with_suffix("")
    work = Path(str(work))
    work.mkdir(parents=True, exist_ok=True)

    invs = [inversion_rate(p["elements"]) for p in pages]
    el_path = Path(args.elements)
    if args.order == "reading":
        reordered = [dict(p, elements=sort_reading(p["elements"])) for p in pages]
        el_path = work / "elements-reading.json"
        el_path.write_text(json.dumps(reordered, ensure_ascii=False), encoding="utf-8")

    modes = ["per-element", "per-row"] if args.mode == "both" else [args.mode]
    report = {"elements": args.elements, "pages": len(pages), "order": args.order,
              "zOrderInversionMean": round(sum(invs) / len(invs), 3) if invs else 0,
              "modes": {}}

    for mode in modes:
        seg_pages, gold_all = [], {}
        for p in pages:
            segs = build_segments(p, mode)
            seg_pages.append({"slide": p["index"] + 1,
                              "segments": [{"id": s[0], "text": s[1]} for s in segs]})
            gold_all[p["index"]] = {s[0]: set(s[2]) for s in segs}
        seg_path = work / f"seg-{mode}.json"
        seg_path.write_text(json.dumps({"pages": seg_pages}, ensure_ascii=False, indent=1),
                            encoding="utf-8")
        rows_path = work / f"band-{mode}.json"
        run_bandcheck(el_path, seg_path, rows_path)

        bands = json.load(open(rows_path, encoding="utf-8"))["pages"]
        per = [score_page(pages[i], gold_all[pages[i]["index"]], b.get("rows") or [])
               for i, b in enumerate(bands)]
        gm = sum(x["goldMain"] for x in per)
        hm = sum(x["hitMain"] for x in per)
        tb = sum(x["bound"] for x in per)
        report["modes"][mode] = {
            "recallMain": round(hm / gm, 3) if gm else None,
            "boundRatio": round(tb / gm, 3) if gm else None,
            "pages": per,
        }
        print(f"\n=== {mode}：{len(pages)} 页，正文元素 {gm}，绑回自身 {hm} "
              f"（召回 {hm / gm:.1%}），总绑定 {tb}" if gm else "")
        worst = sorted([x for x in per if x["recallMain"] is not None],
                       key=lambda x: x["recallMain"])[:args.worst]
        for w in worst:
            print(f"  p{w['index'] + 1:>2} 召回 {w['recallMain']:.0%} "
                  f"({w['hitMain']}/{w['goldMain']}) 绑定 {w['bound']} 精确 {w['precision']}")

    if args.out:
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=1),
                                  encoding="utf-8")
        print(f"\n写到 {args.out}")


if __name__ == "__main__":
    main()
