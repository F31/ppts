#!/usr/bin/env python3
"""从真实 PPTX 抽元素清单，作为绑定实验的**第二个样本**。

第一个样本（`tmp/q7/fid/elements.json`）是 `gen_pages.py` 造的 5 页，风格单一、
每页 2–6 个元素、没有表格也没有装饰形状。实验记录里第 1 条边界就是「样本只有一个」，
换一份讲义数字会变。这个脚本提供那份讲义：

    internal/animation/animdefault/testdata/sample-customer-deck.pptx
    14 页 / 真实客户方案 / 有表格、图片、卡片底、页码、眉标、零宽连线

选它不是因为好看，是因为它**脏**：同一页上叠着 20 多个形状，其中一半是装饰。
绑定器在干净样本上准确率再高，遇到这种页面仍是另一回事。

## 与真实链路的关系

真实链路里的元素来自 `project.Shape`（`internal/project/model.go`），
`PageElements` 换算成 `render.Element` 时只留 ID/Kind/Bounds，文本丢掉。
这里用 python-pptx 复刻同一份清单：同样的四件事——id、kind、包围盒、文本。
**kind 的判定是启发式的**（见 `classify()`），真实链路里 kind 来自形状类型与占位符，
两边不必逐字相同；绑定器只把 kind 当作「图注/页码这类不该动」的线索用。

坐标系换算到 1920×1080，与 `gen_pages.py` 一致，这样两个样本的包围盒可比。

## 为什么不用 Go 写

`PageElements` 在 `internal/app`，要复用就得起一个 cmd 或加导出函数；
而这一步只发生在实验准备阶段，不该为了脚本去改产品代码的 API。
python-pptx 是现成的（本机 1.0.2），且抽出来的东西要人工核对，Python 改起来快。

    python3 scripts/fidelity/extract_pptx_elements.py \
        --pptx internal/animation/animdefault/testdata/sample-customer-deck.pptx \
        --slides 1,2,4,8,10 --out tmp/q8/fid/elements.json
"""
import argparse
import json
import os
import re
import sys

from pptx import Presentation
from pptx.util import Emu

PAGE_W, PAGE_H = 1920, 1080

# 小于这个面积的形状不进清单。零宽零高的连线与分隔线在画面上产不出可见变化，
# 绑上它们只会让报告多出一堆「误绑」——那是噪声，不是发现。
MIN_AREA_PX = 4 * 4


def px(emu, total_emu, total_px):
    return int(round(emu / total_emu * total_px))


def max_font_pt(shape):
    """形状里最大的字号（pt）。取不到返回 None——继承自母版的情况很常见。"""
    if not shape.has_text_frame:
        return None
    sizes = [r.font.size.pt for p in shape.text_frame.paragraphs for r in p.runs
             if r.font.size is not None]
    return max(sizes) if sizes else None


def classify(shape, box, pt):
    """形状 → kind。

    判据只用三样东西：形状类型、字号、位置。字号是主力——标题 32pt、卡片标题 15pt、
    正文字号 10–11pt、眉标 12pt，在这份真实讲义里分得很开；取不到字号（继承母版）
    时才退回位置与宽度。**不按文本内容猜**，否则「01」这种编号会和页码混在一起。
    """
    x, y, w, h = box
    text = shape.text_frame.text.strip() if shape.has_text_frame else ""

    if shape.has_table:
        return "table"
    if shape.shape_type == 13:                     # PICTURE
        return "image"
    if not text:
        return "shape"                             # 卡片底、色块、圆形底座
    # 页码：右下角两位数字。位置判据不能少——正文里也有「01」这种编号。
    if re.fullmatch(r"\d{1,2}", text) and y > PAGE_H * 0.85 and x > PAGE_W * 0.85:
        return "page_number"
    # 编号标记：纯数字的列表序号。与正文分开是有用的：计数规则只认它，
    # 不会把「174s」这种带单位的数字当成序号。
    if re.fullmatch(r"0?\d{1,2}", text):
        return "marker"
    if pt is not None and pt >= 28:
        return "title"
    if y < PAGE_H * 0.15 and (pt is None or pt <= 14) and len(text) <= 40:
        return "eyebrow"
    if y > PAGE_H * 0.85 and (pt is None or pt <= 14):
        return "note"
    return "bullet"


def dump_slide(slide, index, sw, sh):
    elements = []
    for shape in slide.shapes:
        box = [px(shape.left, sw, PAGE_W), px(shape.top, sh, PAGE_H),
               px(shape.width, sw, PAGE_W), px(shape.height, sh, PAGE_H)]
        if box[2] * box[3] < MIN_AREA_PX:
            continue
        text = shape.text_frame.text.strip().replace("\v", "\n") if shape.has_text_frame else ""
        kind = classify(shape, box, max_font_pt(shape))
        elements.append({
            "id": f"p{index + 1}-s{shape.shape_id}",
            "kind": kind,
            "box": box,
            "text": text,
        })
    # 不排序：**保留文档顺序**，也就是 PPTX 里的 z-order。渲染端给元素编的号、
    # 上一版样片里 `120 + i*180` 的错落、单调对齐 DP 吃的顺序，都是这一个顺序。
    # 按 (y 带, x) 重排听起来更像阅读顺序，实际上会把「卡片标题」和「卡片注释」
    # 拆到两个完全不同的位置（一行一行读，而不是一块一块读），DP 反而绑不上。
    return {"index": index, "slideId": f"slide-{index}", "elements": elements}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pptx",
                    default="internal/animation/animdefault/testdata/sample-customer-deck.pptx")
    ap.add_argument("--slides", default="1,2,4,8,10",
                    help="逗号分隔的 1-based 页号；all = 全部")
    ap.add_argument("--out", default="tmp/q8/fid/elements.json")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    prs = Presentation(args.pptx)
    sw, sh = prs.slide_width, prs.slide_height

    wanted = None
    if args.slides != "all":
        wanted = {int(s) for s in args.slides.split(",") if s.strip()}

    pages = []
    for i, slide in enumerate(prs.slides):
        if wanted is not None and (i + 1) not in wanted:
            continue
        page = dump_slide(slide, len(pages), sw, sh)
        pages.append(page)
        if not args.quiet:
            print(f"== slide {i + 1} → page {page['index']}  {len(page['elements'])} 个元素")
            for e in page["elements"]:
                print(f"   {e['id']:10s} {e['kind']:12s} {str(e['box']):24s} "
                      f"{(e['text'] or '<无文本>')[:44]}")

    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    json.dump(pages, open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    total = sum(len(p["elements"]) for p in pages)
    print(f"\n{len(pages)} 页 / {total} 个元素 → {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
