# SPDX-License-Identifier: AGPL-3.0-only

import json
import os
from pathlib import Path
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from urllib.parse import urlsplit

MODEL = "openrouter/auto"
RESUME_MODEL = "anthropic/claude-sonnet-4.5"
CHAT_ROUTE = "/llm/openrouter/v1/chat/completions"
MESSAGES_ROUTE = "/llm/openrouter/v1/v1/messages"
TOOL = "mcp__botbox_desktop__fixture_desktop"
REQUESTS = []
TOOL_CALLS = []


class Fixture(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_error(405)

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        path = urlsplit(self.path).path
        if path == "/mcp":
            self.mcp(request)
        elif path == CHAT_ROUTE:
            self.chat_completions(request)
        elif path == MESSAGES_ROUTE:
            self.messages(request)
        else:
            self.send_error(404)

    def mcp(self, request):
        method = request["method"]
        if method.startswith("notifications/"):
            self.send_response(202)
            self.end_headers()
            return
        if method == "initialize":
            result = {
                "protocolVersion": "2025-03-26",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "desktop-fixture", "version": "1"},
            }
        elif method == "tools/list":
            result = {"tools": [{
                "name": "fixture_desktop",
                "description": "Returns the test desktop status.",
                "inputSchema": {"type": "object", "properties": {}},
            }]}
        elif method == "tools/call":
            TOOL_CALLS.append(request["params"]["name"])
            result = {"content": [
                {"type": "text", "text": "desktop fixture"},
                {"type": "image", "mimeType": "image/png", "data":
                    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/mXkAAAAASUVORK5CYII="},
            ]}
        else:
            result = {}
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({
            "jsonrpc": "2.0", "id": request["id"], "result": result,
        }).encode())

    def chat_completions(self, request):
        REQUESTS.append((self.headers.get("Authorization"), request, CHAT_ROUTE))
        step = len(REQUESTS)
        if step == 1:
            delta = {"role": "assistant", "tool_calls": [{
                "index": 0, "id": "call_desktop", "type": "function",
                "function": {"name": TOOL, "arguments": "{}"},
            }]}
            reason = "tool_calls"
        else:
            delta = {"role": "assistant", "content": f"fixture reply {step}"}
            reason = "stop"
        common = {
            "id": f"chatcmpl-{step}", "object": "chat.completion.chunk",
            "created": 1, "model": request["model"],
        }
        chunks = [
            {**common, "choices": [{"index": 0, "delta": delta, "finish_reason": None}]},
            {**common, "choices": [{"index": 0, "delta": {}, "finish_reason": reason}]},
            {**common, "choices": [], "usage": {
                "prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13,
            }},
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for chunk in chunks:
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
        self.wfile.write(b"data: [DONE]\n\n")

    def messages(self, request):
        REQUESTS.append((self.headers.get("x-api-key"), request, MESSAGES_ROUTE))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        events = [
            {"type": "message_start", "message": {
                "id": "msg-fixture", "type": "message", "role": "assistant",
                "model": request["model"], "content": [], "stop_reason": None,
                "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 0},
            }},
            {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
            {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "resumed"}},
            {"type": "content_block_stop", "index": 0},
            {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None},
             "usage": {"output_tokens": 3}},
            {"type": "message_stop"},
        ]
        for event in events:
            self.wfile.write(f"event: {event['type']}\ndata: {json.dumps(event)}\n\n".encode())


def run_pi(directory, prompt, model=MODEL, session=None):
    env = dict(os.environ, PI_CODING_AGENT_DIR=str(directory), PI_TELEMETRY="0")
    args = [
        "pi", "--mode", "json", "--offline", "--no-approve", "--no-extensions",
        "--extension", "builtin:mcp", "--no-skills", "--no-prompt-templates",
        "--provider", "openrouter", "--api-key", "botbox-placeholder",
        "--session-dir", str(directory / "sessions"), "--model", f"openrouter/{model}",
    ]
    if session:
        args += ["--session-id", session]
    result = subprocess.run(
        args, cwd=directory, env=env, input=prompt, capture_output=True,
        text=True, check=False, timeout=30,
    )
    assert result.returncode == 0, (result.stderr, result.stdout)
    events = [json.loads(line) for line in result.stdout.splitlines()]
    assert events[0]["type"] == "session", (events, result.stderr)
    assert events[-1]["type"] == "agent_settled", (events, result.stderr)
    assert not events[-1]["aborted"], events
    return events


with ThreadingHTTPServer(("127.0.0.1", 0), Fixture) as server, tempfile.TemporaryDirectory() as tmp:
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        directory = Path(tmp)
        endpoint = f"http://127.0.0.1:{server.server_port}"
        (directory / "models.json").write_text(json.dumps({"providers": {
            "openrouter": {"baseUrl": endpoint + "/llm/openrouter/v1"},
        }}))
        (directory / "mcp.json").write_text(json.dumps({"mcpServers": {
            "botbox-desktop": {"url": endpoint + "/mcp", "exposure": "direct"},
        }}))
        first = run_pi(directory, "first fixture prompt")
        assert TOOL_CALLS == ["fixture_desktop"], (first, REQUESTS)
        assert any(e.get("toolName") == TOOL for e in first), first
        session = first[0]["id"]
        second = run_pi(directory, "second fixture prompt", RESUME_MODEL, session)
        assert second[0]["id"] == session, second
        assert len(REQUESTS) == 3, (len(REQUESTS), second[-3:])
        for (auth, request, path), model in zip(REQUESTS, (MODEL, MODEL, RESUME_MODEL)):
            assert request["model"] == model, (request["model"], model)
            assert request["stream"], path
            if path == CHAT_ROUTE:
                assert auth == "Bearer botbox-placeholder", auth
                assert request["stream_options"]["include_usage"], path
            else:
                assert auth == "botbox-placeholder", auth
        assert TOOL in json.dumps(REQUESTS[0][1]["tools"]), TOOL
        tool_result = json.dumps(REQUESTS[1][1]["messages"])
        assert "desktop fixture" in tool_result and "image/png" in tool_result, tool_result
        resumed = json.dumps(REQUESTS[2][1]["messages"])
        assert "first fixture prompt" in resumed and "second fixture prompt" in resumed, resumed
        assert any(e.get("assistantMessageEvent", {}).get("type") == "text_delta" for e in second), second
    finally:
        server.shutdown()
        thread.join()
