"""Exercise the pinned real Claude CLI against a local fake provider, offline."""

import json
import os
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

requests = []


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        requests.append((self.path, self.headers, body))
        events = [
            ("message_start", {"type": "message_start", "message": {
                "id": "msg_fixture", "type": "message", "role": "assistant",
                "model": "claude-sonnet-4-20250514", "content": [],
                "stop_reason": None, "stop_sequence": None,
                "usage": {"input_tokens": 10, "output_tokens": 0,
                          "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}}}),
            ("content_block_start", {"type": "content_block_start", "index": 0,
                                     "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                     "delta": {"type": "text_delta", "text": "Fixture reply"}}),
            ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ("message_delta", {"type": "message_delta", "delta": {
                "stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 3}}),
            ("message_stop", {"type": "message_stop"}),
        ]
        data = "".join(f"event: {name}\ndata: {json.dumps(event)}\n\n" for name, event in events).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


server = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
try:
    with tempfile.TemporaryDirectory() as home:
        # No host login or provider secret is available to this test process.
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home,
               "TERM": "dumb", "LANG": "C.UTF-8",
               "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{server.server_port}",
               "CLAUDE_CODE_OAUTH_TOKEN": "botbox-placeholder",
               "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
               "DISABLE_TELEMETRY": "1"}
        result = subprocess.run(
            ["claude", "-p", "--verbose", "--input-format", "stream-json",
             "--output-format", "stream-json", "--dangerously-skip-permissions",
             "--model", "sonnet"],
            input=json.dumps({"type": "user", "message": {"role": "user", "content": "Fixture greeting"}}) + "\n",
            text=True, capture_output=True, env=env, timeout=30, check=False)
        assert result.returncode == 0, f"CLI contract failed: {result.stderr[:500]} {result.stdout[:500]}"
        assert "Fixture reply" in result.stdout, "CLI dropped the provider response"
        assert requests, "CLI did not call the fake provider"
        for path, headers, body in requests:
            assert urlsplit(path).path == "/v1/messages", f"unexpected provider route: {path}"
            assert headers.get("Authorization") == "Bearer botbox-placeholder", "OAuth mode was not selected"
            assert not headers.get("x-api-key"), "subscription attempted an API-key fallback"
            assert "oauth-2025-04-20" in headers.get("anthropic-beta", ""), "OAuth beta missing"
        assert any(body["model"].startswith("claude-sonnet") for _, _, body in requests), "sonnet alias was not resolved"
        print("official Claude CLI: subscription placeholder and sonnet alias verified offline")
finally:
    server.shutdown()
    server.server_close()
    thread.join()
