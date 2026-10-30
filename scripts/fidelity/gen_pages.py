#!/usr/bin/env python3
"""生成 5 页「带真实文字」的底图 + 元素清单，作为渲染保真的对照输入。

为什么要这个而不是仓库里的色块样片：色块画面里一个字形都没有，
它证明不了「子集字体能排版」，也证明不了「文字没被合成吃掉」。
ADR-15 §7 的空格缺陷、ADR-17 的图层顺序缺陷，都是换成这份样片后才暴露的。

依赖 PIL（只用于造测试输入，不进镜像）：

    python3 scripts/fidelity/gen_pages.py \
        --fonts /usr/share/fonts/opentype/ppts \
        --out /tmp/fidelity

`--fonts` 指向镜像里的子集字体目录（`docker cp` 出来即可），这样造出来的底图
与线上用的是同一份字形，缺字会直接出现在画面上而不是事后才发现。
"""
import argparse
import json
import os

from PIL import Image, ImageDraw, ImageFont

W, H = 1920, 1080

INK = (0x1B, 0x22, 0x2C)
MUTED = (0x5B, 0x66, 0x77)
ACCENT = (0x1E, 0x3A, 0x8A)
ACCENT2 = (0x0F, 0x76, 0x6E)
CARD = (0xF4, 0xF7, 0xFA)
LINE = (0xD8, 0xE0, 0xEA)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fonts", required=True, help="含 NotoSansCJKsc-{Regular,Bold}.otf 的目录")
    ap.add_argument("--out", required=True)
    # 只重画指定页（逗号分隔）。改一页版面时不该顺带重画另外四页：
    # 其余页的底图是别人量过的对照，重画会让已记录的对齐/外观读数失去意义。
    ap.add_argument("--only", default="", help="只生成这些页，如 2 或 1,3")
    args = ap.parse_args()

    reg = os.path.join(args.fonts, "NotoSansCJKsc-Regular.otf")
    bld = os.path.join(args.fonts, "NotoSansCJKsc-Bold.otf")
    for p in (reg, bld):
        if not os.path.exists(p):
            raise SystemExit(f"缺字体：{p}（从镜像 docker cp 出来）")

    def f(path, size):
        ft = ImageFont.truetype(path, size)
        # 可变字体（如 NotoSansSC-VF）在同一个文件里装全部字重：路径叫 Bold 就把实例
        # 切到 Bold。静态字体没有这个接口，异常吞掉即可——行为与从前一致。
        if "Bold" in os.path.basename(path) and hasattr(ft, "set_variation_by_name"):
            try:
                ft.set_variation_by_name("Bold")
            except Exception:
                pass
        return ft

    def bg(img, top=(0xFF, 0xFF, 0xFF), bottom=None):
        d = ImageDraw.Draw(img)
        if bottom is None:
            d.rectangle([0, 0, W, H], fill=top)
            return
        for y in range(H):
            t = y / H
            d.rectangle([0, y, W, y + 1],
                        fill=tuple(int(top[i] + (bottom[i] - top[i]) * t) for i in range(3)))

    # ---- 5 页内容。生僻字与符号单独成行：它们是字集边界上最可疑的部分。
    def page0():
        img = Image.new("RGB", (W, H))
        bg(img, (0xF7, 0xF9, 0xFC), (0xE6, 0xEE, 0xF8))
        d = ImageDraw.Draw(img)
        d.rectangle([0, 0, 18, H], fill=ACCENT)
        d.text((120, 210), "2026 年度技术规划 · 内部资料", font=f(reg, 34), fill=MUTED)
        d.text((116, 300), "智讲 PPT 渲染服务", font=f(bld, 104), fill=INK)
        d.text((120, 452), "从版式到视频：像素级一致性的工程实现", font=f(reg, 46), fill=ACCENT)
        d.rectangle([120, 546, 520, 552], fill=ACCENT2)
        d.text((120, 620), "汇报人：李喆　·　2026-10-29", font=f(reg, 34), fill=MUTED)
        d.text((120, 690), "覆盖：并发压测、字体字集、超时推导、容器限额", font=f(reg, 32), fill=MUTED)
        d.rectangle([1500, 780, 1800, 900], fill=(0xDC, 0xE6, 0xF4))
        d.text((1524, 812), "PPTX → MP4", font=f(bld, 30), fill=ACCENT)
        # 第 4 项是形状里的文字。真实链路里它来自 pptx 解析（project.Shape.Text），
        # 这里必须一并导出——元素级绑定靠它做匹配，丢了文本就只能靠人工挂。
        return img, [
            ("p0-eyebrow", "bullet", (120, 200, 900, 60), "2026 年度技术规划 · 内部资料"),
            ("p0-title", "title", (116, 288, 1320, 130), "智讲 PPT 渲染服务"),
            ("p0-sub", "bullet", (120, 440, 1500, 70), "从版式到视频：像素级一致性的工程实现"),
            ("p0-meta", "bullet", (120, 608, 1200, 110),
             "汇报人：李喆 · 2026-10-29 覆盖：并发压测、字体字集、超时推导、容器限额"),
        ]

    def page1():
        img = Image.new("RGB", (W, H), (0xFF, 0xFF, 0xFF))
        d = ImageDraw.Draw(img)
        d.rectangle([0, 0, W, 8], fill=ACCENT)
        d.text((120, 96), "本期四件事", font=f(bld, 66), fill=INK)
        d.text((120, 186), "每一件都对应一个可复核的数字，不是「已优化」三个字", font=f(reg, 32), fill=MUTED)
        d.rounded_rectangle([120, 270, 1800, 900], radius=24, fill=CARD, outline=LINE, width=2)
        items = [
            ("稳定出片", "同一份计划渲染两次，5 段 sha256 逐字节相同"),
            ("资源可控", "单任务峰值 2.7GB；两个 30 页长片并发 948s 不 OOM"),
            ("字体可验", "字集边界写成构建门禁，缺一个必须码位就失败"),
            ("可观测", "每页进度与失败原因可定位，而不是「渲染卡住」"),
        ]
        boxes = []
        for i, (head, sub) in enumerate(items):
            y = 320 + i * 150
            d.ellipse([176, y + 14, 236, y + 74], fill=ACCENT if i % 2 == 0 else ACCENT2)
            d.text((196, y + 28), str(i + 1), font=f(bld, 30), fill=(0xFF, 0xFF, 0xFF))
            d.text((276, y + 10), head, font=f(bld, 40), fill=INK)
            d.text((276, y + 66), sub, font=f(reg, 30), fill=MUTED)
            boxes.append((f"p1-item{i}", "bullet", (160, y, 1600, 110), f"{head}　{sub}"))
        # 顺序刻意「父在前」：卡片是 item 的父层。把它挪后就是 ADR-17 的复现用例。
        # 卡片本身没有文字（纯容器）—— 这是文本匹配的一个真实盲点，实验里要单独算。
        return img, [("p1-title", "title", (116, 84, 900, 90), "本期四件事"),
                     ("p1-card", "shape", (120, 270, 1680, 630), "")] + boxes

    def page2():
        img = Image.new("RGB", (W, H), (0xFF, 0xFF, 0xFF))
        d = ImageDraw.Draw(img)
        d.rectangle([0, 0, W, 8], fill=ACCENT2)
        d.text((120, 96), "压测数据：时间都花在哪", font=f(bld, 62), fill=INK)
        d.text((120, 180), "1080p30，细纹理底图，单位：秒（越短越好）", font=f(reg, 30), fill=MUTED)
        d.rounded_rectangle([120, 260, 1800, 930], radius=24, fill=(0xFA, 0xFB, 0xFD), outline=LINE, width=2)

        # 【真值】柱高 = 值 ÷ 轴上限。
        #
        # 旧版给每根柱子手填了一个 ratio（0.72 / 0.54 / 1.0 / 1.0）：174s 被画成
        # 943s 的 72%，而它实际只有 18%——图上「50s 片和 300s 片差不多贵」，
        # 数据说的正好相反。这张图在撒谎，而当时 20 条判据一条都没发现：
        # 它们量的是「元素在不在、准不准、空不空、动不动」，没有一条量**图里画的对不对**。
        axis_max = 1000
        base_y, plot_top = 850, 300
        plot_h = base_y - plot_top
        bars = [("2 核 / 50s 片", 174, ACCENT),
                ("4 核 / 50s 片", 131, ACCENT),
                ("2 核 / 300s 片", 943, ACCENT2),
                ("2×300s 并发", 948, ACCENT2)]

        # y 轴刻度画在柱子左侧、不横穿柱体：底板是按「元素外环取色」擦除的，
        # 网格线一旦从柱子背后穿过，擦完就在底板上留一道断口，柱子长起来时看得见。
        d.line([236, plot_top, 236, base_y], fill=(0xC8, 0xD2, 0xDE), width=3)
        for tick in (0, 250, 500, 750, 1000):
            y = base_y - round(plot_h * tick / axis_max)
            d.line([224, y, 238, y], fill=(0xC8, 0xD2, 0xDE), width=2)
            d.text((216 - d.textlength(str(tick), font=f(reg, 26)), y - 17),
                   str(tick), font=f(reg, 26), fill=MUTED)
        d.line([236, base_y + 1, 1740, base_y + 1], fill=(0xC8, 0xD2, 0xDE), width=3)

        elems = []
        for i, (label, v, color) in enumerate(bars):
            x = 320 + i * 360
            h = max(2, round(plot_h * v / axis_max))
            top = base_y - h
            # PIL 的矩形两端都含：画到 x+160 会多出第 161 列，而元素框是 160 宽，
            # 多出的那一列留在底板里——柱子生长时，右边会出现一条通到顶的竖线。
            # 同理底边少画一行，基线那一行留给坐标轴。
            d.rounded_rectangle([x, top, x + 159, base_y - 1], radius=8, fill=color)
            d.text((x, top - 58), f"{v}s", font=f(bld, 38), fill=INK)
            d.text((x - 6, base_y + 24), label, font=f(reg, 28), fill=MUTED)
            # 每根柱子是一个独立元素：既能各自入场（念到哪根长哪根），
            # 也能被逐根测量——「柱高与标注值成比例」这条判据需要它。
            elems.append((f"p2-bar{i}", "chart", (x, top, 160, h), f"{v}s",
                          {"value": v, "enter": "grow"}))

        d.text((180, 900), "注：并发用例为两条 5 分钟长片同时提交，统计到最后一个任务收敛",
               font=f(reg, 26), fill=MUTED)
        return img, [("p2-title", "title", (116, 84, 1000, 84), "压测数据：时间都花在哪"),
                     ("p2-chart", "chart", (120, 260, 1680, 670),
                      "y 轴 0–1000 秒；2 核 / 50s 片 174s；4 核 / 50s 片 131s；"
                      "2 核 / 300s 片 943s；2×300s 并发 948s")] + elems

    def page3():
        img = Image.new("RGB", (W, H), (0xFF, 0xFF, 0xFF))
        d = ImageDraw.Draw(img)
        d.text((120, 96), "架构要点", font=f(bld, 62), fill=INK)
        steps = ["PPTX 解析", "版式编译", "IR 计划", "Chrome 合成", "MP4 封装"]
        for i, s in enumerate(steps):
            y = 240 + i * 128
            d.rounded_rectangle([120, y, 760, y + 96], radius=16,
                                fill=(0xEE, 0xF3, 0xFA) if i % 2 == 0 else (0xE8, 0xF4, 0xF2))
            d.text((156, y + 24), f"{i + 1}. {s}", font=f(reg, 34), fill=INK)
            if i < len(steps) - 1:
                d.line([170, y + 96, 170, y + 128], fill=(0xB8, 0xC6, 0xD8), width=3)
        for y in range(240, 840):
            t = (y - 240) / 600
            d.rectangle([860, y, 1800, y + 1],
                        fill=(int(0x18 + 40 * t), int(0x3A + 60 * t), int(0x6A + 80 * t)))
        d.ellipse([980, 380, 1280, 680], fill=(0x2E, 0x6F, 0xB8))
        d.rectangle([1320, 460, 1700, 700], fill=(0x0F, 0x76, 0x6E))
        d.text((1000, 700), "渲染即服务", font=f(bld, 44), fill=(0xFF, 0xFF, 0xFF))
        d.text((120, 900), "图：从解析到封装的五段流水线；每一段都有独立的重试与超时语义",
               font=f(reg, 28), fill=MUTED)
        return img, [("p3-title", "title", (116, 84, 700, 84), "架构要点"),
                     ("p3-flow", "shape", (120, 240, 640, 600),
                      "1. PPTX 解析 2. 版式编译 3. IR 计划 4. Chrome 合成 5. MP4 封装"),
                     ("p3-hero", "image", (860, 240, 940, 600), "渲染即服务"),
                     ("p3-caption", "bullet", (120, 890, 1600, 50),
                      "图：从解析到封装的五段流水线；每一段都有独立的重试与超时语义")]

    def page4():
        img = Image.new("RGB", (W, H))
        bg(img, (0x0E, 0x1B, 0x2E), (0x1B, 0x36, 0x5C))
        d = ImageDraw.Draw(img)
        d.text((120, 240), "谢谢", font=f(bld, 110), fill=(0xFF, 0xFF, 0xFF))
        d.text((124, 400), "问题请直接提，数据都可以复现", font=f(reg, 40), fill=(0xC9, 0xD6, 0xE8))
        d.rectangle([124, 490, 620, 496], fill=(0x4C, 0x9A, 0xD8))
        d.text((124, 560), "生僻字与符号样张：喆 堃 犇 垚 玥 珺 赟 燚 弢 韡 祎",
               font=f(reg, 32), fill=(0xE6, 0xEE, 0xF8))
        d.text((124, 620), "符号：← → ⇒ α β π ￥ € ① ② 《》【】——“引号”……",
               font=f(reg, 32), fill=(0xE6, 0xEE, 0xF8))
        d.text((124, 760), "联系：li.zhe@example.com　·　分机 8021", font=f(reg, 30), fill=(0xA9, 0xBC, 0xD4))
        return img, [("p4-title", "title", (116, 228, 700, 140), "谢谢"),
                     ("p4-note", "bullet", (120, 548, 1500, 120),
                      "生僻字与符号样张：喆 堃 犇 垚 玥 珺 赟 燚 弢 韡 祎 "
                      "符号：← → ⇒ α β π ￥ € ① ② 《》【】——“引号”……"),
                     ("p4-contact", "bullet", (120, 748, 1200, 60),
                      "联系：li.zhe@example.com · 分机 8021")]

    os.makedirs(os.path.join(args.out, "pages"), exist_ok=True)
    only = {int(x) for x in args.only.split(",") if x.strip()}
    meta = []
    if only:
        # 只改部分页时沿用已有的 elements.json：其余页的元素清单是量过的输入。
        prev_path = os.path.join(args.out, "elements.json")
        if not os.path.exists(prev_path):
            raise SystemExit(f"--only 需要已存在的 {prev_path}")
        with open(prev_path, encoding="utf-8") as fh:
            meta = json.load(fh)
        meta = [m for m in meta if m["index"] not in only]
    for i, fn in enumerate([page0, page1, page2, page3, page4]):
        if only and i not in only:
            continue
        img, elems = fn()
        rel = f"pages/p{i:03d}.png"
        img.save(os.path.join(args.out, rel))
        items = []
        for e in elems:
            item = {"id": e[0], "kind": e[1], "box": list(e[2]), "text": e[3]}
            # 第 5 项是可选附加字段：value（图表的数值，供「柱高成比例」判据）
            # 与 enter（入场方式，如柱子用 grow）。
            if len(e) > 4:
                item.update(e[4])
            items.append(item)
        meta.append({"index": i, "slideId": f"slide-{i}", "baseImage": rel,
                     "elements": items})
    meta.sort(key=lambda m: m["index"])
    with open(os.path.join(args.out, "elements.json"), "w", encoding="utf-8") as fh:
        json.dump(meta, fh, ensure_ascii=False, indent=1)
    print(f"pages={len(meta)} out={args.out}")


if __name__ == "__main__":
    main()
