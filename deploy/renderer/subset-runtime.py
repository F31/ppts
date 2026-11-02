#!/usr/bin/env python3
"""运行期字体子集化：把镜像内的母版 OTF 再裁到「一份成品包实际用到的字符」。

与构建期的 subset-fonts.py 的分工：

  构建期（subset-fonts.py）：源 TTC（19MB）→ 母版（11MB，全 URO）。
                            决定「这个镜像认识哪些字」，边界写在 charset.txt。
  运行期（本脚本）：        母版 → 成品包字体（约 100KB，一份讲稿的字符）。
                            决定「这一份包要带哪些字」，边界来自讲稿本身。

为什么不在构建期一次做完：成品的字集只有打包时才知道（字幕、标题、播放器文案），
而每份包的字符集都不同。构建期能产出的最细粒度就是母版。

约定与构建期一致的部分（**不能改**，否则二次子集会丢东西）：layout_features 全保留、
name 表全保留。母版的 cmap 里还有空格这类不出轮廓但决定词距的码位，它们在
populate 时必须一起带上，否则成品里的空格会变成 .notdef。

输出：stdout 只写一行 JSON（kept / missing），日志走 stderr。
     kept 是母版能提供的码位，missing 是母版就没有的（生僻字、CJK Ext-A/B、emoji）——
     missing 不是错误，浏览器会沿 fallback 链继续找，但调用方必须知道它们不在包里。
"""

from __future__ import annotations

import argparse
import json
import os
import sys

from fontTools import subset
from fontTools.ttLib import TTFont


def read_codepoints(path: str) -> list[int]:
    cps: list[int] = []
    with open(path, encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.split("#", 1)[0].strip()
            if not line:
                continue
            try:
                cps.append(int(line, 16))
            except ValueError:
                raise SystemExit(f"charset {path}:{lineno}: 无法解析 {raw.strip()!r}")
    return cps


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="母版 OTF（charset.txt 的产物）")
    ap.add_argument("--charset", required=True, help="每行一个十六进制码位")
    ap.add_argument("--out", required=True, help="子集产物路径")
    args = ap.parse_args()

    requested = read_codepoints(args.charset)
    if not requested:
        raise SystemExit("charset 为空")

    font = TTFont(args.base)
    base_cmap = set(font.getBestCmap())

    kept = [c for c in requested if c in base_cmap]
    missing = [c for c in requested if c not in base_cmap]
    if not kept:
        raise SystemExit(
            f"请求的 {len(requested)} 个码位母版一个都没有——母版可能不是这份 charset 的产物"
        )

    opts = subset.Options()
    opts.layout_features = ["*"]
    opts.name_IDs = ["*"]
    opts.name_legacy = True
    opts.name_languages = ["*"]
    opts.notdef_outline = True
    opts.recommended_glyphs = True
    opts.symbol_cmap = True
    opts.legacy_cmap = True
    opts.glyph_names = False
    opts.drop_tables += ["DSIG"]

    sub = subset.Subsetter(options=opts)
    sub.populate(unicodes=kept)
    sub.subset(font)
    font.save(args.out)
    font.close()

    size = os.path.getsize(args.out)
    print(f"subset: {len(kept)} 码位, {size} 字节, missing={len(missing)}", file=sys.stderr)
    json.dump({"kept": kept, "missing": missing}, sys.stdout)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
