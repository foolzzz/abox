#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

BASE = os.environ.get("AGENTBOX_API_URL", "http://127.0.0.1:8080/api/v1").rstrip("/")
OWNER = os.environ.get("AGENTBOX_DEV_USER", "owner@example.com")
WORKSPACE_PATH = os.environ.get("AGENTBOX_E2E_WORKSPACE", os.getcwd())


def request(login: str, method: str, path: str, body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    actual = {"X-AgentBox-Dev-User": login}
    if data is not None:
        actual["Content-Type"] = "application/json"
    if headers:
        actual.update(headers)
    req = urllib.request.Request(BASE + path, data=data, headers=actual, method=method)
    with urllib.request.urlopen(req, timeout=30) as response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else None


def wait_terminal(login: str, box_id: str, after: int = 0, terminal_count: int = 1):
    req = urllib.request.Request(
        f"{BASE}/boxes/{box_id}/events",
        headers={
            "X-AgentBox-Dev-User": login,
            "Accept": "text/event-stream",
            "Last-Event-ID": str(after),
        },
    )
    terminal = []
    events = []
    with urllib.request.urlopen(req, timeout=180) as response:
        frame = {}
        for raw in response:
            line = raw.decode().rstrip("\n")
            if not line:
                if frame.get("data"):
                    event = json.loads(frame["data"])
                    event["type"] = frame.get("event", event.get("type"))
                    events.append(event)
                    if event["type"] in {"run.completed", "run.failed"}:
                        terminal.append(event)
                        if len(terminal) == terminal_count:
                            return events
                frame = {}
                continue
            if line.startswith("id:"):
                frame["id"] = line[3:].strip()
            elif line.startswith("event:"):
                frame["event"] = line[6:].strip()
            elif line.startswith("data:"):
                frame["data"] = line[5:].strip()
    raise RuntimeError("stream ended before terminal event")


def main() -> int:
    stamp = int(time.time() * 1000)
    operator_login = f"operator-{stamp}@example.com"
    viewer_login = f"viewer-{stamp}@example.com"

    _, operator_meta = request(operator_login, "GET", "/meta")
    _, viewer_meta = request(viewer_login, "GET", "/meta")
    if operator_meta["currentUser"]["role"] != "viewer" or viewer_meta["currentUser"]["role"] != "viewer":
        raise AssertionError("new users must start as viewers")

    _, members = request(OWNER, "GET", "/members")
    operator = next(item for item in members if item["login"] == operator_login)
    request(OWNER, "PATCH", f"/members/{operator['id']}/role", {"role": "operator"})

    _, hosts = request(OWNER, "GET", "/hosts")
    host = next(item for item in hosts if item["status"] == "online" and "claude" in item["runtimes"])
    _, workspaces = request(OWNER, "GET", "/workspaces")
    workspace = next(item for item in workspaces if item["hostId"] == host["id"] and item["path"] == WORKSPACE_PATH)

    _, agent = request(OWNER, "POST", "/agents", {
        "name": f"Claude multi-user {stamp}",
        "runtimeType": "claude",
        "model": "",
        "systemPrompt": "Reply exactly as requested.",
    })
    _, box = request(OWNER, "POST", "/boxes", {
        "name": f"Multi-user box {stamp}",
        "agentId": agent["id"],
        "hostId": host["id"],
        "workspaceId": workspace["id"],
    })
    request(OWNER, "PUT", f"/boxes/{box['id']}/acl", {
        "entries": [{"userId": operator["id"], "role": "operator"}],
    })

    viewer_status = None
    try:
        request(viewer_login, "POST", f"/boxes/{box['id']}/messages", {
            "content": "must be rejected",
            "delivery": "prompt",
        }, {"Idempotency-Key": str(uuid.uuid4())})
    except urllib.error.HTTPError as error:
        viewer_status = error.code
    if viewer_status != 403:
        raise AssertionError(f"viewer prompt expected 403, received {viewer_status}")

    expected = "Multi-user Claude passed"
    _, sent = request(operator_login, "POST", f"/boxes/{box['id']}/messages", {
        "content": f"Reply with exactly: {expected}",
        "delivery": "prompt",
    }, {"Idempotency-Key": str(uuid.uuid4())})
    events = wait_terminal(operator_login, box["id"])
    if events[-1]["type"] != "run.completed":
        raise RuntimeError(f"runtime failed: {events[-1]}")

    _, messages = request(operator_login, "GET", f"/boxes/{box['id']}/messages")
    if messages[-2]["authorName"] != operator_login.split("@", 1)[0]:
        raise AssertionError("operator attribution was not preserved")
    if messages[-1]["role"] != "assistant" or messages[-1]["content"].strip() != expected:
        raise AssertionError(f"unexpected assistant response: {messages[-1]}")

    print(json.dumps({
        "boxId": box["id"],
        "runId": sent["runId"],
        "operator": operator_login,
        "viewerDenied": viewer_status,
        "assistant": messages[-1]["content"],
    }))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (urllib.error.URLError, TimeoutError, RuntimeError, AssertionError, StopIteration) as error:
        print(f"phase2 e2e failed: {error}", file=sys.stderr)
        raise SystemExit(1)
