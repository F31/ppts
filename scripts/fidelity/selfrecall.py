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


def has_words(text):
    """这段文字里有没有「可以念出来的符号」。

    纯标点（`…`、`……`、`·`、`/`）在归一化后是空串，绑定器的相似度直接返回 0，
    任何阈值都绑不上。把它算进 gold 会让召回凭空掉一截——掉的不是绑定器的能力，
    是这份 gold 造得不对。产品侧也一样：一个省略号本来就不该有出场动画。
    """
    return any(ch.isalnum() for ch in (text or ""))


def is_content(el):
    return (el.get("text") or "").strip() and has_words(el["text"])


def read_order(els):
    """按阅读顺序排：先按顶边分行，行内按左边。"""
    return sort_reading([e for e in els if is_content(e)])


def sort_reading(els):
    """把元素按阅读顺序（行、行内从左到右）排好。

    `align` 的单调 DP 假设「元素顺序就是讲解顺序」。产品链路
    (`internal/app/bind_elements.go` 的 `walk(page.Shapes)`) 给的是形状树顺序，
    也就是 z-order——作者先放背景框、后放标题时，这个顺序和阅读顺序是反的。
    自引用测试必须能把两种情况分开：顺序不对造成的失败，不能算到讲稿头上。

    分行只用**有文字**的元素，空形状随后挂到纵向中心最近的那一行——与
    `internal/animation/bind/order.go` 的 readingOrder 逐行对应。两份实现算的
    必须是同一件事，否则离线实验和产品行为对不上，量的就不是同一个东西。

    空形状不当基准，是因为一行字从上往下扫时，第一个落进这一行的元素就是基准线；
    底板常比它装的文字高、顶边也靠上几像素，基准线被抬高后下一行字就掉出去了。
    """
    if not els:
        return []
    text = [e for e in els if is_content(e)]
    rest = [e for e in els if not is_content(e)]
    if not text:
        return sorted(rest, key=lambda e: (e["box"][1], e["box"][0]))

    rows = bands(text)
    for e in rest:
        rows[nearest_row(rows, e)].append(e)

    out = []
    for r in rows:
        out.extend(sorted(r, key=lambda e: e["box"][0]))
    return out


def bands(els):
    """把文字元素分成行：顶边接近的算一行，行按 y 升序、行内按 x 升序。"""
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
    return [sorted(r, key=lambda e: e["box"][0]) for r in rows]


def nearest_row(rows, e):
    """纵向中心落在哪一行；落在行间空隙时取最近的一行（并列取靠上的）。"""
    cy = e["box"][1] + e["box"][3] / 2
    best, best_d = 0, float("inf")
    for i, row in enumerate(rows):
        top = min(x["box"][1] for x in row)
        bot = max(x["box"][1] + x["box"][3] for x in row)
        d = top - cy if cy < top else (cy - bot if cy > bot else 0.0)
        if d < best_d:
            best, best_d = i, d
    return best


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
    # per-row：同一行的元素合成一段，行切分与阅读序共用 bands（一份实现）
    rows = bands(read_order(page["elements"]))
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
    """gold: {segment_id: set(element_id)}；rows: bandcheck 的绑定明细。

    两类绑定的判定口径必须分开，否则精确率是假的：

    - `text`（字面匹配）：gold 里指定了这段该带出哪些元素，绑到别的就是错绑。
    - 容器类（structural / sibling / prior）：它们作用在**没有文字**的卡片底板、
      底板旁的图标上，gold 里本来就没有它们的条目——按「不在 gold 里就算错」
      统计，等于把「猜不出」当成「猜错」，精确率会被系统性压低。
      真实产品里这类绑定该不该出，要看几何是否真的包住内容，不是这份 gold 能回答的。
    """
    main_ids = {e["id"] for e in page["elements"]
                if is_content(e) and e["kind"] not in AUX_KINDS}
    hit_main = set()
    wrong, container = [], 0
    text_rows = 0
    for r in rows:
        if r["source"] != "text":
            container += 1
            continue
        text_rows += 1
        g = gold.get(r["segmentId"])
        if g and r["elementId"] in g:
            if r["elementId"] in main_ids:
                hit_main.add(r["elementId"])
        else:
            wrong.append(r)
    return {
        "index": page["index"],
        "bound": len(rows),
        "boundText": text_rows,
        "boundContainer": container,
        "goldMain": len(main_ids),
        "hitMain": len(hit_main),
        "recallMain": round(len(hit_main) / len(main_ids), 3) if main_ids else None,
        # 精确率只对字面匹配有定义：分母是字面绑定的条数。
        "precision": round((text_rows - len(wrong)) / text_rows, 3) if text_rows else None,
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
                    help="喂给绑定器的元素顺序：reading=按阅读序重排，asis=保持形状树（z-order）。"
                         "order.go 上线后绑定器内部会自己重排，两种喂法应得同一结果；"
                         "这个开关留着是为了能复现「不重排会怎样」的对照，不是产品路径")
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
        tt = sum(x["boundText"] for x in per)
        tc = sum(x["boundContainer"] for x in per)
        nw = sum(len(x["wrong"]) for x in per)  # 只含字面绑定的错
        report["modes"][mode] = {
            "recallMain": round(hm / gm, 3) if gm else None,
            "boundRatio": round(tb / gm, 3) if gm else None,
            "boundText": tt,
            "boundContainer": tc,
            "precisionText": round((tt - nw) / tt, 3) if tt else None,
            "pages": per,
        }
        if gm:
            print(f"\n=== {mode}：{len(pages)} 页，正文元素 {gm}，绑回自身 {hm} "
                  f"（召回 {hm / gm:.1%}）")
            print(f"    绑定 {tb} 条：字面 {tt}（其中错绑 {nw}，精确 "
                  f"{(tt - nw) / tt:.1%}）+ 容器推断 {tc}"
                  if tt else f"    绑定 {tb} 条：字面 0 + 容器推断 {tc}")
        worst = sorted([x for x in per if x["recallMain"] is not None],
                       key=lambda x: x["recallMain"])[:args.worst]
        for w in worst:
            print(f"  p{w['index'] + 1:>2} 召回 {w['recallMain']:.0%} "
                  f"({w['hitMain']}/{w['goldMain']}) 字面 {w['boundText']} 精确 {w['precision']}")

    if args.out:
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=1),
                                  encoding="utf-8")
        print(f"\n写到 {args.out}")


if __name__ == "__main__":
    main()
