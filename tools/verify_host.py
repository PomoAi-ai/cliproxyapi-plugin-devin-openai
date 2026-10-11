#!/usr/bin/env python3
"""Verify a real CPA host against a loopback-only fake Devin gateway."""

import argparse
from datetime import datetime, timezone
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
QUOTA_PATH = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"
RESET_DAILY, RESET_WEEKLY = 2000000000, 2000600000
SESSION_KEY = "devin-session-token$dummy-a"


def varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    return bytes(result) + bytes([value])


def proto_bytes(field, value):
    return varint(field * 8 + 2) + varint(len(value)) + value


def quota_response():
    plan = b"".join(varint(field * 8) + varint(value) for field, value in (
        (14, 75), (15, 0), (17, RESET_DAILY), (18, RESET_WEEKLY)))
    return proto_bytes(1, proto_bytes(13, plan))


def content_text(content):
    return content if isinstance(content, str) else "".join(part["text"] for part in content)


class Gateway(BaseHTTPRequestHandler):
    records = []

    def log_message(self, *_):
        pass

    def reply(self, status, body, content_type="application/json"):
        raw = body if isinstance(body, bytes) else body.encode() if isinstance(body, str) else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_CONNECT(self):
        self.reply(502, {"error": "external network forbidden by local verifier"})

    def account(self):
        return {f"Bearer {SESSION_KEY}": "a", "Bearer dummy-b": "b"}.get(
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
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        if target.path == QUOTA_PATH:
            valid = (self.headers.get("Authorization") == f"Basic {SESSION_KEY}-{SESSION_KEY}"
                     and SESSION_KEY.encode() in raw and self.headers.get("Connect-Protocol-Version") == "1")
            self.records.append(("a", "quota", valid))
            self.reply(200 if valid else 401, quota_response() if valid else b"invalid test session", "application/proto")
            return
        body = json.loads(raw)
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
        if body.get("reasoning_effort"):
            message["reasoning_content"] = "local reasoning"
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
            chunks = []
            if "reasoning_content" in delta:
                response["choices"][0]["delta"] = {"role": "assistant", "reasoning_content": delta.pop("reasoning_content")}
                chunks.append(json.dumps(response))
                response["choices"][0]["delta"] = delta
            chunks.append(json.dumps(response))
            response["choices"] = [{"index": 0, "delta": {}, "finish_reason": finish}]
            chunks.append(json.dumps(response))
            self.reply(200, "".join(f"data: {chunk}\n\n" for chunk in chunks) + "data: [DONE]\n\n",
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
                    "type": "devin-openai", "api_key": SESSION_KEY if account == "a" else "dummy-b",
                    "label": f"local-{account}", "inference_url": origin,
                    "proxy_url": "direct", "disabled": False}))
            with socket.socket() as port_socket:
                port_socket.bind(("127.0.0.1", 0))
                port = port_socket.getsockname()[1]
            config = {
                "config-version": 8, "server": {"host": "127.0.0.1", "port": port,
                    "discovery": {"enabled": False}},
                "management": {"secret-key": "dummy-management", "disable-control-panel": True, "disable-auto-update-panel": True},
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
                key = "dummy-management" if path.startswith("/v8/management/") else "dummy-client"
                req = Request(f"http://127.0.0.1:{port}{path}",
                              data=None if body is None else json.dumps(body).encode(),
                              headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
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
                    # These requests fail if the model catalog strips user-specified reasoning effort.
                    chat = {"model": "devin-openai/local-b", "messages": [{"role": "user", "content": "inspect"}], "tools": [TOOL]}
                    for extra, effort in (({"reasoning_effort": "high"}, "high"),
                                          ({"model": "devin-openai/local-b(high)"}, "high")):
                        status, raw = request("/v1/chat/completions", {**chat, **extra})
                        assert status == 200 and "local reasoning" in raw, (status, raw)
                        assert Gateway.records[-1][2]["reasoning_effort"] == effort, Gateway.records[-1]
                    messages = {"model": "devin-openai/local-b", "max_tokens": 12000,
                                "messages": [{"role": "user", "content": "inspect"}],
                                "tools": [{"name": "inspect", "input_schema": TOOL["function"]["parameters"]}]}
                    for extra, effort in (({"thinking": {"type": "enabled", "budget_tokens": 8192}}, "medium"),
                                          ({"thinking": {"type": "adaptive"}, "output_config": {"effort": "high"}}, "high")):
                        status, raw = request("/v1/messages", {**messages, **extra})
                        assert status == 200 and "local reasoning" in raw, (status, raw)
                        assert Gateway.records[-1][2]["reasoning_effort"] == effort, Gateway.records[-1]
                    messages.update(stream=True, thinking={"type": "adaptive"}, output_config={"effort": "high"})
                    status, raw = request("/v1/messages", messages)
                    assert status == 200, (status, raw)
                    events = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ")]
                    assert any(e["type"] == "message_stop" for e in events), events
                    blocks = {}
                    tool_json = ""
                    for event in events:
                        if event["type"] == "content_block_start":
                            blocks[event["index"]] = event["content_block"]
                        elif event["type"] == "content_block_delta":
                            delta = event["delta"]
                            if delta["type"] == "thinking_delta":
                                block = blocks[event["index"]]
                                block["thinking"] = block.get("thinking", "") + delta["thinking"]
                            elif delta["type"] == "input_json_delta":
                                tool_json += delta["partial_json"]
                    thinking = next(b for b in blocks.values() if b["type"] == "thinking")
                    tool = next(b for b in blocks.values() if b["type"] == "tool_use")
                    assert thinking["thinking"] == "local reasoning" and tool["id"] == CALL["id"], blocks
                    tool["input"] = json.loads(tool_json)
                    assert tool["input"] == {"path": "sample"}, tool
                    messages["messages"].extend([{"role": "assistant", "content": list(blocks.values())},
                        {"role": "user", "content": [{"type": "tool_result", "tool_use_id": CALL["id"], "content": "sample result"}]}])
                    status, raw = request("/v1/messages", messages)
                    assert status == 200 and "done-b" in raw, (status, raw)
                    upstream = Gateway.records[-1][2]
                    assert upstream["reasoning_effort"] == "high", upstream
                    assert upstream["messages"][-1]["tool_call_id"] == CALL["id"], upstream
                    assert content_text(upstream["messages"][-1]["content"]) == "sample result", upstream
                    print("OBSERVED: unsigned thinking history forwarded =", any(
                        "reasoning_content" in message for message in upstream["messages"][:-1]))

                    status, raw = request("/v8/management/credentials")
                    assert status == 200, (status, raw)
                    auths = {a["name"]: a["auth_index"] for a in json.loads(raw)["files"]}
                    status, raw = request("/v8/management/plugins/devin-openai/quota", {"auth_index": auths["a.json"]})
                    assert status == 200, (status, raw)
                    buckets = {b["window"]: b for g in json.loads(raw)["groups"] for b in g["buckets"]}
                    for window, fraction, reset in (("daily", .75, RESET_DAILY), ("weekly", 0, RESET_WEEKLY)):
                        assert buckets[window]["remainingFraction"] == fraction, buckets
                        assert buckets[window]["resetTime"] == datetime.fromtimestamp(reset, timezone.utc).isoformat().replace("+00:00", "Z"), buckets
                    assert any(path == "quota" and valid for _, path, valid in Gateway.records), Gateway.records
                    status, raw = request("/v8/management/plugins/devin-openai/quota", {"auth_index": auths["b.json"]})
                    assert status == 502 and "session token" in raw, (status, raw)
                    assert not any(secret in raw for secret in (SESSION_KEY, "dummy-b")), raw
                    print("PASS: real CPA loading, account isolation, Chat/Messages tools and SSE, 403, reasoning effort/budget/suffix, thinking SSE, and quota 75%/0% with resets")
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
