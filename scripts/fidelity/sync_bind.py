#!/usr/bin/env python3
"""把讲解脚本里的 `bind` 与 `focus` 同步进 narrate.py 产出的 segments.json。

## 为什么需要这一步

`narrate.py` 一次性做三件事：TTS、量时长、把脚本里的 `bind` 抄进 segments.json。
plan 编译器只认 segments.json（它要的是实测算出的 `startMs`），所以**改了绑定但没改
讲解词**时，正常路径是整条重跑一遍——为了一处 `offsetMs` 重打 15 段 TTS，
既慢又会让已有成片的音频基线漂移（同一句重合成一次，时长不一定分毫不差）。

这个脚本只做那次「抄写」，前提是**文本一字未变**：段 id 对不上、文本对不上就报错退出，
不做任何猜测。时间戳（startMs/endMs/durationMs）一律不动——它们描述的是已经生成的音频。

    python3 scripts/fidelity/sync_bind.py \
        --script scripts/fidelity/q7-script.json \
        --segments tmp/q7/segments.json           # 原地改，先备份

    python3 scripts/fidelity/sync_bind.py ... --out tmp/q7/segments-d.json  # 写副本
"""
import argparse
import json
import sys


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def binds_of(doc):
    """{(页序, 段 id): bind}，页序也参与键：段 id 只在页内唯一。"""
    out = {}
    for page in doc["pages"]:
        for seg in page["segments"]:
            out[(page["index"], seg["id"])] = seg
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--script", required=True, help="q7-script.json（绑定的来源）")
    ap.add_argument("--segments", required=True, help="segments.json（plan 的输入）")
    ap.add_argument("--out", help="写到新文件；不给就原地改")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    script = load(args.script)
    seg = load(args.segments)
    src = binds_of(script)
    dst = binds_of(seg)

    if set(src) != set(dst):
        only_s = sorted(set(src) - set(dst))
        only_d = sorted(set(dst) - set(src))
        print("段集合不一致：脚本多 %s；segments 多 %s" % (only_s, only_d), file=sys.stderr)
        return 1

    changed = []
    for key in sorted(src):
        s, d = src[key], dst[key]
        if s["text"] != d["text"]:
            print("段 %s 的文本不一致，拒绝同步：\n  脚本: %s\n  音频: %s"
                  % (key[1], s["text"], d["text"]), file=sys.stderr)
            return 1
        old = [(b["elementId"], b.get("offsetMs", 0)) for b in d.get("bind", [])]
        new = [(b["elementId"], b.get("offsetMs", 0)) for b in s.get("bind", [])]
        if old != new:
            changed.append((key[1], old, new))
            d["bind"] = s.get("bind", [])
        # focus 与 bind 并列抄写：前者决定「讲它时谁让位」，后者决定「它何时出场」。
        # 漏抄 focus 不会报错，只会让这页回到「铺满之后不再变化」——那正是要修的病。
        if s.get("focus") or d.get("focus"):
            want = s.get("focus", "")
            if d.get("focus", "") != want:
                changed.append((key[1], "focus=%r" % d.get("focus", ""), "focus=%r" % want))
                if want:
                    d["focus"] = want
                else:
                    d.pop("focus", None)
        # focusAt 是句内切换点（相对本段开口）。它与 focus 一样只影响「谁让位」，
        # 不影响任何时间戳；漏抄同样不报错，只是那句 3 秒的话里画面不动了。
        if s.get("focusAt") or d.get("focusAt"):
            want = s.get("focusAt", [])
            if d.get("focusAt", []) != want:
                changed.append((key[1], "focusAt=%r" % d.get("focusAt", []), "focusAt=%r" % want))
                if want:
                    d["focusAt"] = want
                else:
                    d.pop("focusAt", None)

    # 页级「常驻」清单也一并抄：它是编排的一部分，和 bind 一样属于脚本，
    # 但不体现在 segments.json 的段落里——漏抄就等于没生效，而且没有任何报错。
    spages = {p["index"]: p for p in script["pages"]}
    for page in seg["pages"]:
        want = spages[page["index"]].get("static", [])
        if page.get("static", []) != want:
            print("  page%d static: %s → %s" % (page["index"], page.get("static", []), want))
            changed.append(("static", page.get("static", []), want))
            if want:
                page["static"] = want
            else:
                page.pop("static", None)

    for sid, old, new in changed:
        print("  %-6s %s → %s" % (sid, old, new))
    print("同步 %d 段（共 %d 段），文本与时间戳未动" % (len(changed), len(src)))

    if args.dry_run:
        return 0
    text = json.dumps(seg, ensure_ascii=False, indent=1) + "\n"
    with open(args.out or args.segments, "w", encoding="utf-8") as f:
        f.write(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
