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
USER = os.environ.get("AGENTBOX_DEV_USER", "owner@example.com")
WORKSPACE_PATH = os.environ.get("AGENTBOX_E2E_WORKSPACE", os.getcwd())
EXPECTED = "AgentBox Phase 1 E2E passed"


def request(method: str, path: str, body: object | None = None, headers: dict[str, str] | None = None):
    data = None if body is None else json.dumps(body).encode()
    actual_headers = {"X-AgentBox-Dev-User": USER}
    if data is not None:
        actual_headers["Content-Type"] = "application/json"
    if headers:
        actual_headers.update(headers)
    req = urllib.request.Request(BASE + path, data=data, headers=actual_headers, method=method)
    with urllib.request.urlopen(req, timeout=30) as response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else None


def stream_until_terminal(box_id: str, timeout_seconds: int = 120):
    req = urllib.request.Request(
        f"{BASE}/boxes/{box_id}/events",
        headers={"X-AgentBox-Dev-User": USER, "Accept": "text/event-stream"},
    )
    deadline = time.monotonic() + timeout_seconds
    events: list[dict] = []
    with urllib.request.urlopen(req, timeout=timeout_seconds) as response:
        frame: dict[str, str] = {}
        for raw in response:
            if time.monotonic() > deadline:
                raise TimeoutError("agent run did not reach a terminal event")
            line = raw.decode().rstrip("\n")
            if not line:
                if "data" in frame:
                    event = json.loads(frame["data"])
                    event["type"] = frame.get("event", event.get("type"))
                    events.append(event)
                    if event["type"] in {"run.completed", "run.failed"}:
                        return events
                frame = {}
                continue
            if line.startswith("id:"):
                frame["id"] = line[3:].strip()
            elif line.startswith("event:"):
                frame["event"] = line[6:].strip()
            elif line.startswith("data:"):
                frame["data"] = line[5:].strip()
    raise RuntimeError("event stream ended before a terminal event")


def main() -> int:
    _, hosts = request("GET", "/hosts")
    host = next((item for item in hosts if item["status"] == "online" and "omp" in item["runtimes"]), None)
    if not host:
        raise RuntimeError("no online OMP host")

    stamp = int(time.time() * 1000)
    _, agent = request(
        "POST",
        "/agents",
        {
            "name": f"Phase 1 E2E {stamp}",
            "runtimeType": "omp",
            "model": "",
            "systemPrompt": "Reply exactly as requested.",
        },
    )

    _, workspaces = request("GET", "/workspaces")
    workspace = next(
        (item for item in workspaces if item["hostId"] == host["id"] and item["path"] == WORKSPACE_PATH),
        None,
    )
    if workspace is None:
        _, workspace = request(
            "POST",
            "/workspaces",
            {
                "hostId": host["id"],
                "name": f"e2e-{stamp}",
                "path": WORKSPACE_PATH,
                "kind": "existing",
            },
        )

    _, box = request(
        "POST",
        "/boxes",
        {
            "name": f"Phase 1 E2E {stamp}",
            "agentId": agent["id"],
            "hostId": host["id"],
            "workspaceId": workspace["id"],
        },
    )
    _, sent = request(
        "POST",
        f"/boxes/{box['id']}/messages",
        {"content": f"Reply with exactly: {EXPECTED}", "delivery": "prompt"},
        {"Idempotency-Key": str(uuid.uuid4())},
    )

    events = stream_until_terminal(box["id"])
    if events[-1]["type"] != "run.completed":
        raise RuntimeError(f"run failed: {events[-1]}")

    _, messages = request("GET", f"/boxes/{box['id']}/messages")
    assistant = [item for item in messages if item["role"] == "assistant"]
    if not assistant or assistant[-1]["content"].strip() != EXPECTED:
        raise AssertionError(f"unexpected assistant messages: {assistant}")
    if sent["status"] not in {"dispatched", "applied"}:
        raise AssertionError(f"unexpected initial message status: {sent['status']}")

    _, snapshot = request("GET", f"/boxes/{box['id']}")
    if snapshot["status"] != "idle":
        raise AssertionError(f"box did not return to idle: {snapshot['status']}")

    print(json.dumps({
        "boxId": box["id"],
        "runId": sent["runId"],
        "eventCount": len(events),
        "assistant": assistant[-1]["content"],
        "boxStatus": snapshot["status"],
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (urllib.error.URLError, TimeoutError, RuntimeError, AssertionError) as error:
        print(f"phase1 e2e failed: {error}", file=sys.stderr)
        raise SystemExit(1)
