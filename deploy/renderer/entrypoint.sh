#!/bin/sh
# 容器入口：把构建期推导出的字体版本拼成 -fonts，再交给 Go 服务。
#
# 为什么不在 Dockerfile 的 CMD 里写死 `-fonts "Noto Sans CJK SC@20230817"`：
# 那个版本号会进 planHash（字体换了，同一份计划的像素就变了）。手写的版本号
# 一定会在某次改字集后忘记同步——改的人是改 charset.txt，不会记得回来改 CMD。
# 现在的链条是机械的：charset.txt 的 sha256 → subset-fonts.py 推导 → /etc/ppts/fonts.version
# → 这里读出来 → planHash。改字集而版本不变这件事，在结构上不可能发生。
#
# exec 保证信号直达 Go 进程（PID 1 是 sh 的话，SIGTERM 不转发会变成 10s 后强杀）。
set -e

if [ -s /etc/ppts/fonts.version ]; then
    set -- -fonts "Noto Sans CJK SC@$(cat /etc/ppts/fonts.version)" "$@"
fi

exec /usr/local/bin/ppts-renderer "$@"
