#!/usr/bin/env python3
"""Q7 实验：把讲解词合成为音频与页内时间轴。

产出的 `segments.json` 同时是两件事的输入：
  1) `scripts/fidelity/plan -script` 用它把 clip 的 trigger 写成 `withSegment:<id>`；
  2) 音轨用它按 startMs 摆放，于是「段落的开口」在音轨与视频里是同一个时刻。

三个刻意为之的口径：

- **逐段合成，而不是整页合成后再切**。整页音频只能靠 VAD 或强制对齐切段，误差在
  几十到几百毫秒；逐段合成让段落边界就是拼接点，误差只剩编码器的一帧。
- **裁掉首尾静音**。edge-tts 的 mp3 前面自带约 100ms 静音（字幕第一句落在 0.100s）。
  不裁的话，元素按 startMs 出现、声音却晚 100ms 才起——3 帧，正好踩在「不滞后」的
  判据线上，测出来的是 TTS 的头部静音，不是渲染器的偏差。
- **段间留 150ms、页末留 700ms**。不留间隙的连续朗读听起来像机器报数；
  700ms 页末既是呼吸，也是「页切换前最后一帧」的观察窗。
"""

import argparse
import json
import os
import subprocess
import sys
import time
import wave

SAMPLE_RATE = 24000
TRIM_FILTER = (
    "silenceremove=start_periods=1:start_duration=0.02:start_threshold=-45dB,"
    "areverse,"
    "silenceremove=start_periods=1:start_duration=0.02:start_threshold=-45dB,"
    "areverse"
)


def run(cmd):
    subprocess.run(cmd, check=True)


def synth(edge, voice, rate, text, mp3, attempts=4):
    """合成一段。空音频是服务端偶发（同一句重试即成功），不是参数错，直接重试。

    edge-tts 把「服务端没回音频」报成 `NoAudioReceived: Please verify that your
    parameters are correct`——按字面去调参数会越调越远：同一句换个时间点再发就成功。
    所以这里是退避重试，不改文本、不改语速——改语速会改变段落时长，而时长正是
    这次实验要测的量。
    """
    last = None
    for i in range(attempts):
        try:
            run([edge, "--voice", voice, f"--rate={rate}", "--text", text,
                 "--write-media", mp3])
            if os.path.getsize(mp3) > 1024:
                return
            last = "empty audio"
        except subprocess.CalledProcessError as exc:
            last = exc
        time.sleep(0.4 * (2 ** i))
    raise SystemExit(f"tts failed for {text!r}: {last}")


def trim_to_wav(ffmpeg, mp3, wav):
    run([ffmpeg, "-y", "-loglevel", "error", "-i", mp3, "-af", TRIM_FILTER,
         "-ar", str(SAMPLE_RATE), "-ac", "1", "-c:a", "pcm_s16le", wav])


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


def main():
    ap = argparse.ArgumentParser(description="Q7：讲解词 → 音频 + 页内时间轴")
    ap.add_argument("--script", default="scripts/fidelity/q7-script.json")
    ap.add_argument("--out", default="tmp/q7")
    ap.add_argument("--edge", default=os.path.expanduser("~/ttsenv/bin/edge-tts"))
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
    for page in script["pages"]:
        cursor = lead
        segs = []
        for seg in page["segments"]:
            mp3 = os.path.join(tts_dir, seg["id"] + ".mp3")
            wav = os.path.join(tts_dir, seg["id"] + ".wav")
            synth(args.edge, voice, rate, seg["text"], mp3)
            trim_to_wav(args.ffmpeg, mp3, wav)
            ms = duration_ms(args.ffprobe, wav)
            if ms <= 0:
                raise SystemExit(f"empty segment: {seg['id']}")
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

    print(f"\ntotal {total_ms}ms ({total_ms/1000:.2f}s) → {out_json}")
    print(f"audio → {timeline} ({os.path.getsize(timeline)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
