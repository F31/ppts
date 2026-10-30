#!/usr/bin/env python3
"""成片的音轨与旁白原件是否对齐（音画同步）。

为什么需要：第 7 项的四个判据全都是「元素相对**语音**」的时间关系，而
`check_alignment.py` / `check_appearance.py` 的参照点是 `segments.json` 里的
`startMs`——那是**文本时间轴**，不是观众听到的时间。如果封装时音频整体偏了
（编码器补帧、AAC priming、容器起点不同），画面就会相对人声提前或滞后，
而两个视频检查一个都发现不了：它们比的都是同一个文本时间轴。

做法：把成片音轨解出来，与 `timeline.wav` 各算一条 100Hz 的能量包络，
做互相关求最佳 lag。峰形要尖——「±100ms 相关值掉到一半以下」说明峰是真的，
不是两条都平、随便对上一个位置。

用法：
  python3 scripts/fidelity/check_av_sync.py [film] [参考音轨]
判据：|lag| ≤ 33ms（1 帧）视为同步；超过 1 帧会让「不滞后 3 帧」这条判据失真。
"""
import os
import subprocess
import sys
import tempfile
import wave

import numpy as np


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def load(path):
    w = wave.open(path, "rb")
    a = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16).astype(np.float32)
    if w.getnchannels() > 1:
        a = a.reshape(-1, w.getnchannels()).mean(1)
    return a, w.getframerate()


def envelope(x, rate, hz=100):
    k = max(1, int(rate / hz))
    x = x[: len(x) // k * k].reshape(-1, k)
    return np.sqrt((x ** 2).mean(1))


def measure(film, ref):
    """成片音轨 vs 参考音轨的最佳 lag 与峰形。

    返回 dict(lag=秒, sharp=±100ms 处归一化相关值, durFilm, durRef)。
    `sharp` 是给调用方判「这个 lag 读数可不可信」用的：两条包络都平的时候，
    互相关在任何位置都差不多高，argmax 落哪纯属偶然，lag 好看看但没有意义。
    所以这个函数只报数，不判断——判断留给调用方（CLI 与 verify.py 各有一套阈值语境）。
    """
    with tempfile.TemporaryDirectory() as tmp:
        out = os.path.join(tmp, "film.wav")
        subprocess.run([ffmpeg_bin(), "-v", "error", "-y", "-i", film,
                        "-vn", "-ac", "1", "-ar", "24000", "-f", "wav", out], check=True)
        a, ra = load(out)
    b, rb = load(ref)

    ea, eb = envelope(a, ra), envelope(b, rb)
    m = min(len(ea), len(eb))
    ea, eb = ea[:m] - ea[:m].mean(), eb[:m] - eb[:m].mean()
    c = np.correlate(ea, eb, mode="full")
    peak = int(np.argmax(c))
    lag = (peak - (len(eb) - 1)) / 100.0
    norm = (c - c.min()) / (c.max() - c.min() + 1e-9)
    side = max(norm[max(0, peak - 10)], norm[min(len(norm) - 1, peak + 10)])
    return {"lag": round(lag, 3), "sharp": round(float(side), 2),
            "durFilm": round(len(a) / ra, 3), "durRef": round(len(b) / rb, 3)}


def main():
    film = sys.argv[1] if len(sys.argv) > 1 else "tmp/q7/film_C_narrated.mp4"
    ref = sys.argv[2] if len(sys.argv) > 2 else "tmp/q7/audio/timeline.wav"

    r = measure(film, ref)
    print("成片音轨 %.3fs / 参考 %.3fs" % (r["durFilm"], r["durRef"]))
    print("最佳 lag %+.3fs（正 = 成片音频滞后）· ±100ms 处相关值 %.2f" % (r["lag"], r["sharp"]))
    ok = abs(r["lag"]) <= 0.033
    print("判据 |lag| ≤ 33ms（1 帧）：%s" % ("通过" if ok else "不通过"))
    if r["sharp"] > 0.7:
        print("注意：峰不够尖（±100ms 处 %.2f），说明两条包络都很平，lag 不可信" % r["sharp"])
        return 2
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
