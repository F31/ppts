#!/usr/bin/env bash
# 把一段成片压成一张"联系表"（storyboard），供人眼在 10 秒内过完整条片子的运动与文字。
#
# 为什么不是逐帧看视频：残影、元素被父层吃掉、字形缺失这几类问题，
# 在播放器里是"一闪而过"的；固定时间点抽帧拼图后，它们变成一张静态图上的
# 可比对证据，评审者不必反复拖动进度条，也便于把"我觉得不对"钉到具体秒。
#
# 用法：
#   ./storyboard.sh <input.mp4> [output.png] [间隔秒=2] [列数=5]
# 依赖：ffmpeg（WSL / Linux / 容器内都有；Windows 侧通常没有）
set -euo pipefail

in="${1:?用法: storyboard.sh <input.mp4> [output.png] [间隔秒=2] [列数=5]}"
out="${2:-${in%.*}_storyboard.png}"
every="${3:-2}"
cols="${4:-5}"

[ -f "$in" ] || { echo "找不到输入文件: $in" >&2; exit 1; }

# 抽帧间隔 → fps 表达式；总帧数决定行数（向上取整），tile 需要固定网格
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$in")
dur=${dur%.*}
frames=$(awk -v d="$dur" -v e="$every" 'BEGIN { n = int((d + e - 1) / e); if (n < 1) n = 1; print n }')
rows=$(awk -v n="$frames" -v c="$cols" 'BEGIN { print int((n + c - 1) / c) }')

# 单格宽 384px：5 列刚好 1920，既不缩放失真到看不清字，也不至于一张图几百 MB
ffmpeg -hide_banner -loglevel error -y -i "$in" \
  -vf "fps=1/${every},scale=384:-1:flags=lanczos,tile=${cols}x${rows}" \
  -frames:v 1 "$out"

echo "已生成 $out（${dur}s / 每 ${every}s 一帧 / ${cols}x${rows}）"
