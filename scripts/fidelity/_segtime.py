"""页起点的时间基准：从**本次产物目录**的段文件读实测时长。

为什么单独一个模块：三个脚本各自写死过 `tmp/q7/out/`。产物换个目录（G 片用
`out-g`），它们就拿上一版片子的段时长当这一版的页起点——整个页面的采样区间
被挪到隔壁页上，然后报「元素整页未出现」。那是一个长得非常像片子坏了的假失败，
会把人引去改本来没问题的编排。

两条硬约束，都是为了不再出现上面那种静默错位：

1. **段文件必须存在，缺了就退出。** 静默回退到计划值，会让「目录指错了」伪装成
   「片子有问题」。
2. **实测段长与计划页长差超过 200ms 就退出。** 差这么多只剩一种解释：这批段不是
   这份计划渲染出来的。

口径与 `check_alignment.py` 原先的一致：按帧输出，每页比计划短几毫秒，用计划值
当起点会把误差一路累加到最后一页。
"""
import os

# 本版产物目录。默认沿用 run_all.sh 的约定，换目录时用环境变量指过去。
SEG_DIR = os.environ.get("PPTS_SEG_DIR", "tmp/q7/out")

# 单页允许的「实测 vs 计划」之差。一帧 33ms，200ms 已经宽容到 6 帧。
TOL_MS = 200


def page_starts(script, probe_ms):
    """返回 (每页起点列表, 全片总时长)。`probe_ms` 由调用方注入（各自的 ffmpeg 封装）。"""
    starts, acc = [], 0
    for page in script["pages"]:
        path = os.path.join(SEG_DIR, "segment_%03d.mp4" % page["index"])
        if not os.path.exists(path):
            raise SystemExit(
                "缺段文件：%s\n"
                "  页起点要从**本次**产物的段时长累计；目录不对就不是同一版片子。\n"
                "  用 PPTS_SEG_DIR 指向它（当前 %r）。" % (path, SEG_DIR))
        ms = probe_ms(path)
        if abs(ms - page["durationMs"]) > TOL_MS:
            raise SystemExit(
                "段 %d 实测 %.0fms、计划 %dms（差 %.0fms > %dms）：\n"
                "  这批段不属于这份计划，页起点会整段错位。当前目录 %r。"
                % (page["index"], ms, page["durationMs"],
                   abs(ms - page["durationMs"]), TOL_MS, SEG_DIR))
        starts.append(acc)
        acc += ms
    return starts, acc
