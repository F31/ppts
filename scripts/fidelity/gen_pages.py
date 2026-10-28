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
    args = ap.parse_args()

    reg = os.path.join(args.fonts, "NotoSansCJKsc-Regular.otf")
    bld = os.path.join(args.fonts, "NotoSansCJKsc-Bold.otf")
    for p in (reg, bld):
        if not os.path.exists(p):
            raise SystemExit(f"缺字体：{p}（从镜像 docker cp 出来）")

    def f(path, size):
        return ImageFont.truetype(path, size)

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
        return img, [
            ("p0-eyebrow", "bullet", (120, 200, 900, 60)),
            ("p0-title", "title", (116, 288, 1320, 130)),
            ("p0-sub", "bullet", (120, 440, 1500, 70)),
            ("p0-meta", "bullet", (120, 608, 1200, 110)),
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
            boxes.append((f"p1-item{i}", "bullet", (160, y, 1600, 110)))
        # 顺序刻意「父在前」：卡片是 item 的父层。把它挪后就是 ADR-17 的复现用例。
        return img, [("p1-title", "title", (116, 84, 900, 90)),
                     ("p1-card", "shape", (120, 270, 1680, 630))] + boxes

    def page2():
        img = Image.new("RGB", (W, H), (0xFF, 0xFF, 0xFF))
        d = ImageDraw.Draw(img)
        d.rectangle([0, 0, W, 8], fill=ACCENT2)
        d.text((120, 96), "压测数据：时间都花在哪", font=f(bld, 62), fill=INK)
        d.text((120, 180), "1080p30，细纹理底图，单位：秒（越短越好）", font=f(reg, 30), fill=MUTED)
        d.rounded_rectangle([120, 260, 1800, 930], radius=24, fill=(0xFA, 0xFB, 0xFD), outline=LINE, width=2)
        data = [("2 核 / 50s 片", 174, 0.72), ("4 核 / 50s 片", 131, 0.54),
                ("2 核 / 300s 片", 943, 1.0), ("2×300s 并发", 948, 1.0)]
        base_y, max_h = 850, 420
        for i, (label, v, ratio) in enumerate(data):
            x = 220 + i * 400
            h = int(max_h * ratio)
            d.rounded_rectangle([x, base_y - h, x + 200, base_y], radius=8,
                                fill=ACCENT if i < 2 else ACCENT2)
            d.text((x, base_y - h - 62), f"{v}s", font=f(bld, 38), fill=INK)
            d.text((x - 6, base_y + 24), label, font=f(reg, 28), fill=MUTED)
        d.line([180, base_y + 4, 1740, base_y + 4], fill=(0xC8, 0xD2, 0xDE), width=2)
        d.text((180, 900), "注：并发用例为两条 5 分钟长片同时提交，统计到最后一个任务收敛",
               font=f(reg, 26), fill=MUTED)
        return img, [("p2-title", "title", (116, 84, 1000, 84)),
                     ("p2-chart", "chart", (120, 260, 1680, 670))]

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
        return img, [("p3-title", "title", (116, 84, 700, 84)),
                     ("p3-flow", "shape", (120, 240, 640, 600)),
                     ("p3-hero", "image", (860, 240, 940, 600)),
                     ("p3-caption", "bullet", (120, 890, 1600, 50))]

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
        return img, [("p4-title", "title", (116, 228, 700, 140)),
                     ("p4-note", "bullet", (120, 548, 1500, 120)),
                     ("p4-contact", "bullet", (120, 748, 1200, 60))]

    os.makedirs(os.path.join(args.out, "pages"), exist_ok=True)
    meta = []
    for i, fn in enumerate([page0, page1, page2, page3, page4]):
        img, elems = fn()
        rel = f"pages/p{i:03d}.png"
        img.save(os.path.join(args.out, rel))
        meta.append({"index": i, "slideId": f"slide-{i}", "baseImage": rel,
                     "elements": [{"id": e[0], "kind": e[1], "box": e[2]} for e in elems]})
    with open(os.path.join(args.out, "elements.json"), "w", encoding="utf-8") as fh:
        json.dump(meta, fh, ensure_ascii=False, indent=1)
    print(f"pages={len(meta)} out={args.out}")


if __name__ == "__main__":
    main()
