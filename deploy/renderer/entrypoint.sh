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
    ver=$(cat /etc/ppts/fonts.version)
    set -- -fonts "Noto Sans CJK SC@$ver" "$@"
    # 字体子集（HTML 成品包内嵌用）：母版就是构建期那份 URO 子集产物，运行时再裁到
    # 「一份讲稿实际用到的字符」（约 100KB）。两个文件都在才打开——缺脚本时宁可让
    # 端点返回 404 走降级，也不要让服务起不来。
    if [ -f /usr/share/fonts/opentype/ppts/NotoSansCJKsc-Regular.otf ] \
        && [ -f /usr/local/bin/ppts-subset-runtime ]; then
        set -- -font-base /usr/share/fonts/opentype/ppts/NotoSansCJKsc-Regular.otf \
               -font-subset-script /usr/local/bin/ppts-subset-runtime \
               -font-version "$ver" "$@"
    fi
fi

exec /usr/local/bin/ppts-renderer "$@"
