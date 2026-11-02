"""Exercise the pinned interactive Codex CLI without external networking."""

import errno
import fcntl
import http.server
import json
import os
import pty
import queue
import re
import select
import signal
import struct
import subprocess
import tempfile
import termios
import threading
import time
from pathlib import Path

PROMPT = "Read the fixture using an available shell tool."
REPLY = "DESKTOP_CONTRACT_OK"
RESUME_REPLY = "RESUME_CONTRACT_OK"
requests = queue.Queue()
output_token_counts = {"input_tokens": 8, "output_tokens": 3, "total_tokens": 11}


def event(kind, **fields):
    return f"event: {kind}\ndata: {json.dumps({'type': kind, **fields})}\n\n"


def reply_events(rid, text):
    item = {"type": "message", "id": f"msg_{rid}", "role": "assistant", "status": "completed",
            "content": [{"type": "output_text", "text": text, "annotations": []}]}
    return [
        event("response.output_item.added", output_index=0, item={**item, "content": []}),
        event("response.content_part.added", output_index=0, item_id=item["id"], content_index=0,
              part={"type": "output_text", "text": "", "annotations": []}),
        event("response.output_text.delta", output_index=0, item_id=item["id"], content_index=0, delta=text),
        event("response.output_item.done", output_index=0, item=item),
    ]


class Fixture(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def do_GET(self):
        requests.put((self.command, self.path, None))
        self.send_response(204 if self.path == "/llm/openai-plan/ready" else 404)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        requests.put((self.command, self.path, body))
        if (self.path not in ("/llm/openai-plan/v1/responses", "/llm/openai/v1/responses")
                or self.headers.get("Authorization") != "Bearer botbox-placeholder"):
            self.send_error(403)
            return
        flattened = json.dumps(body["input"])
        is_resume = "Continue with the resume fixture." in flattened
        has_output = any(item.get("type") == "function_call_output" for item in body["input"])
        rid = "resp_resume" if is_resume else "resp_reply" if has_output else "resp_tool"
        data = [event("response.created", response={"id": rid, "object": "response", "status": "in_progress"})]
        if is_resume or has_output:
            data.extend(reply_events(rid, RESUME_REPLY if is_resume else REPLY))
        else:
            tool = next((tool for tool in body.get("tools", []) if tool.get("name") == "shell_command"), None)
            if tool is None:
                tool = next((tool for tool in body.get("tools", []) if tool.get("name") == "exec_command"), None)
            if tool is None:
                self.send_error(422, "no fixture shell tool")
                return
            command = {"type": "function_call", "id": "fc_fixture", "call_id": "call_fixture",
                       "name": tool["name"], "arguments": json.dumps({"command" if tool["name"] == "shell_command"
                                                                    else "cmd": "cat fixture.txt"})}
            data.extend([event("response.output_item.added", output_index=0, item={**command, "arguments": ""}),
                         event("response.output_item.done", output_index=0, item=command)])
        data.append(event("response.completed", response={"id": rid, "status": "completed", "usage": output_token_counts}))
        payload = "".join(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def run_terminal(args, expected, env, directory):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 42, 120, 0, 0))
    process = subprocess.Popen(
        ["/usr/local/bin/codex", "--no-alt-screen", *args], stdin=slave, stdout=slave,
        stderr=slave, env=env, cwd=directory, start_new_session=True,
    )
    os.close(slave)
    output = bytearray()
    deadline = time.monotonic() + 25
    try:
        while time.monotonic() < deadline:
            readable, _, _ = select.select([master], [], [], max(0, deadline - time.monotonic()))
            if not readable:
                break
            try:
                chunk = os.read(master, 65536)
            except OSError as exc:
                if exc.errno != errno.EIO:
                    raise
                break
            if not chunk:
                break
            output.extend(chunk)
            clean = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", output)
            if expected.encode() in clean:
                return
        raise AssertionError(f"Codex did not show {expected}. Terminal: {output[-2500:]!r}")
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        os.close(master)


def main():
    with tempfile.TemporaryDirectory() as home, http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            root = Path(home)
            project = root / "project"
            project.mkdir()
            (project / "fixture.txt").write_text("LOCAL_TOOL_FIXTURE", encoding="utf-8")
            config = root / ".codex"
            config.mkdir()
            (config / "config.toml").write_text(
                f'check_for_update_on_startup = false\n[projects."{project}"]\ntrust_level = "trusted"\n',
                encoding="utf-8",
            )
            env = {**os.environ, "HOME": home, "CODEX_HOME": str(config), "TERM": "xterm-256color",
                   "HTTP_PROXY": f"http://127.0.0.1:{server.server_port}", "NO_PROXY": "127.0.0.1"}
            run_terminal([PROMPT], REPLY, env, project)
            run_terminal(["resume", "--last", "Continue with the resume fixture."], RESUME_REPLY, env, project)
            calls = []
            while not requests.empty():
                calls.append(requests.get_nowait())
            inference = [body for method, path, body in calls if method == "POST" and path == "/llm/openai-plan/v1/responses"]
            assert len(inference) >= 3, [(method, path) for method, path, _ in calls]
            assert all(body.get("store") is False and body.get("stream") is True for body in inference)
            outputs = [item.get("output", "") for body in inference for item in body["input"]
                       if item.get("type") == "function_call_output"]
            assert any("LOCAL_TOOL_FIXTURE" in str(output) for output in outputs), outputs
            forbidden = {"background", "conversation", "max_output_tokens", "max_tool_calls", "moderation",
                         "multi_agent", "prompt", "previous_response_id", "temperature", "top_logprobs",
                         "top_p", "truncation", "user"}
            assert not {key for body in inference for key in forbidden.intersection(body)}, [list(body) for body in inference]
            assert PROMPT in json.dumps(inference[-1]["input"])
            api_env = {**env, "OPENAI_API_KEY": "botbox-placeholder",
                       "OPENAI_BASE_URL": env["HTTP_PROXY"] + "/llm/openai/v1"}
            try:
                batch = subprocess.run(
                    ["/usr/local/bin/codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox",
                     "--skip-git-repo-check", "-", "--model", "gpt-6.1-sol"],
                    input="Continue with the resume fixture.", capture_output=True, text=True,
                    env=api_env, cwd=project, timeout=25, check=False,
                )
            except subprocess.TimeoutExpired as error:
                routes = [(method, path) for method, path, _ in list(requests.queue)]
                raise AssertionError(f"API-key CLI timed out. Routes: {routes}, stderr: {(error.stderr or b'')[-1200:]!r}") from error
            assert batch.returncode == 0 and RESUME_REPLY in batch.stdout, batch.stderr[-1000:]
            assert any(path == "/llm/openai/v1/responses" for _, path, _ in list(requests.queue))
            print("Interactive Codex, resumed context, local tools, and API-key CLI mode verified.")
        finally:
            server.shutdown()
            thread.join(timeout=5)


if __name__ == "__main__":
    main()
