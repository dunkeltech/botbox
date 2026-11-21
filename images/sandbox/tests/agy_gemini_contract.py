# SPDX-License-Identifier: AGPL-3.0-only

"""Runs the pinned agy binary the way the agent does, against a loopback
Gemini fixture (ADR-0040). No real model API is called."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from urllib.parse import urlsplit

MODEL = "gemini-3.8-flash"
ROUTE = f"/llm/google/v1beta/models/{MODEL}:streamGenerateContent"
SETTINGS = '{"modelProvider":"gemini"}'
REQUESTS = []
TOOL_CALLS = []


def chunk(parts, final):
    body = {"candidates": [{"content": {"role": "model", "parts": parts}, "index": 0}]}
    if final:
        body["candidates"][0]["finishReason"] = "STOP"
        body["usageMetadata"] = {
            "promptTokenCount": 120, "candidatesTokenCount": 4,
            "thoughtsTokenCount": 6, "cachedContentTokenCount": 100, "totalTokenCount": 130,
        }
    return body


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.send_error(405)

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        url = urlsplit(self.path)
        if url.path == "/mcp":
            self.mcp(request)
        elif url.path == ROUTE and url.query == "alt=sse":
            self.generate(request)
        else:
            self.send_error(404)

    def mcp(self, request):
        method = request.get("method", "")
        if "id" not in request:
            self.send_response(202)
            self.end_headers()
            return
        reply = {"jsonrpc": "2.0", "id": request["id"]}
        if method == "initialize":
            reply["result"] = {
                "protocolVersion": "2025-03-26",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "desktop-fixture", "version": "1"},
            }
        elif method == "tools/list":
            reply["result"] = {"tools": [{
                "name": "fixture_desktop",
                "description": "Returns the test desktop status.",
                "inputSchema": {"type": "object", "properties": {}},
            }]}
        elif method == "tools/call":
            TOOL_CALLS.append(request["params"]["name"])
            reply["result"] = {"content": [{"type": "text", "text": "desktop fixture"}]}
        else:
            reply["error"] = {"code": -32601, "message": "method not found"}
        body = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def generate(self, request):
        REQUESTS.append((self.headers.get("X-Goog-Api-Key"), self.headers.get("Authorization"), request))
        last = request["contents"][-1]
        if len(REQUESTS) == 1:
            chunks = [chunk([{"functionCall": {"name": "call_mcp_tool", "id": "call_desktop", "args": {
                "ServerName": "botbox-desktop", "ToolName": "fixture_desktop", "Arguments": {},
            }}}], True)]
        else:
            assert len(REQUESTS) != 2 or any("functionResponse" in p for p in last["parts"]), last
            chunks = [chunk([{"text": "fixture "}], False), chunk([{"text": f"reply {len(REQUESTS)}"}], True)]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for item in chunks:
            self.wfile.write(f"data: {json.dumps(item)}\r\n\r\n".encode())


def run_agy(home, endpoint, prompt, conversation=None, payload_key="event"):
    env = {
        "HOME": str(home), "PATH": os.environ["PATH"], "TERM": "xterm-256color",
        "GEMINI_API_KEY": "botbox-placeholder",
        "GOOGLE_GEMINI_BASE_URL": endpoint + "/llm/google",
    }
    args = [
        "agy", "--input-format", "stream-json", "--output-format", "stream-json",
        "--print-timeout", "60s", "--settings", SETTINGS, "--dangerously-skip-permissions",
        "--model", MODEL,
    ]
    if conversation:
        args += ["--conversation", conversation]
    line = json.dumps({payload_key: "user", "message": {"role": "user", "content": prompt}})
    result = subprocess.run(
        args, cwd=home, env=env, input=line + "\n", capture_output=True,
        text=True, check=False, timeout=90,
    )
    records = [json.loads(item) for item in result.stdout.splitlines() if item.strip()]
    return result, records


with ThreadingHTTPServer(("127.0.0.1", 0), Fixture) as server, tempfile.TemporaryDirectory() as tmp:
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        home = Path(tmp)
        endpoint = f"http://127.0.0.1:{server.server_port}"
        config = home / ".gemini" / "config"
        config.mkdir(parents=True)
        (config / "mcp_config.json").write_text(json.dumps({"mcpServers": {
            "botbox-desktop": {"url": endpoint + "/mcp"},
        }}))

        # The pre-1.5.9 input envelope is rejected; the agent must send "event".
        legacy, legacy_records = run_agy(home, endpoint, "legacy prompt", payload_key="type")
        assert legacy_records and legacy_records[-1]["event"] == "result", (legacy.stdout, legacy.stderr)
        assert legacy_records[-1]["result"]["status"] == "ERROR", legacy_records[-1]
        assert not REQUESTS, REQUESTS

        first, records = run_agy(home, endpoint, "first fixture prompt")
        assert first.returncode == 0, (first.stdout, first.stderr)
        assert records[0]["event"] == "init", records
        conversation = records[0]["conversation_id"]
        result = records[-1]["result"]
        assert records[-1]["event"] == "result" and result["status"] == "SUCCESS", records[-1]
        # output_tokens includes thinking; input_tokens excludes cached tokens.
        assert result["usage"]["output_tokens"] == 2 * (4 + 6), result["usage"]
        assert result["usage"]["input_tokens"] == 2 * (120 - 100), result["usage"]
        assert TOOL_CALLS == ["fixture_desktop"], TOOL_CALLS
        tools = [r["step_update"] for r in records if r["event"] == "step_update" and r["step_update"]["step_type"] == "tool"]
        assert tools and all(isinstance(t["step_index"], int) for t in tools), tools
        assert tools[0]["state"] == "ACTIVE" and tools[-1]["state"] == "DONE", tools
        params = tools[-1]["tool_info"]["parameters"]
        assert tools[-1]["tool_name"] == "call_mcp_tool", tools[-1]
        assert params["ServerName"] == "botbox-desktop" and params["ToolName"] == "fixture_desktop", params
        assert tools[-1]["tool_info"]["output"] == "desktop fixture", tools[-1]
        deltas = [r["step_update"].get("text_delta", "") for r in records
                  if r["event"] == "step_update" and r["step_update"]["step_type"] == "agent_response"]
        assert "".join(deltas).strip() == "fixture reply 2", deltas

        second, resumed = run_agy(home, endpoint, "second fixture prompt", conversation)
        assert second.returncode == 0, (second.stdout, second.stderr)
        assert resumed[-1]["result"]["conversation_id"] == conversation, resumed[-1]
        history = json.dumps(REQUESTS[-1][2]["contents"])
        assert "first fixture prompt" in history and "second fixture prompt" in history, history

        for api_key, authorization, request in REQUESTS:
            assert api_key == "botbox-placeholder", api_key
            assert authorization is None, authorization
            assert "call_mcp_tool" in json.dumps(request["tools"]), request["tools"]
    finally:
        server.shutdown()
        thread.join()
