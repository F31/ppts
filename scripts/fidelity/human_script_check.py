#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Q7 真人讲稿重测：输入契约闸门 + 一键评测 + 报告对账。

## 为什么不能直接跑 bind_elements.py

README 末节写「素材到位即可一键闭环」，但绑定器对**输入写错**是静默的，
而这里量的是「0.972 有没有自证」，一个被静默改小的分母足以把结论反过来：

  · `main()` 用 `zip(elements_doc, seg_doc["pages"], gold_doc["pages"])` 按位置配对，
    两份页数不等时**静默截断**，照样打印一个看起来正常的召回数字；
  · `compare()` 只遍历 `elements`，金标里写错一个 `elementId`（`p1-s5` 写成 `p1-s05`）
    会被**静默忽略**：分母少一个，召回反而变好看；
  · `golden_binding()` 用 dict 按 elementId 去重，同一元素被标到两段时后者覆盖前者，
    不报错；
  · 段里没有 `bind` 时 `recall_content` 是 `None`，输出「召回 None」，容易被误读。

本脚本在调用绑定器**之前**挡住上述输入问题，在拿到报告**之后**再对一次账，
确保「金标里的每一条，都真的进了这次计算」。绑定器本身一行不改，q8 的
0.972 / 0.962 逐位不变。

## 用法

    # 自检（离线合成夹具，不需要任何 tmp 产物；CI 可跑）
    python3 scripts/fidelity/human_script_check.py --self-test

    # 真人素材到位后一键跑（--segments 与 --golden 强制同源，就是这一份文件）
    python3 scripts/fidelity/human_script_check.py \
        --script human-script.json --elements elements.json \
        --origin "2026-11-06 由 X 对照 5 页页面逐段标注" \
        --out tmp/q8/human-bind-report.json

自检用的合成夹具**只验判据**，不是真人稿、不是实验结果，绝不写回路线图。
"""
import argparse
import copy
import json
import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
# 结构词尺子与消融实验共用一份：这里量的是「讲稿有没有把版面念出来」，
# 用两把尺子会各自漂移。（消融脚本刻意不共用被测实现，那是为了不跟着改尺子；
# 本脚本是第三方检查者，不存在这个问题。）
from ablate_structure import PATTERNS  # noqa: E402

BINDER = HERE / "bind_elements.py"
EXPECTED_BAND = (0.67, 0.75)


# ---------------------------------------------------------------- 输入侧校验

def golden_counts(script):
    """返回（全局唯一金标数, 每页唯一金标数）。

    与 golden_binding() 的 dict 语义一致：同一个 elementId 只算一条。
    """
    seen = set()
    per_page = []
    for pg in script.get("pages", []):
        page_seen = set()
        for s in pg.get("segments", []):
            for b in s.get("bind", []) or []:
                if isinstance(b, dict) and isinstance(b.get("elementId"), str):
                    eid = b["elementId"]
                    seen.add(eid)
                    page_seen.add(eid)
        per_page.append(len(page_seen))
    return len(seen), per_page


def validate_inputs(script, elements):
    """返回 [(错误码, 说明)]；空列表表示这份素材可以进评测。"""
    errs = []
    if not isinstance(script, dict) or not isinstance(script.get("pages"), list) \
            or not script["pages"]:
        return [("SCRIPT_SHAPE", "讲稿必须是含非空 pages 数组的对象")]
    if not isinstance(elements, list) or not elements:
        return [("ELEMENTS_SHAPE", "元素清单必须是非空数组（extract_pptx_elements.py 的输出）")]

    sp = script["pages"]
    if len(sp) != len(elements):
        errs.append(("PAGE_COUNT",
                     "讲稿 %d 页 vs 元素清单 %d 页。绑定器用 zip 按位置配对，"
                     "页数不等会静默截断，必须先对齐" % (len(sp), len(elements))))

    by_index = {}
    for i, ep in enumerate(elements):
        if not isinstance(ep, dict) or not isinstance(ep.get("elements"), list):
            errs.append(("ELEMENTS_SHAPE", "元素清单第 %d 项不是含 elements 数组的对象" % i))
            continue
        idx = ep.get("index")
        if idx in by_index:
            errs.append(("ELEMENTS_INDEX_DUP", "元素清单里 index=%r 出现两次" % idx))
        by_index[idx] = ep
        for e in ep["elements"]:
            if not isinstance(e, dict) or not isinstance(e.get("id"), str) or not e["id"]:
                errs.append(("ELEMENT_SHAPE", "第 %d 页有元素缺 id" % i))
                break

    seen_seg = set()
    seen_gold = set()
    for i, pg in enumerate(sp):
        if not isinstance(pg, dict) or not isinstance(pg.get("segments"), list) \
                or not pg["segments"]:
            errs.append(("SEGMENTS_SHAPE", "讲稿第 %d 页不是含非空 segments 数组的对象" % i))
            continue
        idx = pg.get("index")
        if idx not in by_index:
            errs.append(("PAGE_INDEX",
                         "讲稿第 %d 页 index=%r 在元素清单里找不到（清单位置 %s）"
                         % (i, idx, [e.get("index") for e in elements])))
            page_ids = set()
        else:
            ep = by_index[idx]
            page_ids = {e["id"] for e in ep["elements"]
                        if isinstance(e, dict) and isinstance(e.get("id"), str)}
            # 绑定器按位置 zip，不是按 index 查。两份顺序不一致会算到别的页上，
            # 即使页数相同、index 也都存在。
            if i < len(elements) and elements[i].get("index") != idx:
                errs.append(("PAGE_ORDER",
                             "讲稿第 %d 页 index=%r，但元素清单同一位置是 index=%r；"
                             "绑定器按位置配对，顺序必须一致"
                             % (i, idx, elements[i].get("index"))))
            if isinstance(ep.get("slideId"), str) and isinstance(pg.get("slideId"), str) \
                    and ep["slideId"] != pg["slideId"]:
                errs.append(("SLIDE_ID", "第 %d 页 slideId 不一致：讲稿 %s / 清单 %s"
                             % (i, pg["slideId"], ep["slideId"])))

        for s in pg["segments"]:
            if not isinstance(s, dict):
                errs.append(("SEGMENT_SHAPE", "第 %d 页有段不是对象" % i))
                continue
            sid = s.get("id")
            if not isinstance(sid, str) or not sid.strip():
                errs.append(("SEGMENT_ID", "第 %d 页有段缺非空 id" % i))
            elif sid in seen_seg:
                errs.append(("SEGMENT_ID_DUP", "段 id %r 重复" % sid))
            else:
                seen_seg.add(sid)
            text = s.get("text")
            if not isinstance(text, str) or not text.strip():
                errs.append(("SEGMENT_TEXT", "段 %r 的 text 不是非空字符串" % sid))
            binds = s.get("bind")
            if not isinstance(binds, list) or not binds:
                errs.append(("BIND_MISSING",
                             "段 %r 没有 bind 金标。没有金标就算不出召回，这一份不能省" % sid))
                continue
            for b in binds:
                if not isinstance(b, dict) or not isinstance(b.get("elementId"), str) \
                        or not b["elementId"]:
                    errs.append(("BIND_SHAPE", "段 %r 的 bind 项缺 elementId" % sid))
                    continue
                eid = b["elementId"]
                if eid not in page_ids:
                    errs.append(("GOLDEN_ID_UNKNOWN",
                                 "段 %r 的金标 %r 不在同页元素清单里；绑定器会把这条静默丢掉，"
                                 "分母变小、召回虚高" % (sid, eid)))
                if eid in seen_gold:
                    errs.append(("GOLDEN_ID_DUP",
                                 "金标 %r 被标到两段；golden_binding 用 dict 覆盖，只算一条" % eid))
                seen_gold.add(eid)
    return errs


# ---------------------------------------------------------------- 报告侧对账

def reconcile(report, script, elements):
    """报告里的每一页、每个元素、每条金标都真的进了计算吗？"""
    errs = []
    ov = report.get("overall") or {}
    pages = report.get("pages") or []
    total_gold, per_page_gold = golden_counts(script)
    total_elems = sum(len(ep.get("elements") or [])
                      for ep in elements if isinstance(ep, dict))

    if report.get("rules") != "v2":
        errs.append(("REPORT_RULES", "报告 rules=%r，重测必须用 v2" % report.get("rules")))
    if len(pages) != len(script["pages"]):
        errs.append(("REPORT_PAGE_COUNT",
                     "报告只有 %d 页、输入有 %d 页 —— zip 静默截断了" %
                     (len(pages), len(script["pages"]))))
    if ov.get("elements") != total_elems:
        errs.append(("REPORT_ELEMENT_COUNT",
                     "报告算了 %r 个元素、清单里有 %d 个" % (ov.get("elements"), total_elems)))
    if ov.get("need") != total_gold:
        errs.append(("REPORT_GOLDEN_COUNT",
                     "报告只认了 %r 条金标、输入有 %d 条 —— 有金标被静默丢掉" %
                     (ov.get("need"), total_gold)))
    if ov.get("recall_content") is None or not ov.get("need_content"):
        errs.append(("REPORT_NO_CONTENT_RECALL",
                     "仅内容元素召回是 None / 没有内容金标，这个数不能写回路线图"))
    for i, p in enumerate(pages):
        if i >= len(per_page_gold):
            break
        got = len(p.get("golden") or {})
        if got != per_page_gold[i]:
            errs.append(("REPORT_PAGE_GOLDEN",
                         "报告第 %d 页认了 %d 条金标、输入该页有 %d 条" %
                         (i, got, per_page_gold[i])))
            break
    return errs


# ---------------------------------------------------------------- 自证风险

def self_evidence_risk(script, elements):
    """只提示、不判红：真人口播也可能自然说「第一」，硬拒绝会赶走真素材。

    但「讲稿把版面结构念出来」正是 0.972 的来源，必须让它在结论里显形，
    否则一份复述式讲稿跑出 0.97 会被当成「真人稿也这么高」的证据。
    """
    texts = {}
    for ep in elements:
        if isinstance(ep, dict):
            for e in ep.get("elements") or []:
                if isinstance(e, dict) and isinstance(e.get("id"), str):
                    texts[e["id"]] = (e.get("text") or "").strip()
    segs = [s for pg in script["pages"] for s in pg.get("segments", [])
            if isinstance(s, dict)]
    structure_segs = structure_hits = verbatim_segs = 0
    for s in segs:
        t = s.get("text") or ""
        n = sum(len(rx.findall(t)) for rx in PATTERNS.values())
        structure_hits += n
        if n:
            structure_segs += 1
        flat = re.sub(r"\s+", "", t)
        for b in s.get("bind") or []:
            if not isinstance(b, dict):
                continue
            et = re.sub(r"\s+", "", texts.get(b.get("elementId"), ""))
            if len(et) >= 4 and et in flat:
                verbatim_segs += 1
                break
    ratio = structure_segs / len(segs) if segs else 0.0
    if ratio >= 0.3 or verbatim_segs:
        level = "high"
    elif ratio > 0:
        level = "medium"
    else:
        level = "low"
    return {"segments": len(segs), "structure_hit_segments": structure_segs,
            "structure_hits": structure_hits, "verbatim_segments": verbatim_segs,
            "structure_segment_ratio": round(ratio, 3), "level": level}


def conclusion(report, script, origin):
    """给人工写回路线图用的字段。数字一律取自报告，不在这里重算。"""
    ov = report.get("overall") or {}
    rc = ov.get("recall_content")
    lo, hi = EXPECTED_BAND
    return {"golden_source": origin,
            "pages": len(script["pages"]),
            "segments": sum(len(pg.get("segments", [])) for pg in script["pages"]),
            "golden_binds": golden_counts(script)[0],
            "recall_content": rc, "precision": ov.get("precision"),
            "expected_band": [lo, hi],
            "in_expected_band": bool(rc is not None and lo <= rc <= hi),
            "note": "结论须由人核对金标来源后写回路线图 Q7，不能只搬一个数字"}


# ---------------------------------------------------------------- 一键入口

def run(args):
    script_path = Path(args.script)
    elements_path = Path(args.elements)
    for p in (script_path, elements_path):
        if not p.is_file():
            print("缺文件：%s" % p, file=sys.stderr)
            return 2
    try:
        script = json.loads(script_path.read_text(encoding="utf-8"))
        elements = json.loads(elements_path.read_text(encoding="utf-8"))
    except (ValueError, OSError) as e:
        print("JSON 读不了：%s" % e, file=sys.stderr)
        return 2

    errs = validate_inputs(script, elements)
    if errs:
        print("== 输入契约不通过（%d 条），拒绝评测 ==" % len(errs))
        for code, msg in errs:
            print("  %-22s %s" % (code, msg))
        print("\n修好素材再跑。绑定器对写错的金标是静默的，不能让它算出一个好看的假数。")
        return 1

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    # segments 与 golden 强制同一份：文字作输入、bind 作金标，不同源就没法解释结果。
    cmd = [sys.executable, str(BINDER), "--rules", "v2",
           "--segments", str(script_path), "--golden", str(script_path),
           "--elements", str(elements_path), "--out", str(out)]
    proc = subprocess.run(cmd, text=True, capture_output=True)
    if proc.returncode != 0:
        print("绑定器失败（exit %d）：\n%s\n%s" %
              (proc.returncode, proc.stdout, proc.stderr), file=sys.stderr)
        return 1
    print(proc.stdout.strip())

    report = json.loads(out.read_text(encoding="utf-8"))
    errs = reconcile(report, script, elements)
    if errs:
        print("\n== 报告对账不通过（%d 条），这份报告不可信 ==" % len(errs))
        for code, msg in errs:
            print("  %-22s %s" % (code, msg))
        return 1

    risk = self_evidence_risk(script, elements)
    origin = args.origin or script.get("说明") or "（未标注来源：写回路线图前必须补「谁标的、看什么标的」）"
    concl = conclusion(report, script, origin)
    report["human_check"] = {
        "input": {"script": str(script_path), "elements": str(elements_path),
                  "segments_are_golden": True, "rules": "v2"},
        "input_contract": "pass", "reconcile": "pass",
        "self_evidence_risk": risk, "conclusion": concl,
    }
    out.write_text(json.dumps(report, ensure_ascii=False, indent=1), encoding="utf-8")

    lo, hi = EXPECTED_BAND
    print("\n=== 真人讲稿重测结论（需人工核对后写回路线图 Q7）===")
    print("金标来源：%s" % concl["golden_source"])
    print("样本：%d 页 / %d 段 / %d 条金标" %
          (concl["pages"], concl["segments"], concl["golden_binds"]))
    print("仅内容元素召回 %s，精确 %s" % (concl["recall_content"], concl["precision"]))
    print("预期区间 %.2f–%.2f：%s" %
          (lo, hi, "落在区间内" if concl["in_expected_band"] else "未落在区间内"))
    print("自证风险 %s：%d/%d 段带结构词（命中 %d 处）、%d 段照抄元素文字" %
          (risk["level"], risk["structure_hit_segments"], risk["segments"],
           risk["structure_hits"], risk["verbatim_segments"]))
    if risk["level"] == "high":
        print("⚠ 这份稿把版面结构念出来了（或复述了元素文字）：接近 0.97 更可能是自证，")
        print("  不能当作「真人稿也这么高」的证据；请核对讲稿是不是顺着页面写的。")
    if not args.origin and not script.get("说明"):
        print("⚠ 金标来源未标注，写回路线图前必须补：谁标的、对照什么标的。")
    print("报告：%s" % out)
    return 0


# ---------------------------------------------------------------- 自检

def make_fixture():
    """合成夹具：2 页 / 3 段 / 4 条金标。只验判据，不是实验结果。"""
    elements = [
        {"index": 0, "slideId": "slide-0", "elements": [
            {"id": "p1-s1", "text": "市场份额", "kind": "text"},
            {"id": "p1-s2", "text": "竞争格局表", "kind": "text"},
            {"id": "p1-s3", "text": "四条结论", "kind": "text"}]},
        {"index": 1, "slideId": "slide-1", "elements": [
            {"id": "p2-s1", "text": "华东 174", "kind": "text"},
            {"id": "p2-s2", "text": "华北 131", "kind": "text"}]},
    ]
    script = {"pages": [
        {"index": 0, "slideId": "slide-0", "segments": [
            {"id": "s0-1", "text": "先看整体盘子。", "bind": [{"elementId": "p1-s1"}]},
            {"id": "s0-2", "text": "几家摆在一起比。", "bind": [{"elementId": "p1-s2"}]}]},
        {"index": 1, "slideId": "slide-1", "segments": [
            {"id": "s1-1", "text": "华东和华北差得不多。",
             "bind": [{"elementId": "p2-s1"}, {"elementId": "p2-s2"}]}]},
    ]}
    return script, elements


def make_report(script, elements):
    """与 bind_elements.py 同形的报告，供对账判据离线自检。"""
    total_gold, _ = golden_counts(script)
    total_elems = sum(len(ep["elements"]) for ep in elements)
    pages = []
    for pg in script["pages"]:
        g = {}
        for s in pg["segments"]:
            for b in s.get("bind", []):
                g[b["elementId"]] = s["id"]
        pages.append({"index": pg["index"], "golden": g, "summary": {}})
    return {"rules": "v2",
            "overall": {"elements": total_elems, "need": total_gold,
                        "need_content": total_gold, "recall_content": 0.75,
                        "precision": 0.8},
            "pages": pages}


def self_test():
    ok = total = 0

    def expect(label, cond, detail=""):
        nonlocal ok, total
        total += 1
        if cond:
            ok += 1
            print("  ok   %s" % label)
        else:
            print("  FAIL %s  %s" % (label, detail))

    def codes(errs):
        return [e[0] for e in errs]

    def has(errs, code):
        return code in codes(errs)

    script, elements = make_fixture()
    report = make_report(script, elements)

    # 沉默守卫：正例必须真绿，且结构词尺子必须真的会命中。
    expect("正例：输入校验全绿", not validate_inputs(script, elements),
           str(codes(validate_inputs(script, elements))))
    expect("正例：报告对账全绿", not reconcile(report, script, elements),
           str(codes(reconcile(report, script, elements))))
    # 夹具文案刻意不含结构词：PATTERNS 里的 spatial 吃「这张表」、count 吃「一块儿」，
    # 随手写一句口语就会让负例变成 high，下面的沉默守卫会立刻红。
    risky = copy.deepcopy(script)
    risky["pages"][0]["segments"][0]["text"] = "第一步看左边这张表，第二部分有四条结论。"
    risk_pos = self_evidence_risk(risky, elements)
    risk_neg = self_evidence_risk(script, elements)
    expect("正例：结构词尺子能命中、且不误报",
           risk_pos["structure_hit_segments"] > 0 and risk_pos["level"] == "high"
           and risk_neg["structure_hit_segments"] == 0 and risk_neg["level"] == "low",
           "pos=%s neg=%s" % (risk_pos, risk_neg))

    def mut_input(fn, code, label):
        s, e = copy.deepcopy(script), copy.deepcopy(elements)
        fn(s, e)
        errs = validate_inputs(s, e)
        expect(label, has(errs, code), str(codes(errs)))

    def mut_report(fn, code, label):
        r = copy.deepcopy(report)
        fn(r)
        errs = reconcile(r, script, elements)
        expect(label, has(errs, code), str(codes(errs)))

    mut_input(lambda s, e: e.pop(), "PAGE_COUNT", "页数不等必须红（zip 会静默截断）")
    mut_input(lambda s, e: e.reverse(), "PAGE_ORDER", "两份顺序不一致必须红（按位置 zip）")
    mut_input(lambda s, e: s["pages"][1].update(index=9), "PAGE_INDEX", "讲稿页 index 找不到必须红")
    mut_input(lambda s, e: e[1].update(index=0), "ELEMENTS_INDEX_DUP", "元素清单 index 重复必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][1].update(id=""),
              "SEGMENT_ID", "段缺 id 必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][1].update(id="s0-1"),
              "SEGMENT_ID_DUP", "段 id 重复必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][0].update(text="  "),
              "SEGMENT_TEXT", "段 text 空必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][0].pop("bind"),
              "BIND_MISSING", "段没有金标必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][0].update(bind=["p1-s1"]),
              "BIND_SHAPE", "bind 项不是对象必须红")
    mut_input(lambda s, e: s["pages"][0]["segments"][0]["bind"][0].update(elementId="p1-s01"),
              "GOLDEN_ID_UNKNOWN", "金标 id 拼错必须红（否则召回虚高）")
    mut_input(lambda s, e: s["pages"][0]["segments"][1]["bind"].append({"elementId": "p1-s1"}),
              "GOLDEN_ID_DUP", "同一元素标到两段必须红")

    mut_report(lambda r: r["pages"].pop(), "REPORT_PAGE_COUNT", "报告少一页必须红")
    mut_report(lambda r: r["overall"].update(elements=3), "REPORT_ELEMENT_COUNT",
               "报告元素数不符必须红")
    mut_report(lambda r: r["overall"].update(need=3), "REPORT_GOLDEN_COUNT",
               "报告少认一条金标必须红")
    mut_report(lambda r: r["pages"][1]["golden"].pop("p2-s2"), "REPORT_PAGE_GOLDEN",
               "报告某页少认金标必须红")
    mut_report(lambda r: r["overall"].update(recall_content=None), "REPORT_NO_CONTENT_RECALL",
               "仅内容召回为 None 必须红")
    mut_report(lambda r: r.update(rules="base"), "REPORT_RULES", "跑错规则档必须红")

    c = conclusion(report, script, "谁标的、看什么标的")
    expect("结论带齐金标来源/页数/召回/预期区间",
           all(k in c for k in ("golden_source", "pages", "segments", "golden_binds",
                                "recall_content", "precision", "expected_band",
                                "in_expected_band"))
           and c["golden_binds"] == golden_counts(script)[0])
    r2 = copy.deepcopy(report)
    r2["overall"]["recall_content"] = 0.42
    c2 = conclusion(r2, script, "x")
    expect("结论数字取自报告而不是硬编码",
           c2["recall_content"] == 0.42 and c2["in_expected_band"] is False)
    r3 = copy.deepcopy(report)
    r3["overall"]["recall_content"] = 0.70
    expect("落在预期区间时标记为真", conclusion(r3, script, "x")["in_expected_band"] is True)

    print("\n%d/%d 通过" % (ok, total))
    return 0 if ok == total else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[1])
    ap.add_argument("--script", help="真人讲稿 human-script.json（文字 + 人工 bind 金标）")
    ap.add_argument("--elements", help="该 deck 的元素清单 elements.json")
    ap.add_argument("--out", default="tmp/q8/human-bind-report.json")
    ap.add_argument("--origin", default="",
                    help="金标来源说明（谁标的、看什么标的），写回路线图要带")
    ap.add_argument("--self-test", action="store_true", help="离线自检判据，不需要素材")
    args = ap.parse_args()
    if args.self_test:
        return self_test()
    if not args.script or not args.elements:
        ap.error("需要 --script 与 --elements（或 --self-test）")
    return run(args)


if __name__ == "__main__":
    sys.exit(main())
