"""内核事件先攒着。满 20 条或到点再送，complete 前必须送出。"""

from __future__ import annotations

import asyncio

import httpx

_by_run: dict[str, EventSink] = {}


def bind(run_id: str, sink: EventSink) -> None:
    _by_run[run_id] = sink


def unbind(run_id: str) -> None:
    _by_run.pop(run_id, None)


def sink_for(config: dict) -> EventSink | None:
    conf = config.get("configurable") or {}
    return _by_run.get(conf.get("thread_id"))


def assistant_payload(message) -> dict:
    content = message.content if isinstance(message.content, str) else ""
    calls = []
    for call in getattr(message, "tool_calls", None) or []:
        calls.append({
            "id": call.get("id"),
            "name": call.get("name"),
            "args": call.get("args") or {},
        })
    meta = getattr(message, "usage_metadata", None) or {}
    if meta:
        usage = {
            "prompt_tokens": _meta(meta, "input_tokens"),
            "completion_tokens": _meta(meta, "output_tokens"),
            "total_tokens": _meta(meta, "total_tokens"),
        }
    else:
        usage = {}
    return {"content": content, "tool_calls": calls, "usage": usage}


def _meta(meta, key: str) -> int:
    if isinstance(meta, dict):
        return int(meta.get(key) or 0)
    return int(getattr(meta, key, 0) or 0)


class EventSink:
    def __init__(self, client: httpx.AsyncClient, api: str, token: str, worker_id: str, attempt: int, run_id: str, batch: int) -> None:
        self.client = client
        self.api = api
        self.token = token
        self.worker_id = worker_id
        self.attempt = attempt
        self.run_id = run_id
        self.batch = batch
        self.buf: list[dict] = []
        self.lock = asyncio.Lock()

    async def add(self, event: dict) -> None:
        async with self.lock:
            self.buf.append(event)
            if len(self.buf) >= self.batch:
                await self._flush_locked()

    async def flush(self) -> None:
        async with self.lock:
            await self._flush_locked()

    async def _flush_locked(self) -> None:
        if not self.buf:
            return
        batch = self.buf
        self.buf = []
        try:
            res = await self.client.post(
                f"{self.api}/internal/runs/{self.run_id}/events",
                headers={"Authorization": f"Bearer {self.token}"},
                json={"worker_id": self.worker_id, "attempt": self.attempt, "events": batch},
            )
            res.raise_for_status()
        except httpx.HTTPError:
            self.buf = batch + self.buf
            raise

    async def run(self, stop: asyncio.Event, flush_ms: int) -> None:
        interval = max(flush_ms, 1) / 1000
        while not stop.is_set():
            try:
                await asyncio.wait_for(stop.wait(), interval)
            except TimeoutError:
                await self.flush()
