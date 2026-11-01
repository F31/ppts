"""本机假模型：只实现 POST /v1/chat/completions，回固定的绑定 JSON。

联调替身，不是产品代码。它唯一的职责是让产品侧的 HTTP 适配器（base url 拼接、
json_object、temperature/top_p、usage 解析）真的跑一遍——进程内的假 completer
跨不过序列化这一层，而这一层恰恰是「本机跑通、线上 400」的常见出处。

返回值对应 sample-customer-deck 第 7 页（USE CASES）：四张卡片，各一个标题 + 一段说明。
换了样本页就必须同步改 BINDINGS，否则校验会以 bad_element_id 全数拒绝——
脚本在第 5 步断言 route=model 会立刻停下，不会假通过。
"""

import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BINDINGS = [
    {"elementId": "shape-7", "segmentId": "seg-07-01", "confidence": 0.92},
    {"elementId": "shape-8", "segmentId": "seg-07-01", "confidence": 0.88},
    {"elementId": "shape-12", "segmentId": "seg-07-02", "confidence": 0.9},
    {"elementId": "shape-13", "segmentId": "seg-07-02", "confidence": 0.86},
    {"elementId": "shape-17", "segmentId": "seg-07-03", "confidence": 0.9},
    {"elementId": "shape-18", "segmentId": "seg-07-03", "confidence": 0.87},
    {"elementId": "shape-22", "segmentId": "seg-07-04", "confidence": 0.89},
    {"elementId": "shape-23", "segmentId": "seg-07-04", "confidence": 0.85},
]

LOG_PATH = ""


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n)
        try:
            req = json.loads(raw)
        except Exception:
            req = {}
        if LOG_PATH:
            with open(LOG_PATH, "a", encoding="utf-8") as f:
                f.write(json.dumps({"path": self.path, "body": req}, ensure_ascii=False) + "\n")

        chars = sum(len(str(m.get("content") or "")) for m in (req.get("messages") or []))
        prompt_tokens = max(1, chars // 4)
        body = {
            "id": "fake-" + str(int(time.time())),
            "model": req.get("model") or "fake-model",
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": json.dumps({"bindings": BINDINGS}, ensure_ascii=False),
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": 64,
                "total_tokens": prompt_tokens + 64,
            },
        }
        out = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, fmt, *args):
        sys.stderr.write("fake-llm: " + fmt % args + "\n")


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 38190
    LOG_PATH = sys.argv[2] if len(sys.argv) > 2 else ""
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
