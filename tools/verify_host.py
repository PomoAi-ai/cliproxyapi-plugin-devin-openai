#!/usr/bin/env python3
"""Verify a real CPA host against a loopback-only fake Devin gateway."""

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import ProxyHandler, Request, build_opener


TOOL = {"type": "function", "function": {"name": "inspect", "parameters": {
    "type": "object", "properties": {"path": {"type": "string"}}}}}
CALL = {"id": "call_local", "type": "function", "function": {
    "name": "inspect", "arguments": '{"path":"sample"}'}}


def content_text(content):
    return content if isinstance(content, str) else "".join(part["text"] for part in content)


class Gateway(BaseHTTPRequestHandler):
    records = []

    def log_message(self, *_):
        pass

    def reply(self, status, body, content_type="application/json"):
        raw = body.encode() if isinstance(body, str) else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_CONNECT(self):
        self.reply(502, {"error": "external network forbidden by local verifier"})

    def account(self):
        return {"Bearer dummy-a": "a", "Bearer dummy-b": "b"}.get(
            self.headers.get("Authorization"))

    def do_GET(self):
        target = urlsplit(self.path)
        if target.hostname and target.hostname != "127.0.0.1":
            self.reply(502, {"error": "external network forbidden by local verifier"})
            return
        account = self.account()
        if target.path != "/api/v1/models" or not account:
            self.reply(401, {"error": {"message": "dummy credential required"}})
            return
        self.records.append((account, "models", None))
        self.reply(200, {"object": "list", "data": [
            {"id": f"local-{account}", "object": "model", "owned_by": "local"},
            {"id": f"denied-{account}", "object": "model", "owned_by": "local"}]})

    def do_POST(self):
        target = urlsplit(self.path)
        if target.hostname and target.hostname != "127.0.0.1":
            self.reply(502, {"error": "external network forbidden by local verifier"})
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        account = self.account()
        self.records.append((account, target.path, body))
        if target.path != "/api/v1/chat/completions" or not account:
            self.reply(401, {"error": {"message": "dummy credential required"}})
            return
        if body["model"] == f"denied-{account}":
            self.reply(403, {"error": {"message": "local policy denial", "type": "permission_denied"}})
            return
        if body["model"] != f"local-{account}":
            self.reply(409, {"error": {"message": "credential crossed account boundary"}})
            return
        has_result = body["messages"][-1]["role"] == "tool"
        message = {"role": "assistant", "content": f"done-{account}"} if has_result else {
            "role": "assistant", "content": None, "tool_calls": [CALL]}
        finish = "stop" if has_result else "tool_calls"
        response = {"id": "chatcmpl-local", "object": "chat.completion", "created": 1,
                    "model": body["model"], "choices": [{"index": 0, "message": message,
                    "finish_reason": finish}], "usage": {"prompt_tokens": 1,
                    "completion_tokens": 1, "total_tokens": 2}}
        if body.get("stream"):
            delta = dict(message)
            if not has_result:
                delta["tool_calls"] = [{"index": 0, **CALL}]
            response["object"] = "chat.completion.chunk"
            response["choices"] = [{"index": 0, "delta": delta, "finish_reason": None}]
            first = json.dumps(response)
            response["choices"] = [{"index": 0, "delta": {}, "finish_reason": finish}]
            self.reply(200, f"data: {first}\n\ndata: {json.dumps(response)}\n\ndata: [DONE]\n\n",
                       "text/event-stream")
        else:
            self.reply(200, response)


def verify(host, plugin):
    Gateway.records = []
    gateway = ThreadingHTTPServer(("127.0.0.1", 0), Gateway)
    thread = threading.Thread(target=gateway.serve_forever, daemon=True)
    thread.start()
    origin = f"http://127.0.0.1:{gateway.server_port}"
    opener = build_opener(ProxyHandler({}))
    try:
        with tempfile.TemporaryDirectory(prefix="devin-openai-host-") as folder:
            root = Path(folder)
            (root / "auth").mkdir()
            (root / "plugins").mkdir()
            shutil.copy2(plugin, root / "plugins" / plugin.name)
            for account in ("a", "b"):
                (root / "auth" / f"{account}.json").write_text(json.dumps({
                    "type": "devin-openai", "api_key": f"dummy-{account}",
                    "label": f"local-{account}", "inference_url": origin,
                    "proxy_url": "direct", "disabled": False}))
            with socket.socket() as port_socket:
                port_socket.bind(("127.0.0.1", 0))
                port = port_socket.getsockname()[1]
            config = {
                "config-version": 8, "server": {"host": "127.0.0.1", "port": port,
                    "discovery": {"enabled": False}},
                "management": {"disable-control-panel": True, "disable-auto-update-panel": True},
                "access": {"api-keys": ["dummy-client"]}, "oauth": {"auth-dir": str(root / "auth")},
                "plugins": {"enabled": True, "dir": str(root / "plugins"),
                            "configs": {"devin-openai": {"enabled": True}}},
                "routing": {"force-model-prefix": True, "retry": {"request-retry": 0}},
                "requests": {"proxy-url": origin},
                "observability": {"usage": {"usage-statistics-enabled": False}}}
            config_path = root / "config.yaml"
            config_path.write_text(json.dumps(config))  # JSON is valid YAML.
            env = {key: value for key, value in os.environ.items()
                   if key in ("PATH", "TMPDIR", "LANG", "SYSTEMROOT")}
            env.update(HOME=str(root), HTTP_PROXY=origin, HTTPS_PROXY=origin,
                       ALL_PROXY=origin, NO_PROXY="127.0.0.1,localhost")

            def request(path, body=None):
                req = Request(f"http://127.0.0.1:{port}{path}",
                              data=None if body is None else json.dumps(body).encode(),
                              headers={"Authorization": "Bearer dummy-client", "Content-Type": "application/json"})
                try:
                    with opener.open(req, timeout=10) as response:
                        return response.status, response.read().decode()
                except HTTPError as error:
                    return error.code, error.read().decode()

            with (root / "host.log").open("w+") as log:
                process = subprocess.Popen([str(host), "-config", str(config_path), "-local-model"],
                                           cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    deadline = time.monotonic() + 30
                    expected = {f"devin-openai/local-{a}" for a in ("a", "b")}
                    while time.monotonic() < deadline:
                        if process.poll() is not None:
                            raise RuntimeError(f"CPA exited with {process.returncode}")
                        try:
                            status, raw = request("/v1/models")
                            if status == 200 and expected <= {m["id"] for m in json.loads(raw)["data"]}:
                                break
                        except (URLError, TimeoutError):
                            pass
                        time.sleep(0.5)
                    else:
                        raise AssertionError("plugin model discovery did not finish in 30 seconds")
                    for account in ("a", "b"):
                        for stream in (False, True):
                            messages = [{"role": "system", "content": "local subagent identity — preserve exactly"},
                                        {"role": "user", "content": "inspect sample"}]
                            body = {"model": f"devin-openai/local-{account}", "messages": messages,
                                    "tools": [TOOL], "stream": stream}
                            status, raw = request("/v1/chat/completions", body)
                            assert status == 200, (status, raw)
                            if stream:
                                events = [json.loads(line[6:]) for line in raw.splitlines()
                                          if line.startswith("data: ") and line != "data: [DONE]"]
                                calls = [call for event in events for choice in event["choices"]
                                         for call in choice.get("delta", {}).get("tool_calls", [])]
                                assert calls and calls[0]["id"] == CALL["id"], raw
                                assert "data: [DONE]" in raw, raw
                            else:
                                assert json.loads(raw)["choices"][0]["message"]["tool_calls"] == [CALL], raw
                            messages.extend([{"role": "assistant", "content": None, "tool_calls": [CALL]},
                                             {"role": "tool", "tool_call_id": CALL["id"], "content": "sample result"}])
                            status, raw = request("/v1/chat/completions", body)
                            assert status == 200 and f"done-{account}" in raw, (status, raw)
                    messages = [{"role": "user", "content": "inspect sample"}]
                    body = {"model": "devin-openai/local-b", "max_tokens": 100,
                            "system": "local subagent identity — preserve exactly", "messages": messages,
                            "tools": [{"name": "inspect", "input_schema": TOOL["function"]["parameters"]}]}
                    status, raw = request("/v1/messages", body)
                    assert status == 200, (status, raw)
                    result = json.loads(raw)
                    calls = [item for item in result["content"] if item["type"] == "tool_use"]
                    assert calls and calls[0]["id"] == CALL["id"] and calls[0]["input"] == {"path": "sample"}, raw
                    messages.extend([{"role": "assistant", "content": result["content"]},
                                     {"role": "user", "content": [{"type": "tool_result",
                                      "tool_use_id": CALL["id"], "content": "sample result"}]}])
                    status, raw = request("/v1/messages", body)
                    assert status == 200 and "done-b" in raw, (status, raw)
                    status, raw = request("/v1/chat/completions", {
                        "model": "devin-openai/denied-a", "messages": [{"role": "user", "content": "deny"}]})
                    assert status == 403 and "local policy denial" in raw, (status, raw)
                    assert {a for a, path, _ in Gateway.records if path == "models"} == {"a", "b"}
                    chats = [(a, b) for a, path, b in Gateway.records if path.endswith("chat/completions")]
                    assert len(chats) == 11, chats
                    for account, body in chats[:-1]:
                        assert body["model"] == f"local-{account}", body
                        assert content_text(body["messages"][0]["content"]) == "local subagent identity — preserve exactly", body
                        assert body["tools"][0]["function"]["name"] == "inspect", body
                        if body["messages"][-1]["role"] == "tool":
                            assert body["messages"][-1]["tool_call_id"] == CALL["id"], body
                            assert content_text(body["messages"][-1]["content"]) == "sample result", body
                    print("PASS: real CPA plugin loading, per-account models/credentials, Chat and Messages tool roundtrips, SSE, and upstream 403")
                except BaseException:
                    log.flush()
                    log.seek(0)
                    print(log.read())
                    raise
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
    finally:
        gateway.shutdown()
        gateway.server_close()
        thread.join()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", type=Path, required=True)
    parser.add_argument("--plugin", type=Path, required=True)
    args = parser.parse_args()
    verify(args.host.resolve(strict=True), args.plugin.resolve(strict=True))
