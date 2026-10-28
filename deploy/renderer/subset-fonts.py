#!/usr/bin/env python3
"""构建期字体子集化：把 fonts-noto-cjk 的 TTC 切成「只含 charset.txt 声明字集」的 OTF。

为什么在构建期做：字集是部署决策（进 planHash），不是运行时输入。运行时按文案子集化
要缓存、要处理并发、还要为一堆一次性字体付内存，换来的只有几百 KB——不值得。
ADR-4 已经定了「只做构建期子集」，这里把它落地。

三条硬性质，脚本自己不满足就退出非零，让镜像构建失败：

1. **不带病出镜像**：charset.txt 声明的每个「可绘制」码位，子集产物必须都有字形。
   判定时排除 Cn（未分配）、Cc/Cf（控制/格式）、Zs/Zl/Zp（空白）——这些本来就不出字形，
   把它们的缺失算成缺字会让断言永远红。
2. **家族名不变**：子集后 name 表必须仍能报出 `Noto Sans CJK SC`。fontconfig 靠这个
   匹配，改名会让 `/healthz.fonts` 与实际可用字体对不上。
3. **版本可推导**：版本号 = 字体自身的 Version + charset.txt 的 sha256 前 8 位。
   改字集 → 版本变 → planHash 变，不需要任何人记得手改版本号。

产物：
    <out>/NotoSansCJKsc-Regular.otf
    <out>/NotoSansCJKsc-Bold.otf
    <out>/manifest.json          审计用：字集摘要、每字面的码位/字形/体积
    <out>/../fonts.version       运行时读的版本号（由 --version-file 指定）
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import unicodedata

from fontTools import subset
from fontTools.ttLib import TTFont, TTCollection

FAMILY = "Noto Sans CJK SC"
# 字面：TTC 文件名 → 输出用的样式名。只留 Regular/Bold：
# 正文与强调是这两档，其余（Light/Medium/Black）没有消费方。
FACES = {"NotoSansCJK-Regular.ttc": "Regular", "NotoSansCJK-Bold.ttc": "Bold"}

# 不出字形的类别：断言时跳过它们，否则 U+2000 这类空白会让覆盖率永远不达标。
NON_DRAWING = {"Cn", "Cc", "Cf", "Zs", "Zl", "Zp", "Co"}


def parse_charset(path: str) -> tuple[dict[int, bool], str]:
    """返回 {码位: 是否 must}。同一码位出现多次时 must 优先。"""
    cps: dict[int, bool] = {}
    with open(path, encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.split("#", 1)[0].strip()
            if not line:
                continue
            parts = line.split()
            rng, must = parts[0], len(parts) > 1 and parts[1] == "must"
            try:
                if "-" in rng:
                    lo, hi = rng.split("-", 1)
                    span = range(int(lo, 16), int(hi, 16) + 1)
                else:
                    span = [int(rng, 16)]
            except ValueError:
                raise SystemExit(f"charset {path}:{lineno}: 无法解析 {raw.strip()!r}")
            for cp in span:
                cps[cp] = cps.get(cp, False) or must
    if not cps:
        raise SystemExit(f"charset {path}: 一个字都没有")
    digest = hashlib.sha256(open(path, "rb").read()).hexdigest()
    return cps, digest


def pick_face(coll: TTCollection, family: str) -> int:
    for i, font in enumerate(coll.fonts):
        if font["name"].getDebugName(1) == family:
            return i
    raise SystemExit(f"{family}: TTC 里没有这个家族")


def sanitize_version(raw: str) -> str:
    """从 name 表里抠出版本号本身。

    Noto CJK 的 name ID 5 是 `Version 2.004;hotconv 1.0.118;makeotfexe 2.5.65603`
    这种带工具链的串，整条塞进 planHash 既长又会因为「用哪版 makeotf 构建」而变，
    那不是字体行为的变化。只取第一段版本号。
    """
    m = re.search(r"Version\s*([0-9]+(?:\.[0-9]+)*)", raw)
    if m:
        return m.group(1)
    keep = "".join(c for c in raw if c.isalnum() or c in ".-")
    return keep.strip(".") or "unknown"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", required=True, help="fonts-noto-cjk 的 opentype/noto 目录")
    ap.add_argument("--charset", required=True, help="charset.txt")
    ap.add_argument("--out", required=True, help="子集产物目录")
    ap.add_argument("--version-file", required=True, help="把推导出的版本号写到这里")
    args = ap.parse_args()

    cps, digest = parse_charset(args.charset)
    # 空白/控制符不出字形，断言时排除（否则 U+2000 这类永远不达标）。
    drawable = sorted(c for c in cps if unicodedata.category(chr(c)) not in NON_DRAWING)
    must = [c for c in drawable if cps[c]]
    best = [c for c in drawable if not cps[c]]
    print(f"charset: {len(cps)} 码位，可绘制 {len(drawable)}（must {len(must)} / best-effort {len(best)}），"
          f"sha256={digest[:8]}")

    os.makedirs(args.out, exist_ok=True)
    report = {"family": FAMILY, "charsetSha256": digest, "faces": []}
    version = None

    for filename, style in FACES.items():
        src = os.path.join(args.src, filename)
        if not os.path.exists(src):
            raise SystemExit(f"缺少字面文件：{src}")

        coll = TTCollection(src, lazy=True)
        idx = pick_face(coll, FAMILY)
        font = TTFont(src, fontNumber=idx)
        src_cmap = set(font.getBestCmap())
        font.close()

        # 先证明「声明必须覆盖的东西源字体真有」，否则是 charset.txt 在说谎。
        absent = [c for c in must if c not in src_cmap]
        if absent:
            sample = " ".join(f"U+{c:04X}" for c in absent[:12])
            raise SystemExit(
                f"{filename}: charset.txt 标了 must 的码位里有 {len(absent)} 个源字体没有（{sample}…）\n"
                f"要么把它们降成 best-effort（靠 fallback），要么换字体——"
                f"不要让镜像带着「声明覆盖、实际缺字」出厂。"
            )
        # best-effort：只收源字体有的，缺的交给 fallback 链。
        extra = [c for c in best if c in src_cmap]
        skipped = [c for c in best if c not in src_cmap]
        wanted = sorted(must + extra)

        opts = subset.Options()
        opts.layout_features = ["*"]  # 保留 kern/liga：拉丁与标点排版依赖它
        opts.name_IDs = ["*"]          # 家族名/样式名必须活下来（见脚本头第 2 条）
        opts.name_legacy = True
        opts.name_languages = ["*"]
        opts.notdef_outline = True
        opts.recommended_glyphs = True
        opts.symbol_cmap = True
        opts.legacy_cmap = True
        opts.glyph_names = False
        opts.drop_tables += ["DSIG"]

        font = TTFont(src, fontNumber=idx)
        if version is None:
            version = sanitize_version(font["name"].getDebugName(5) or "unknown")
        sub = subset.Subsetter(options=opts)
        sub.populate(unicodes=wanted)
        sub.subset(font)

        family_after = font["name"].getDebugName(1)
        if family_after != FAMILY:
            raise SystemExit(f"{filename}: 子集后家族名变成了 {family_after!r}，fontconfig 会匹配不上")

        out_cmap = set(font.getBestCmap())
        missing = [c for c in must if c not in out_cmap]
        if missing:
            raise SystemExit(f"{filename}: 子集后仍缺 {len(missing)} 个 must 码位，子集参数有问题")

        dst = os.path.join(args.out, f"NotoSansCJKsc-{style}.otf")
        font.save(dst)
        font.close()

        size = os.path.getsize(dst)
        report["faces"].append(
            {"style": style, "file": os.path.basename(dst), "codepoints": len(out_cmap),
             "glyphs": len(TTFont(dst).getGlyphOrder()), "bytes": size,
             "bestEffortSkipped": len(skipped)}
        )
        print(f"  {style:8s} 码位={len(out_cmap):6d} 体积={size / 1024 / 1024:.2f}MB "
              f"best-effort 跳过={len(skipped)} -> {dst}")

    # 版本号进 planHash：字集一改，所有缓存键失效——这正是要的。
    full_version = f"{version}-sub{digest[:8]}"
    report["version"] = full_version
    with open(os.path.join(args.out, "manifest.json"), "w", encoding="utf-8") as fh:
        json.dump(report, fh, ensure_ascii=False, indent=2)
    with open(args.version_file, "w", encoding="utf-8") as fh:
        fh.write(full_version + "\n")

    total = sum(f["bytes"] for f in report["faces"])
    print(f"合计 {total / 1024 / 1024:.2f}MB，版本 {full_version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
