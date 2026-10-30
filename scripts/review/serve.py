#!/usr/bin/env python3
"""5.5 观感盲评的本地评审服务。

为什么需要一个服务、而不是直接双击 mp4：

1. 浏览器打开本地文件时，源页 PNG 与成片不在同一个浏览上下文里，没法左边播放右边对照；
2. `SimpleHTTPRequestHandler` 不支持 Range，Chrome 里拖进度条会失效——而"回看 12.4s"是
   评分的核心动作（RUBRIC §一第 2 条：分差 ≥2 分时回看对应秒数）；
3. 评分表要能被复制成 review-packet.md §四 的格式，手抄六行数字容易漏项。

用法：

    python3 scripts/review/serve.py                # http://127.0.0.1:8088
    python3 scripts/review/serve.py --port 9000
    python3 scripts/review/serve.py --root tmp/5.5 --no-verify

`--no-verify` 跳过成片 sha256 校验。默认会校验：评审对象必须是评审包里记的那一份，
否则打出来的分对不上账（换片子比换评分表更容易发生，而且没有任何报错）。
"""

from __future__ import annotations

import argparse
import hashlib
import html
import mimetypes
import re
import sys
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

# 评审包里登记的成片（review-packet.md §二）。前缀足够——完整值在包里有。
EXPECTED = {
    "film_B_tier_30s.mp4": "cdddb11e",  # 评审对象
    "film_30s.mp4": "6f55b67e",         # 回归片 A
}

REVIEW_PAGE = Path(__file__).with_name("review.html")
RANGE_RE = re.compile(r"^bytes=(\d*)-(\d*)$")


def sha256_prefix(path: Path, n: int = 8) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()[:n]


def verify(root: Path) -> bool:
    ok = True
    for name, want in EXPECTED.items():
        path = root / name
        if not path.exists():
            print(f"  ✗ 缺文件 {name}", file=sys.stderr)
            ok = False
            continue
        got = sha256_prefix(path)
        mark = "✓" if got == want else "✗"
        if got != want:
            ok = False
        print(f"  {mark} {name}  sha256 {got}…（评审包登记 {want}…）")
    if not ok:
        print(
            "  成片与评审包登记的不一致。用 --no-verify 强行继续只能用于调试——\n"
            "  拿一份没登记过的片子打分，结论对不上 review-packet.md。",
            file=sys.stderr,
        )
    return ok


class Handler(BaseHTTPRequestHandler):
    server_version = "ppts-review/1.0"
    # HTTP/1.1：视频拖动会连开多条连接，1.0 每次都要重连、起播更慢。
    # 前提是所有响应都带准确的 Content-Length——这里每条路径都带了。
    protocol_version = "HTTP/1.1"
    root: Path = Path("tmp/5.5")

    def log_message(self, fmt: str, *args) -> None:  # 静音：评审时刷屏只会挡住真正的报错
        pass

    # ---- 路由 ----

    def do_HEAD(self) -> None:
        self._serve(head_only=True)

    def do_GET(self) -> None:
        self._serve(head_only=False)

    def _serve(self, head_only: bool) -> None:
        path = self.path.split("?", 1)[0]
        if path in ("/", "/index.html"):
            self._send_page(head_only)
            return
        target = (self.root / path.lstrip("/")).resolve()
        try:
            target.relative_to(self.root.resolve())
        except ValueError:
            self.send_error(HTTPStatus.FORBIDDEN, "path escapes review root")
            return
        if not target.is_file():
            self.send_error(HTTPStatus.NOT_FOUND, "not found")
            return
        self._send_file(target, head_only)

    # ---- 评审页 ----

    def _send_page(self, head_only: bool) -> None:
        if not REVIEW_PAGE.exists():
            self.send_error(HTTPStatus.INTERNAL_SERVER_ERROR, "review.html missing")
            return
        body = REVIEW_PAGE.read_bytes()
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")  # 页面是现场改的，缓存会看到旧表格
        self.end_headers()
        if not head_only:
            self.wfile.write(body)

    # ---- 静态文件（含 Range） ----

    def _send_file(self, path: Path, head_only: bool) -> None:
        size = path.stat().st_size
        ctype = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
        if ctype.startswith("text/") or ctype in ("application/json", "image/svg+xml"):
            ctype += "; charset=utf-8"

        start, end = 0, size - 1
        partial = False
        header = self.headers.get("Range")
        if header:
            m = RANGE_RE.match(header.strip())
            if m:
                raw_start, raw_end = m.group(1), m.group(2)
                if raw_start == "" and raw_end != "":
                    # 后缀范围：bytes=-1024 表示最后 1KB
                    start = max(0, size - int(raw_end))
                else:
                    start = int(raw_start or 0)
                    if raw_end:
                        end = min(int(raw_end), size - 1)
                if start > end or start >= size:
                    self.send_response(HTTPStatus.REQUESTED_RANGE_NOT_SATISFIABLE)
                    self.send_header("Content-Range", f"bytes */{size}")
                    self.end_headers()
                    return
                partial = True

        length = end - start + 1
        self.send_response(HTTPStatus.PARTIAL_CONTENT if partial else HTTPStatus.OK)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(length))
        self.send_header("Accept-Ranges", "bytes")
        if partial:
            self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self.end_headers()
        if head_only:
            return
        with path.open("rb") as fh:
            fh.seek(start)
            remaining = length
            while remaining > 0:
                chunk = fh.read(min(64 * 1024, remaining))
                if not chunk:
                    break
                try:
                    self.wfile.write(chunk)
                except (BrokenPipeError, ConnectionResetError):
                    return  # 拖进度条会主动断开上一条流，属正常
                remaining -= len(chunk)


def main() -> int:
    # 不重定向到文件时 stdout 是块缓冲的，启动信息会憋到进程结束才出现——
    # 评审人看到的是一行都没有的黑窗口。
    try:
        sys.stdout.reconfigure(line_buffering=True)
    except AttributeError:
        pass

    ap = argparse.ArgumentParser(description="5.5 观感盲评本地服务")
    ap.add_argument("--root", default="tmp/5.5", help="评审素材目录（默认 tmp/5.5）")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8088)
    ap.add_argument("--no-verify", action="store_true", help="跳过成片 sha256 校验")
    args = ap.parse_args()

    root = Path(args.root).resolve()
    if not root.is_dir():
        print(f"素材目录不存在：{root}\n先按 review-packet.md §七 复跑出片。", file=sys.stderr)
        return 2

    print("成片身份：")
    verified = verify(root)
    if not verified and not args.no_verify:
        print("\n校验未通过，不启动。确认片子无误后再用 --no-verify。", file=sys.stderr)
        return 3

    Handler.root = root
    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    url = f"http://{args.host}:{args.port}/"
    pages = len(list((root / "fid" / "pages").glob("*.png")))
    print(f"\n素材目录：{root}")
    print(f"源页：{pages} 张；联系表：{'有' if (root / 'film_B_storyboard.png').exists() else '无'}")
    print(f"评审页：{html.escape(url)}")
    print("Ctrl+C 结束。\n")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n已停止。")
    finally:
        httpd.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
