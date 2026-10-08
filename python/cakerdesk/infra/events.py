from __future__ import annotations

import json
import logging
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any

logger = logging.getLogger("cakerdesk.infra.events")


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class EventSink:
    """每个 run_id 单独从 1 编号，并可选 POST 到 Go。失败只重试同一份 body。"""

    def __init__(self, post_url: str | None = None) -> None:
        self.post_url = post_url.rstrip("/") if post_url else None
        self.events: list[dict[str, Any]] = []
        self._seq: dict[str, int] = {}

    def emit(self, run_id: str, event_type: str, payload: dict[str, Any]) -> dict[str, Any]:
        self._seq[run_id] = self._seq.get(run_id, 0) + 1
        event = {
            "run_id": run_id,
            "seq": self._seq[run_id],
            "type": event_type,
            "timestamp": _now(),
            "payload": payload,
        }
        self.events.append(event)
        if self.post_url:
            self._post(event)
        return event

    def _post(self, event: dict[str, Any]) -> None:
        url = f"{self.post_url}/internal/runs/{event['run_id']}/events"
        data = json.dumps(event).encode("utf-8")
        for _ in range(3):
            request = urllib.request.Request(
                url,
                data=data,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            try:
                with urllib.request.urlopen(request, timeout=5) as response:
                    if 200 <= response.status < 300:
                        return
            except urllib.error.HTTPError as exc:
                if exc.code == 409:
                    return
                logger.warning("event post failed: %s", exc)
            except urllib.error.URLError as exc:
                logger.warning("event post failed: %s", exc)
        logger.warning("event seq=%s dropped after retries", event["seq"])


def publish(config: dict, event_type: str, payload: dict[str, Any]) -> None:
    """节点把事件写入 custom stream。没有 stream 时直接交给 EventSink。"""
    from langgraph.config import get_stream_writer

    cfg = config["configurable"]
    chunk = {"run_id": cfg["run_id"], "type": event_type, "payload": payload}
    try:
        get_stream_writer()(chunk)
    except RuntimeError:
        cfg["sink"].emit(cfg["run_id"], event_type, payload)


def fetch_run_status(go_url: str | None, run_id: str) -> str | None:
    if not go_url:
        return None
    url = f"{go_url.rstrip('/')}/internal/runs/{run_id}"
    try:
        with urllib.request.urlopen(url, timeout=2) as response:
            body = json.loads(response.read().decode())
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError):
        return None
    return body.get("status")
