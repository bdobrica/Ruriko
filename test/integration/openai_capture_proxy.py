#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import threading
import urllib.error
import urllib.request
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class ProxyState:
    def __init__(self, mode: str, upstream_base: str, capture_file: str, upstream_timeout: int):
        self.mode = mode
        self.upstream_base = upstream_base.rstrip("/")
        self.capture_file = capture_file
        self.upstream_timeout = upstream_timeout
        self.calls = 0
        self.lock = threading.Lock()

    def next_call_number(self) -> int:
        with self.lock:
            self.calls += 1
            return self.calls

    def record_exchange(
        self,
        *,
        call_no: int,
        path: str,
        request_body: bytes,
        request_headers: dict[str, str],
        response_status: int,
        response_body: bytes,
        response_content_type: str,
        mode: str,
        upstream_url: str,
    ) -> None:
        payload = {
            "call": call_no,
            "ts": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "mode": mode,
            "path": path,
            "upstream_url": upstream_url,
            "request": {
                "auth_present": bool(request_headers.get("authorization", "").strip()),
                "content_type": request_headers.get("content-type", ""),
                "body": request_body.decode("utf-8", errors="replace"),
            },
            "response": {
                "status": int(response_status),
                "content_type": response_content_type,
                "body": response_body.decode("utf-8", errors="replace"),
            },
        }
        with open(self.capture_file, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(payload, ensure_ascii=True) + "\n")


def _stub_response(call_no: int) -> dict:
    # Call 1 feeds the deterministic plan step output schema.
    if call_no == 1:
        content = json.dumps(
            {
                "items": [
                    {
                        "query": "OpenAI latest news products executives today",
                    }
                ]
            },
            ensure_ascii=True,
        )
    else:
        content = json.dumps(
            {
                "run_id": 1,
                "summary": "OpenAI coverage was collected and summarized.",
                "headlines": ["OpenAI coverage snapshot"],
                "material": True,
            },
            ensure_ascii=True,
        )

    return {
        "id": f"chatcmpl-kumo-live-{call_no}",
        "object": "chat.completion",
        "created": int(datetime.datetime.now(datetime.timezone.utc).timestamp()),
        "model": "gpt-4o",
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": content,
                },
                "finish_reason": "stop",
            }
        ],
        "usage": {
            "prompt_tokens": 50,
            "completion_tokens": 30,
            "total_tokens": 80,
        },
    }


def build_handler(state: ProxyState):
    class Handler(BaseHTTPRequestHandler):
        def _send_json(self, code: int, payload: dict):
            data = json.dumps(payload, ensure_ascii=True).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):  # noqa: N802
            if self.path == "/health":
                self._send_json(HTTPStatus.OK, {"status": "ok", "mode": state.mode})
                return
            if self.path == "/calls":
                with state.lock:
                    calls = state.calls
                self._send_json(HTTPStatus.OK, {"calls": calls, "mode": state.mode})
                return
            self._send_json(HTTPStatus.NOT_FOUND, {"error": "not found"})

        def do_POST(self):  # noqa: N802
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length) if length > 0 else b""
            headers = {k.lower(): v for k, v in self.headers.items()}
            call_no = state.next_call_number()

            response_status = HTTPStatus.OK
            response_content_type = "application/json"
            response_payload = b""
            upstream_url = state.upstream_base + self.path
            mode_used = state.mode

            if state.mode == "stub":
                response_payload = json.dumps(_stub_response(call_no), ensure_ascii=True).encode("utf-8")
            else:
                req = urllib.request.Request(
                    upstream_url,
                    data=body,
                    method="POST",
                    headers={
                        "Authorization": self.headers.get("Authorization", ""),
                        "Content-Type": self.headers.get("Content-Type", "application/json"),
                        "Accept": "application/json",
                    },
                )
                try:
                    with urllib.request.urlopen(req, timeout=state.upstream_timeout) as resp:
                        response_payload = resp.read()
                        response_status = resp.getcode()
                        response_content_type = resp.headers.get("Content-Type", "application/json")
                except urllib.error.HTTPError as err:
                    response_payload = err.read() if err.fp is not None else b""
                    response_status = err.code
                    response_content_type = err.headers.get("Content-Type", "application/json") if err.headers else "application/json"
                except Exception as err:  # noqa: BLE001
                    response_status = HTTPStatus.BAD_GATEWAY
                    response_content_type = "application/json"
                    response_payload = json.dumps(
                        {"error": f"upstream request failed: {err}"},
                        ensure_ascii=True,
                    ).encode("utf-8")

            state.record_exchange(
                call_no=call_no,
                path=self.path,
                request_body=body,
                request_headers=headers,
                response_status=int(response_status),
                response_body=response_payload,
                response_content_type=response_content_type,
                mode=mode_used,
                upstream_url=upstream_url,
            )

            self.send_response(int(response_status))
            self.send_header("Content-Type", response_content_type)
            self.send_header("Content-Length", str(len(response_payload)))
            self.end_headers()
            self.wfile.write(response_payload)

        def log_message(self, fmt: str, *args):  # noqa: A003
            return

    return Handler


def main() -> int:
    parser = argparse.ArgumentParser(description="Capture-and-forward proxy for OpenAI chat completions")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18081)
    parser.add_argument("--mode", choices=["passthrough", "stub"], default="passthrough")
    parser.add_argument("--upstream-base", default="https://api.openai.com")
    parser.add_argument("--upstream-timeout", type=int, default=20)
    parser.add_argument("--capture-file", required=True)
    args = parser.parse_args()

    state = ProxyState(args.mode, args.upstream_base, args.capture_file, args.upstream_timeout)
    server = ThreadingHTTPServer((args.host, args.port), build_handler(state))
    print(
        json.dumps(
            {
                "status": "listening",
                "host": args.host,
                "port": args.port,
                "mode": args.mode,
                "capture_file": args.capture_file,
            },
            ensure_ascii=True,
        ),
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
