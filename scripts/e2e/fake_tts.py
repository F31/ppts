"""本机假 TTS：只实现 POST /v1/audio/speech，回一段固定 1.2s 的 PCM16 WAV。

联调替身，不是产品代码。它存在的意义是让音色试听真的穿过一层 HTTP：
网关配置里的 baseUrl → OpenAI 兼容适配器 → 请求体序列化（model/input/voice/speed）
→ 响应 WAV 解码（时长必须来自 RIFF 头，而不是按字数估算）。

请求会逐条写入日志，供脚本断言"用户选的音色/语速真的传到了上游"——
这一步用进程内 fake provider 是跨不过去的，而"线上 401/音色不对/语速没生效"
恰恰都发生在这层序列化上。
"""

import json
import struct
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_PATH = ""
SAMPLE_RATE = 16000
DURATION_MS = 1200


def silent_wav(duration_ms: int, rate: int) -> bytes:
    samples = int(rate * duration_ms / 1000)
    data = b"\x00\x00" * samples
    header = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVE"
    fmt = b"fmt " + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16)
    return header + fmt + b"data" + struct.pack("<I", len(data)) + data


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n)
        try:
            body = json.loads(raw.decode("utf-8"))
        except Exception:
            body = {"_raw": raw.decode("utf-8", "replace")}
        record = {
            "path": self.path,
            "auth": self.headers.get("Authorization"),
            "body": body,
        }
        with open(LOG_PATH, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, ensure_ascii=False) + "\n")

        if self.path != "/v1/audio/speech":
            self._respond(404, b'{"message":"not found"}', "application/json")
            return
        self._respond(200, silent_wav(DURATION_MS, SAMPLE_RATE), "audio/wav")

    def _respond(self, status, payload, content_type):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main():
    global LOG_PATH
    if len(sys.argv) < 3:
        print("usage: fake_tts.py <port> <log-path>", file=sys.stderr)
        raise SystemExit(2)
    port = int(sys.argv[1])
    LOG_PATH = sys.argv[2]
    open(LOG_PATH, "w", encoding="utf-8").close()
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"fake tts on 127.0.0.1:{port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
