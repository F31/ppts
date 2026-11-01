#!/usr/bin/env python3
"""音画累计误差 SLO 实测（技术方案 V4.0 §14.4）。

指标：30 分钟成品**尾部**偏差 ≤ 100 ms，用解码后的真实时间戳，
不用文件标称时长。

为什么不能只看文件头：容器里写的是时长，播放器用的是解码后的时间戳。
AAC 有 priming（编码器在开头补的样本），H.264 有 B 帧重排，两者都会让
"标称时长"与"最后一帧真正出现的时间"错开；错开量随片子变长而累积。
只有把成片解码回来、找到最后一处标记的真实位置，才知道差多少。

做法（同一份成片同时测两条轨，所以"音画相对偏移"与"各自累计误差"都能读）：
  1. 合成一条 duration 秒的音轨：每 mark_every 秒一个 50ms 1kHz burst。
  2. 用 ffmpeg 编成一个 mp4：画面每 mark_every 秒闪一帧白，
     音轨就是上一步的 WAV；视频 30fps，音频 AAC 48kHz。
  3. 解码回来：
     - 音轨解码成 PCM，按能量找最后一个 burst 的起始采样；
     - 视频按场景切换找最后一个白帧的 pts。
  4. 两者都与计划时刻比，也互相比。

判据与局限都写进输出：这是**编码/封装链路**的累计误差，
不含渲染与合成环节，也不代表真实 TTS 音频（真实音频有气音与底噪，
不会像合成 burst 这样干净）。它回答的是"封装 30 分钟后还准不准"。

用法：
  python3 scripts/fidelity/slo_av_sync_30min.py                # 默认 1800 秒
  python3 scripts/fidelity/slo_av_sync_30min.py --duration 300 # 快速冒烟
"""

import argparse
import os
import re
import subprocess
import sys
import tempfile
import wave

import numpy as np

SLO_MS = 100.0


def ffmpeg_bin():
    p = os.environ.get("FFMPEG")
    if p:
        return p
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def run(args, **kw):
    res = subprocess.run(args, capture_output=True, text=True, errors="replace", **kw)
    if res.returncode != 0:
        raise RuntimeError("命令失败: %s\n%s" % (" ".join(args), res.stderr[-2000:]))
    return res


def build_wav(path, duration_s, mark_every_s, rate=48000):
    """写一条带周期 burst 的音轨。

    burst 用 5ms 淡入/淡出，避免方波边沿在 AAC 里产生宽带噪声——
    噪声会让"最后一个 burst 的起点"变得不可靠，测出来的是编码器的
    振铃而不是时间戳误差。
    """
    total = int(duration_s * rate)
    samples = np.zeros(total, dtype=np.int16)
    burst_len = int(0.05 * rate)
    fade = int(0.005 * rate)
    t = np.arange(burst_len) / rate
    tone = np.sin(2 * np.pi * 1000 * t) * 20000
    env = np.ones(burst_len)
    env[:fade] = np.linspace(0, 1, fade)
    env[-fade:] = np.linspace(1, 0, fade)
    burst = (tone * env).astype(np.int16)

    marks = []
    m = mark_every_s
    while m < duration_s - 1:
        i = int(m * rate)
        samples[i:i + burst_len] = burst
        marks.append(m)
        m += mark_every_s

    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(samples.tobytes())
    return marks


def encode(wav, out, duration_s, mark_every_s, fps=30, size="320x180"):
    """把静态画面 + 音轨封装成 mp4。白闪与 burst 同一时刻。"""
    # 每 mark_every 秒闪一帧：mod(t, T) 落在首帧附近。
    # gte(t, T) 把 t=0 也白闪的那一帧排除掉，避免把第 0 秒当成标记。
    enable = "gte(t,{T})*lt(mod(t,{T}),{half})".format(T=mark_every_s, half=0.5 / fps)
    vf = (
        "drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='%s'" % enable
    )
    run([
        ffmpeg_bin(), "-y", "-hide_banner", "-loglevel", "error",
        "-f", "lavfi", "-i", "color=c=black:s=%s:r=%d:d=%d" % (size, fps, duration_s),
        "-i", wav,
        "-vf", vf,
        "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
        "-c:a", "aac", "-b:a", "128k", "-ar", "48000",
        "-t", str(duration_s), "-shortest",
        out,
    ])
    return out


def decode_audio(mp4, out_wav, rate=48000):
    run([
        ffmpeg_bin(), "-y", "-hide_banner", "-loglevel", "error",
        "-i", mp4, "-vn", "-ac", "1", "-ar", str(rate),
        "-c:a", "pcm_s16le", out_wav,
    ])
    with wave.open(out_wav, "rb") as w:
        rate = w.getframerate()
        raw = w.readframes(w.getnframes())
    return np.frombuffer(raw, dtype=np.int16), rate


def last_burst_seconds(samples, rate, threshold=3000, win_s=0.005, min_len_s=0.02):
    """返回最后一个 burst 的起始时刻（秒）与解码后时长。

    必须先在窗口内取峰值包络，再判阈值：1 kHz 正弦每 0.5ms 就过一次零，
    逐采样比较会把一个 50ms 的 burst 切成几百个几十微秒的碎片——
    那样测出来的"最后一个 burst"落在噪声上，时长 0.3ms，偏差读数
    只是碰巧好看（本脚本第一版就踩了这个坑）。

    窗口 5ms 换来 5ms 的时间分辨率，相对 100ms 门槛足够。
    """
    env = np.abs(samples).astype(np.int32)
    win = max(1, int(win_s * rate))
    usable = len(env) // win * win
    if usable == 0:
        return None
    env = env[:usable].reshape(-1, win).max(axis=1)
    loud = env > threshold
    if not loud.any():
        return None
    min_len = max(1, int(min_len_s / win_s))
    idx = len(loud) - 1
    while idx >= 0:
        while idx >= 0 and not loud[idx]:
            idx -= 1
        if idx < 0:
            break
        end = idx
        while idx >= 0 and loud[idx]:
            idx -= 1
        start = idx + 1
        if end - start + 1 >= min_len:
            return start * win / float(rate), (end - start + 1) * win / float(rate)
    return None


def video_marks(mp4, seek_s, duration_s):
    """解码尾部片段，返回 (白闪 pts 列表, 该片段起始 pts)。

    用 -copyts 保留绝对时间戳：报告要的是"成片里第几秒"，
    不是"从 seek 点起第几秒"。scene 分数在纯黑→纯白时接近 1，
    阈值 0.2 足够；不设 0.9 是因为 h264 的块效应会让单帧分数偏低。
    """
    args = [
        ffmpeg_bin(), "-hide_banner", "-nostats",
        "-ss", "%.3f" % seek_s, "-copyts",
        "-i", mp4, "-an",
        "-vf", "select='gt(scene,0.2)',showinfo",
        "-f", "null", "-",
    ]
    res = subprocess.run(args, capture_output=True, text=True, errors="replace")
    if res.returncode != 0:
        raise RuntimeError("抽帧失败: %s" % res.stderr[-1500:])
    pts = [float(m) for m in re.findall(r"pts_time:([0-9.]+)", res.stderr)]
    return pts


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--duration", type=float, default=1800.0, help="成片时长（秒）")
    ap.add_argument("--mark-every", type=float, default=60.0, help="标记间隔（秒）")
    ap.add_argument("--fps", type=int, default=30)
    ap.add_argument("--keep", action="store_true", help="保留中间文件便于复核")
    a = ap.parse_args()

    duration, every = a.duration, a.mark_every
    work = tempfile.mkdtemp(prefix="ppts-slo-av-")
    wav = os.path.join(work, "timeline.wav")
    mp4 = os.path.join(work, "film.mp4")
    dec = os.path.join(work, "decoded.wav")

    print("音画累计误差 SLO：%.0fms（解码后真实时间戳）" % SLO_MS)
    print("成片 %.0f 秒，每 %.0f 秒一处标记，%d fps" % (duration, every, a.fps))

    marks = build_wav(wav, duration, every)
    plan_last = marks[-1]
    print("合成音轨：%d 处标记，最后一处计划在 %.3fs" % (len(marks), plan_last))

    encode(wav, mp4, duration, every, fps=a.fps)
    size_mb = os.path.getsize(mp4) / 1e6
    print("封装完成：%s（%.1f MB）" % (mp4, size_mb))

    samples, rate = decode_audio(mp4, dec)
    audio = last_burst_seconds(samples, rate)
    if audio is None:
        print("FAIL: 解码后找不到 burst")
        return 1
    audio_start, audio_len = audio

    pts = video_marks(mp4, plan_last - 5.0, duration)
    if not pts:
        print("FAIL: 解码后找不到白闪帧")
        return 1
    # 取最后一个明显晚于 seek 点的白闪（seek 可能把前一帧也带进来）。
    video_last = max(pts)

    audio_err_ms = (audio_start - plan_last) * 1000.0
    video_err_ms = (video_last - plan_last) * 1000.0
    av_gap_ms = (audio_start - video_last) * 1000.0

    print("")
    print("%-34s %10s" % ("测量项", "实测"))
    print("%-34s %10.1f ms" % ("音频尾部偏差（vs 计划）", audio_err_ms))
    print("%-34s %10.1f ms" % ("视频尾部偏差（vs 计划）", video_err_ms))
    print("%-34s %10.1f ms" % ("音画相对偏移（音频-视频）", av_gap_ms))
    print("%-34s %10.1f ms" % ("视频帧量化粒度（1/%dfps）" % a.fps, 1000.0 / a.fps))
    print("%-34s %10.1f ms" % ("尾部 burst 解码后时长", audio_len * 1000.0))

    # 判据：音频与视频相对计划的偏差都要在门槛内。
    # 只测音画相对偏移会漏掉"两轨一起漂"——那正是封装累计误差的典型形态。
    worst = max(abs(audio_err_ms), abs(video_err_ms))
    print("")
    if worst <= SLO_MS:
        print("PASS: 最大尾部偏差 %.1f ms ≤ %.0f ms" % (worst, SLO_MS))
        rc = 0
    else:
        print("FAIL: 最大尾部偏差 %.1f ms > %.0f ms" % (worst, SLO_MS))
        rc = 1
    if not a.keep:
        import shutil
        shutil.rmtree(work, ignore_errors=True)
    else:
        print("中间文件保留在 %s" % work)
    return rc


if __name__ == "__main__":
    sys.exit(main())
