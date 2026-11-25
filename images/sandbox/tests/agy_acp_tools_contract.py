# SPDX-License-Identifier: AGPL-3.0-only

"""Verify pinned agy exposes and uses a client-configured MCP tool via ACP."""

import json
import os
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread


MCP_SERVER = """
import json
import sys

for line in sys.stdin:
    req = json.loads(line)
    if "id" not in req:
        continue
    method = req["method"]
    if method == "initialize":
        result = {"protocolVersion": "2025-03-26", "capabilities": {"tools": {}},
                  "serverInfo": {"name": "botbox-computer", "version": "1"}}
    elif method == "tools/list":
        result = {"tools": [{"name": "screenshot", "description": "Capture the desktop",
                             "inputSchema": {"type": "object", "properties": {}}}]}
    elif method == "tools/call":
        assert req["params"]["name"] == "screenshot"
        assert not req["params"].get("arguments"), req["params"]
        result = {"content": [
            {"type": "image", "mimeType": "image/png",
             "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8u0AAAAASUVORK5CYII="},
            {"type": "text", "text": "fixture desktop result"},
        ]}
    else:
        result = {}
    print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": result}), flush=True)
"""

calls = []


def candidate(part):
    return {
        "candidates": [{
            "content": {"role": "model", "parts": [part]},
            "index": 0, "finishReason": "STOP",
        }],
        "usageMetadata": {"promptTokenCount": 2, "candidatesTokenCount": 1, "totalTokenCount": 3},
    }


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        prompt = json.dumps(data.get("contents", []))
        declarations = json.dumps(data.get("tools", []))
        if "fixture desktop result" in prompt:
            if "inlineData" in prompt and "image/png" in prompt:
                calls.append("image result")
            response = candidate({"text": "Saw the fixture desktop"})
        elif "fixture computer prompt" in prompt and "mcp_botbox-computer_screenshot" in declarations and not calls:
            calls.append("call")
            response = candidate({"functionCall": {
                "id": "desktop-call", "name": "mcp_botbox-computer_screenshot",
                "args": {"toolSummary": "Capture the fixture desktop"},
            }})
        else:
            response = candidate({"text": "Fixture title"})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        self.wfile.write(f"data: {json.dumps(response)}\r\n\r\n".encode())


def request(process, request_id, method, params):
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}) + "\n")
    process.stdin.flush()
    notices = []
    for line in process.stdout:
        event = json.loads(line)
        if event.get("id") == request_id and "method" not in event:
            assert "error" not in event, event
            return event["result"], notices
        if event.get("method") == "session/request_permission":
            permission = event["params"]
            assert permission["sessionId"] == params["sessionId"], permission
            assert permission["toolCall"]["name"] == "mcp_botbox-computer_screenshot", permission
            option = next(o["optionId"] for o in permission["options"] if o["kind"] == "allow_once")
            reply = {"jsonrpc": "2.0", "id": event["id"],
                     "result": {"outcome": {"outcome": "selected", "optionId": option}}}
            process.stdin.write(json.dumps(reply) + "\n")
            process.stdin.flush()
        notices.append(event)
    raise AssertionError("ACP exited before replying")


with tempfile.TemporaryDirectory() as directory, ThreadingHTTPServer(("127.0.0.1", 0), Fixture) as server:
    Thread(target=server.serve_forever, daemon=True).start()
    home = Path(directory)
    config = home / ".gemini" / "antigravity-cli"
    config.mkdir(parents=True)
    # Only the test uses a synthetic Gemini key to point at the loopback fixture.
    settings = {
        "modelProvider": "gemini",
        "toolPermission": "request-review",
        "permissions": {
            "allow": ["mcp(botbox-computer/*)"],
            "deny": ["command(*)", "unsandboxed(*)", "read_file(*)", "write_file(*)", "read_url(*)", "execute_url(*)"],
        },
    }
    (config / "settings.json").write_text(json.dumps(settings))
    helper = home / "mcp_fixture.py"
    helper.write_text(MCP_SERVER)
    env = {
        "HOME": str(home), "PATH": os.environ["PATH"],
        "GEMINI_API_KEY": "fixture-only", "GOOGLE_GEMINI_BASE_URL": f"http://127.0.0.1:{server.server_port}",
    }
    args = ["agy", "acp", "--sandbox", "--disable-telemetry", "--include-default-customizations=false"]
    process = subprocess.Popen(args, cwd=home, env=env, text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        request(process, 1, "initialize", {"protocolVersion": 1, "clientCapabilities": {}})
        session, _ = request(process, 2, "session/new", {
            "cwd": str(home),
            "mcpServers": [{"name": "botbox-computer", "command": "/usr/bin/python3", "args": ["-I", str(helper)], "env": []}],
        })
        result, notices = request(process, 3, "session/prompt", {
            "sessionId": session["sessionId"],
            "prompt": [{"type": "text", "text": "fixture computer prompt. Use the screenshot tool."}],
        })
        assert result["stopReason"] == "end_turn", result
        assert calls == ["call", "image result"], calls
        chunks = [
            n["params"]["update"].get("content", {}).get("text", "")
            for n in notices if n.get("params", {}).get("update", {}).get("sessionUpdate") == "agent_message_chunk"
        ]
        assert "Saw the fixture desktop" in "".join(chunks), chunks
    finally:
        process.terminate()
        process.wait(timeout=5)
        server.shutdown()
