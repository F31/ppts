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
import json
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


class Handler(BaseHTTPRequestHandler):
    server_version = "ppts-review/1.0"
    # HTTP/1.1：视频拖动会连开多条连接，1.0 每次都要重连、起播更慢。
    # 前提是所有响应都带准确的 Content-Length——这里每条路径都带了。
    protocol_version = "HTTP/1.1"
    root: Path = Path("tmp/5.5")
    page: Path = REVIEW_PAGE
    film: str = "film_B_tier_30s.mp4"

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
        if path == "/meta.json":
            self._send_meta(head_only)
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

    def _send_meta(self, head_only: bool) -> None:
        """页面靠它渲染身份与段落表：评分要对得上片子，就不能把 sha256 抄在 HTML 里。

        HTML 里写死的哈希只会在换片子后继续显示旧值——那是比没有更糟的「看起来对」。
        """
        out = {"film": None, "segments": None, "elements": None}
        film = self.root / self.film
        if film.is_file():
            out["film"] = {
                "name": self.film,
                "sha256": sha256_prefix(film, 64),
                "bytes": film.stat().st_size,
            }
        seg = self.root / "segments.json"
        if seg.is_file():
            try:
                out["segments"] = json.loads(seg.read_text(encoding="utf-8"))
            except json.JSONDecodeError as exc:
                out["segmentsError"] = str(exc)
        # 元素清单是「哪些元素被讲解提到」的补集算料：段落里只记了绑定的，
        # 没绑定的那些要靠它推出来，否则页面没法指出「这个元素讲解没提到」。
        elems = self.root / "fid" / "elements.json"
        if elems.is_file():
            try:
                out["elements"] = json.loads(elems.read_text(encoding="utf-8"))
            except json.JSONDecodeError:
                pass
        # 机器门禁的结果（scripts/fidelity/verify.py 产出）。页面**默认折叠**它：
        # 第 7 项是盲评，先看到「机器全过」会把人往 5 分上推，那就白评了。
        # 同时给出它相对成片的年龄——报告比片子旧的时候，数字再好看也是旧的。
        ver = self.root / "verify.json"
        if ver.is_file():
            try:
                out["verify"] = json.loads(ver.read_text(encoding="utf-8"))
                if film.is_file():
                    out["verify"]["ageSec"] = round(
                        film.stat().st_mtime - ver.stat().st_mtime, 0)
            except json.JSONDecodeError:
                out["verifyError"] = "verify.json 不是合法 JSON"
        body = json.dumps(out, ensure_ascii=False).encode("utf-8")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if not head_only:
            self.wfile.write(body)

    # ---- 评审页 ----

    def _send_page(self, head_only: bool) -> None:
        if not self.page.exists():
            self.send_error(HTTPStatus.INTERNAL_SERVER_ERROR, "review page missing")
            return
        body = self.page.read_bytes()
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

    ap = argparse.ArgumentParser(description="观感盲评本地服务")
    ap.add_argument("--root", default="tmp/5.5", help="评审素材目录（默认 tmp/5.5）")
    ap.add_argument("--page", default=str(REVIEW_PAGE), help="评审页 HTML（默认 review.html）")
    ap.add_argument("--film", default="film_B_tier_30s.mp4", help="评审对象文件名")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8088)
    ap.add_argument("--no-verify", action="store_true", help="跳过成片 sha256 校验")
    args = ap.parse_args()

    root = Path(args.root).resolve()
    if not root.is_dir():
        print(f"素材目录不存在：{root}\n先按 review-packet.md §七 复跑出片。", file=sys.stderr)
        return 2

    print("成片身份：")
    film = root / args.film
    if not film.is_file():
        print(f"  ✗ 缺成片 {args.film}", file=sys.stderr)
        if not args.no_verify:
            return 3
        verified = True
    else:
        got = sha256_prefix(film)
        want = EXPECTED.get(args.film)
        if want is None:
            # 没登记过的片子照样能起服务，但必须把实际值打出来——
            # 这一行就是评审记录里要写的那一行，抄错等于分对不上账。
            print(f"  ? {args.film}  sha256 {got}…（未在评审包登记，请把此值写进记录）")
            verified = True
        else:
            mark = "✓" if got == want else "✗"
            print(f"  {mark} {args.film}  sha256 {got}…（评审包登记 {want}…）")
            verified = got == want
    if not verified and not args.no_verify:
        print("\n校验未通过，不启动。确认片子无误后再用 --no-verify。", file=sys.stderr)
        return 3

    Handler.root = root
    Handler.page = Path(args.page).resolve()
    Handler.film = args.film
    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    url = f"http://{args.host}:{args.port}/"
    pages = len(list((root / "fid" / "pages").glob("*.png")))
    board = root / (Path(args.film).stem + "_storyboard.png")
    print(f"\n素材目录：{root}")
    print(f"源页：{pages} 张；联系表：{'有' if board.exists() else '无'}")
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
