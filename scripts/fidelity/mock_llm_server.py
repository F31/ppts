#!/usr/bin/env python3
"""离线验证 bind_llm.py 的整条链路：假装是 OpenAI 兼容端点，返回合法 JSON。

不测模型能力——测的是「有 key 的那一天，脚本真的能跑完」：
chat() 的请求体、缓存落盘、validate、score、judge、报告写出。

用法：
    python3 scripts/fidelity/mock_llm_server.py --port 8799 &
    python3 scripts/fidelity/bind_llm.py --api-key fake \
        --base-url http://127.0.0.1:8799 --variants baseline --out-dir tmp/llm-mock
"""
import argparse
import json
import re
from http.server import BaseHTTPRequestHandler, HTTPServer

E_LINE = re.compile(r"^E (\S+) \| (\S+) \| (.*)$")
S_RE = re.compile(r"^S (\S+) \|", re.M)

AUX_KINDS = {"page_number", "eyebrow", "note", "footer"}
AUX_TEXT = re.compile(r"^(图|表|注)\s*[:：]|资料来源|来源[:：]|联系[:：]|©|内部资料")


def parse_elements(user):
    """逐行解出 (元素 id, kind, 文本, 是否已被规则绑走)。

    第一版用一条正则吃整行，`(.+?)` 后面紧跟可选组，引擎直接把 `| BOUND->s1-1`
    吞进文本里；更糟的是拿段 id 当元素 id 去比集合，于是「已被规则绑」全判成空位。
    这里按 ` | ` 从右往左切，标记与文本分开。
    """
    out = []
    for line in user.splitlines():
        m = E_LINE.match(line)
        if not m:
            continue
        eid, kind, rest = m.groups()
        bound = False
        if rest.endswith(" | FREE"):
            rest = rest[: -len(" | FREE")]
        elif " | BOUND->" in rest:
            rest, _, _seg = rest.rpartition(" | BOUND->")
            bound = True
        out.append((eid, kind, rest.strip(), bound))
    return out


def plan(user):
    """把「第一个像正文、且还没被规则绑走」的元素绑到「第一段」。

    过滤规则与产品侧的 is_aux 同口径——mock 不是被测对象，它只需要产出
    **合法的**绑定，好让 validate/score/judge 的分支都能被走到。
    """
    free_text = []
    for eid, kind, text, bound in parse_elements(user):
        if bound:
            continue
        if not text or text == "(no text)":
            continue
        if kind in AUX_KINDS or AUX_TEXT.search(text):
            continue
        free_text.append((eid, text))
    segs = S_RE.findall(user)

    if not free_text or not segs:
        return {"bindings": []}
    eid, _ = free_text[0]
    return {"bindings": [{"elementId": eid, "segmentId": segs[0], "confidence": 0.6}]}


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n).decode("utf-8"))
        user = ""
        for msg in body.get("messages") or []:
            if msg.get("role") == "user":
                user = msg.get("content") or ""
        content = json.dumps(plan(user), ensure_ascii=False)
        payload = {
            "model": "mock-binder",
            "choices": [{"message": {"role": "assistant", "content": content},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": len(user) // 2, "completion_tokens": len(content) // 2},
        }
        raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8799)
    a = ap.parse_args()
    HTTPServer(("127.0.0.1", a.port), Handler).serve_forever()
