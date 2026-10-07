from __future__ import annotations

import json
import logging
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any

logger = logging.getLogger("cakerdesk.events")


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class EventSink:
    """分配单调 seq，并可选 POST 到 Go。失败只重试同一份 body。"""

    def __init__(self, post_url: str | None = None) -> None:
        self.post_url = post_url.rstrip("/") if post_url else None
        self.events: list[dict[str, Any]] = []
        self._seq = 0

    def emit(self, run_id: str, event_type: str, payload: dict[str, Any]) -> dict[str, Any]:
        self._seq += 1
        event = {
            "run_id": run_id,
            "seq": self._seq,
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
