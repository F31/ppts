#!/usr/bin/env python3
"""给 elements.json 补上元素文本（`text` 字段）。

为什么要单独一步，而不是重新生成底图：重跑 `gen_pages.py` 会重写 PNG 字节，
而 `film_C_narrated.mp4` 是按当前这批 PNG 渲的，换图之后成片与素材就对不上了。
所以文本从 `gen_pages.py` 里**抽**出来（用 ast，不用正则），补进现有的
`elements.json`，底图一个字节都不动。

元素文本在真实链路里是现成的：`project.Shape.Text` 来自 pptx 解析
（`internal/project/model.go`）。是 `PageElements` 换算成 `render.Element`
时把它丢了——渲染器不做文本排版，不需要它。但元素级绑定需要。

    python3 scripts/fidelity/enrich_elements.py tmp/q7/fid/elements.json

幂等：重复跑结果一样。
"""
import ast
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
GEN = os.path.join(HERE, "gen_pages.py")

# gen_pages.py 里页 1 的四行是循环生成的（`f"{head}　{sub}"`），文本不是字面量，
# ast 抽不到常量。这四条在这里显式补一份，值抄自 `items = [...]`。
# 新增循环生成的元素时，这里要同步——抽不到的会在 stderr 里点名，不会静默漏。
MANUAL = {
    "p1-item0": "稳定出片　同一份计划渲染两次，5 段 sha256 逐字节相同",
    "p1-item1": "资源可控　单任务峰值 2.7GB；两个 30 页长片并发 948s 不 OOM",
    "p1-item2": "字体可验　字集边界写成构建门禁，缺一个必须码位就失败",
    "p1-item3": "可观测　每页进度与失败原因可定位，而不是「渲染卡住」",
}


def text_map(src_path=GEN):
    """从 gen_pages.py 里抽出 (element id → 形状文本)。

    元素元组统一是 4 项 `(id, kind, box, text)`。只认这种形状：id 必须是
    以 p 开头的字符串、kind 是字符串、box 是数字元组、text 是字符串。
    第 3 项缺失（老的三元组）就跳过——那种情况说明源头还没补文本。
    """
    tree = ast.parse(open(src_path, encoding="utf-8").read(), filename=src_path)
    out = {}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Tuple) or len(node.elts) != 4:
            continue
        eid, kind, box, text = node.elts
        # box 是 (x, y, w, h) 的元组字面量，不是 Constant——所以逐个判类型，
        # 不能用「全是 Constant」一刀切（那样一个都抽不到）。
        if not (
            isinstance(eid, ast.Constant) and isinstance(eid.value, str)
            and eid.value.startswith("p")
            and isinstance(kind, ast.Constant) and isinstance(kind.value, str)
            and isinstance(box, ast.Tuple)
            and isinstance(text, ast.Constant) and isinstance(text.value, str)
        ):
            continue
        out[eid.value] = text.value
    out.update(MANUAL)
    return out


def enrich(path, mapping):
    doc = json.load(open(path, encoding="utf-8"))
    filled = missing = 0
    for page in doc:
        for el in page["elements"]:
            eid = el["id"]
            if eid in mapping:
                el["text"] = mapping[eid]
                filled += 1
            else:
                # 不写空串：空串会被当成「这个元素没有文字」，与「没抽取到」
                # 是两件事，后者的处置是回头补 gen_pages.py，不是静默跳过。
                el.pop("text", None)
                missing += 1
                print(f"  ! 源头没有 {eid} 的文本", file=sys.stderr)
    json.dump(doc, open(path, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    return filled, missing


def main():
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    path = sys.argv[1]
    mapping = text_map()
    print(f"源头抽出 {len(mapping)} 个元素文本")
    filled, missing = enrich(path, mapping)
    print(f"{path}: 补 {filled} 个，缺 {missing} 个")
    return 0 if missing == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
