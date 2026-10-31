#!/usr/bin/env python3
"""调 `dom-shot.mjs` 取帧：V13 与 V13b 共用同一份调用约定。

为什么单独一层：这条命令的**失败形态**比成功形态更值得统一。
`dom-shot.mjs` 把结果写成一行 `SHOT_JSON {...}`（包括失败），所以「进程退了 0
但没有 JSON」和「JSON 说 ok=false」是两种完全不同的故障，混在一起会把环境问题
（没有浏览器）报成判据问题（画面不一致）。两个门禁都只要一句话：拿到报告，或者
带着可读的原因抛 EnvironmentError。

依赖：node 22+、一个 Chromium（PPT_CHROME 或常见安装路径）。
"""

from __future__ import annotations

import json
import os
import subprocess

HERE = os.path.dirname(os.path.abspath(__file__))


def run_dom_shot(pkg, times, out_dir, stage=None):
    """取帧并返回报告。

    stage=(宽, 高) 时按原生尺寸画（去掉播放器 1280 的卡片上限）并校验实测舞台尺寸；
    None 时保持「双击打开看到的样子」。详见 dom-shot.mjs 顶部的说明。
    """
    node = os.environ.get("NODE", "node")
    script = os.path.join(HERE, "dom-shot.mjs")
    cmd = [
        node, script,
        "--pkg", os.path.abspath(pkg),
        "--out", os.path.abspath(out_dir),
        "--times", ",".join(str(t) for t in times),
    ]
    if stage:
        cmd += ["--stage", f"{stage[0]}x{stage[1]}"]

    proc = subprocess.run(cmd, capture_output=True, text=True)
    for line in proc.stdout.splitlines():
        if line.startswith("SHOT_JSON "):
            report = json.loads(line[len("SHOT_JSON "):])
            if not report.get("ok"):
                raise EnvironmentError(f"截图失败：{report.get('error')}")
            return report
    detail = (proc.stderr or proc.stdout or "").strip()[:400]
    raise EnvironmentError(f"dom-shot 没有输出结果（exit {proc.returncode}）：{detail or '无输出'}")
