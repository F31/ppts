#!/usr/bin/env python3
"""Q7 实验：把讲解词合成为音频与页内时间轴。

产出的 `segments.json` 同时是两件事的输入：
  1) `scripts/fidelity/plan -script` 用它把 clip 的 trigger 写成 `withSegment:<id>`；
  2) 音轨用它按 startMs 摆放，于是「段落的开口」在音轨与视频里是同一个时刻。

四个刻意为之的口径：

- **逐段合成，而不是整页合成后再切**。整页音频只能靠 VAD 或强制对齐切段，误差在
  几十到几百毫秒；逐段合成让段落边界就是拼接点，误差只剩编码器的一帧。
- **裁掉首尾静音**。edge-tts 的 mp3 前面自带约 100ms 静音（字幕第一句落在 0.100s）。
  不裁的话，元素按 startMs 出现、声音却晚 100ms 才起——3 帧，正好踩在「不滞后」的
  判据线上，测出来的是 TTS 的头部静音，不是渲染器的偏差。
- **段间留 150ms、页末留 700ms**。不留间隙的连续朗读听起来像机器报数；
  700ms 页末既是呼吸，也是「页切换前最后一帧」的观察窗。
- **顺手存逐词边界**（`words.json`）。编排里的「讲到 174 时这根柱子才长」不该靠耳朵
  调 `offsetMs`：手调出来的数没有人能复核，改一个字就得重调。TTS 自己就知道每个词
  落在哪一毫秒，把它存下来，绑定就能写成 `atText: "一百七十四"`（见 `sync_bind.py`）。
  注意 CLI 的 `--write-subtitles` 对中文只出整句一条，逐词要显式
  `boundary="WordBoundary"` 走 Python API——所以这里不调 CLI。
"""

import argparse
import json
import os
import subprocess
import sys
import time
import wave

SAMPLE_RATE = 24000
TRIM_HEAD = ("silenceremove=start_periods=1:start_duration=0.02:"
             "start_threshold=-45dB")
TRIM_TAIL = "areverse," + TRIM_HEAD + ",areverse"


def run(cmd):
    subprocess.run(cmd, check=True)


def synth(voice, rate, text, mp3, attempts=4):
    """合成一段，返回逐词边界 [{tMs, dMs, text}]（相对 mp3 起点，含头部静音）。

    空音频是服务端偶发（同一句重试即成功），不是参数错，直接重试。
    edge-tts 把「服务端没回音频」报成 `NoAudioReceived: Please verify that your
    parameters are correct`——按字面去调参数会越调越远：同一句换个时间点再发就成功。
    所以这里是退避重试，不改文本、不改语速——改语速会改变段落时长，而时长正是
    这次实验要测的量。
    """
    try:
        import asyncio

        import edge_tts  # 只有 venv 里有：纯 Python 的 edge-tts 客户端
    except ImportError as exc:  # pragma: no cover - 环境问题，不是逻辑分支
        raise SystemExit("需要 edge-tts 的 Python 包（逐词边界只能走 API）：%s" % exc)

    async def once():
        comm = edge_tts.Communicate(text, voice, rate=rate, boundary="WordBoundary")
        buf, words = bytearray(), []
        async for ch in comm.stream():
            kind = ch["type"]
            if kind == "audio":
                buf.extend(ch["data"])
            elif kind == "WordBoundary":
                # offset/duration 的单位是 100ns
                words.append({"tMs": round(ch["offset"] / 10000),
                              "dMs": round(ch["duration"] / 10000),
                              "text": ch["text"]})
        return bytes(buf), words

    last = None
    for i in range(attempts):
        try:
            audio, words = asyncio.run(once())
            if len(audio) > 1024:
                with open(mp3, "wb") as fh:
                    fh.write(audio)
                if not words:
                    raise SystemExit(
                        "TTS 没有返回逐词边界：%r。没有它，atText 绑定无从解析，"
                        "不能静默退化成手调偏移" % text)
                return words
            last = "empty audio"
        except SystemExit:
            raise
        except Exception as exc:  # 网络/服务端抖动，退避重试
            last = exc
        time.sleep(0.4 * (2 ** i))
    raise SystemExit(f"tts failed for {text!r}: {last}")


def trim_to_wav(ffmpeg, ffprobe, mp3, work_wav, head_wav, wav):
    """解码 → 裁头 → 裁尾，返回 (裁掉的头部毫秒, 裁掉的尾部毫秒, 成段毫秒)。

    拆成两步只为一件事：**量出头部到底裁掉多少**。合并成一个滤镜链时这个数只能靠
    「第一个词起于多少」去猜，而 TTS 的边界时间与实际过阈点差 100ms 是常态；
    拆开后 head_ms 就是两次时长相减，不掺任何假设，`atText` 的时间轴才有共同的零点。
    """
    run([ffmpeg, "-y", "-loglevel", "error", "-i", mp3, "-ar", str(SAMPLE_RATE),
         "-ac", "1", "-c:a", "pcm_s16le", work_wav])
    run([ffmpeg, "-y", "-loglevel", "error", "-i", work_wav, "-af", TRIM_HEAD,
         "-c:a", "pcm_s16le", head_wav])
    run([ffmpeg, "-y", "-loglevel", "error", "-i", head_wav, "-af", TRIM_TAIL,
         "-c:a", "pcm_s16le", wav])
    head_ms = duration_ms(ffprobe, work_wav) - duration_ms(ffprobe, head_wav)
    dur = duration_ms(ffprobe, wav)
    return head_ms, duration_ms(ffprobe, head_wav) - dur, dur


def duration_ms(ffprobe, path):
    out = subprocess.check_output(
        [ffprobe, "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", path],
        text=True,
    ).strip()
    return int(round(float(out) * 1000))


def assemble(timeline_path, clips, total_ms):
    """把各段 PCM 按 startMs 摆到一条静音轨上。"""
    frames = bytearray(total_ms * SAMPLE_RATE // 1000 * 2)
    for clip in clips:
        with wave.open(clip["wav"], "rb") as src:
            if src.getnchannels() != 1 or src.getsampwidth() != 2:
                raise SystemExit(f"unexpected wav format: {clip['wav']}")
            if src.getframerate() != SAMPLE_RATE:
                raise SystemExit(f"unexpected sample rate: {clip['wav']}")
            pcm = src.readframes(src.getnframes())
        offset = clip["startMs"] * SAMPLE_RATE // 1000 * 2
        end = offset + len(pcm)
        if end > len(frames):
            raise SystemExit(f"clip overruns timeline: {clip['id']}")
        frames[offset:end] = pcm
    with wave.open(timeline_path, "wb") as dst:
        dst.setnchannels(1)
        dst.setsampwidth(2)
        dst.setframerate(SAMPLE_RATE)
        dst.writeframes(bytes(frames))


def normalize_words(words, seg_id, dur_ms, head_ms, tail_ms):
    """把词边界挪到「裁掉首尾静音之后」的时间轴上，并与实测段长对表。

    零点必须是**实测的裁剪量**，不能拿第一个词的起点顶替：TTS 声称第一个词起于
    96ms，实际音频到 200ms 才越过 -45dB（实测过 s0-1：96 对 200）。差这 100ms
    会让所有 `atText` 绑定点整体偏晚，而偏晚的片子在 V11 里正好卡在「走神」的
    容差边上——看起来每根柱子都差一点，其实是尺子的零点错了。

    对表那一步是防「单位错」的：offset 的单位是 100ns，写错一位整条时间轴会差 10 倍，
    而错 10 倍的时间轴看起来仍然「单调递增、都在段内」，只有拿段长一对就露。
    容差里要**扣掉被剪掉的尾巴**：剪掉的确实都是 -45dB 以下的衰减，词边界却常把它
    算进时长（「谢谢」边界给 575ms、可闻只有 340ms），拿它当错误会天天误报。
    """
    if not words:
        raise SystemExit("段 %s 没有词边界" % seg_id)
    out = []
    for w in words:
        # 起点和终点都要平移再夹到 0：只平移起点会让「跨过裁剪点」的那个词
        # 保留原来的时长，看起来比整段还长（s4-1「谢谢」就这么差了 296ms）。
        start = max(0, w["tMs"] - head_ms)
        stop = max(start, w["tMs"] + w["dMs"] - head_ms)
        out.append({"tMs": start, "dMs": stop - start, "text": w["text"]})
    end = out[-1]["tMs"] + out[-1]["dMs"]
    slack = 120 + tail_ms
    if end - dur_ms > slack or end < dur_ms - slack:
        raise SystemExit(
            "段 %s：末词结束在 %dms，实测段长 %dms（尾剪 %dms）——差得超出解释，"
            "词边界与音频对不上（先怀疑 offset 单位）"
            % (seg_id, end, dur_ms, tail_ms))
    return out


def main():
    ap = argparse.ArgumentParser(description="Q7：讲解词 → 音频 + 页内时间轴")
    ap.add_argument("--script", default="scripts/fidelity/q7-script.json")
    ap.add_argument("--out", default="tmp/q7")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    ap.add_argument("--ffprobe", default="ffprobe")
    ap.add_argument("--voice", default="")
    ap.add_argument("--rate", default="")
    args = ap.parse_args()

    with open(args.script, encoding="utf-8") as fh:
        script = json.load(fh)

    voice = args.voice or script["voice"]
    rate = args.rate or script["rate"]
    lead = int(script.get("leadMs", 220))
    gap = int(script.get("gapMs", 150))
    tail = int(script.get("tailMs", 700))
    min_page = int(script.get("minPageMs", 2500))

    tts_dir = os.path.join(args.out, "tts")
    audio_dir = os.path.join(args.out, "audio")
    os.makedirs(tts_dir, exist_ok=True)
    os.makedirs(audio_dir, exist_ok=True)

    pages, clips = [], []
    words_doc = {}
    for page in script["pages"]:
        cursor = lead
        segs = []
        for seg in page["segments"]:
            mp3 = os.path.join(tts_dir, seg["id"] + ".mp3")
            wav = os.path.join(tts_dir, seg["id"] + ".wav")
            # TTS 只做一次：同一句重合成一次，时长不保证分毫不差，而时长正是这次实验
            # 要测的量。缓存键是「声音+语速+文本」，任一变了才重打。
            sidecar = os.path.join(tts_dir, seg["id"] + ".words.json")
            key = "%s|%s|%s" % (voice, rate, seg["text"])
            words = None
            if os.path.exists(mp3) and os.path.exists(sidecar):
                try:
                    with open(sidecar, encoding="utf-8") as fh:
                        rec = json.load(fh)
                    if rec.get("key") == key:
                        words = rec["words"]
                except (OSError, ValueError):
                    words = None
            if words is None:
                words = synth(voice, rate, seg["text"], mp3)
                with open(sidecar, "w", encoding="utf-8") as fh:
                    json.dump({"key": key, "words": words}, fh, ensure_ascii=False)
            head_ms, tail_ms, ms = trim_to_wav(
                args.ffmpeg, args.ffprobe, mp3,
                os.path.join(tts_dir, seg["id"] + ".raw.wav"),
                os.path.join(tts_dir, seg["id"] + ".head.wav"),
                wav)
            if ms <= 0:
                raise SystemExit(f"empty segment: {seg['id']}")
            words = normalize_words(words, seg["id"], ms, head_ms, tail_ms)
            words_doc[seg["id"]] = {"page": page["index"], "startMs": cursor,
                                    "durationMs": ms, "words": words}
            segs.append({
                "id": seg["id"],
                "text": seg["text"],
                "audio": os.path.relpath(wav, args.out).replace(os.sep, "/"),
                "startMs": cursor,
                "endMs": cursor + ms,
                "durationMs": ms,
                "bind": seg.get("bind", []),
            })
            clips.append({"id": seg["id"], "wav": wav, "startMs": cursor})
            cursor += ms + gap
            print(f"  {seg['id']:<6} {ms:>5}ms  {seg['text']}", flush=True)
        page_ms = max(min_page, cursor - gap + tail)
        pages.append({
            "index": page["index"],
            "slideId": page.get("slideId", f"slide-{page['index']}"),
            "durationMs": page_ms,
            "segments": segs,
        })
        print(f"page {page['index']}: {page_ms}ms / {len(segs)} 段", flush=True)

    total_ms = sum(p["durationMs"] for p in pages)
    timeline = os.path.join(audio_dir, "timeline.wav")
    assemble(timeline, clips, total_ms)

    doc = {
        "voice": voice,
        "rate": rate,
        "leadMs": lead,
        "gapMs": gap,
        "tailMs": tail,
        "totalMs": total_ms,
        "timeline": os.path.relpath(timeline, args.out).replace(os.sep, "/"),
        "pages": pages,
    }
    out_json = os.path.join(args.out, "segments.json")
    with open(out_json, "w", encoding="utf-8") as fh:
        json.dump(doc, fh, ensure_ascii=False, indent=1)

    # 逐词时间轴单独存：它不是 plan 的输入（plan 只认 offsetMs），而是**绑定解析**与
    # **门禁对账**的输入。混进 segments.json 会让「音频时间轴」与「词时间轴」共用一份
    # 文件，改一处必须重跑 TTS——那正是 sync_bind.py 想避开的事。
    words_json = os.path.join(args.out, "words.json")
    with open(words_json, "w", encoding="utf-8") as fh:
        json.dump({"voice": voice, "rate": rate, "segments": words_doc},
                  fh, ensure_ascii=False, indent=1)

    print(f"\ntotal {total_ms}ms ({total_ms/1000:.2f}s) → {out_json}")
    print(f"words → {words_json}（{len(words_doc)} 段 / "
          f"{sum(len(v['words']) for v in words_doc.values())} 词）")
    print(f"audio → {timeline} ({os.path.getsize(timeline)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
