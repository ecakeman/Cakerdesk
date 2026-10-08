from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Any

from langgraph.store.memory import InMemoryStore


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class MemoryStore:
    """项目记忆。默认用进程内 Store；PostgreSQL 由 open_memory 换成 PostgresStore。"""

    def __init__(self, store=None) -> None:
        self.store = store if store is not None else InMemoryStore()

    def write(self, project_id: str, run_id: str, items: list[dict[str, str]]) -> list[dict[str, Any]]:
        namespace = ("project", project_id, "memory")
        written = []
        for item in items:
            kind = item.get("kind")
            if kind not in {"fact", "lesson"}:
                continue
            content = str(item.get("content") or "").strip()[:500]
            if not content:
                continue
            created = _now()
            value = {"kind": kind, "content": content, "source_run_id": run_id, "created_at": created}
            self.store.put(namespace, uuid.uuid4().hex, value)
            written.append({"kind": kind, "content": content, "created_at": created})
        return written

    def read_for_context(self, project_id: str, limit: int = 20) -> list[dict[str, Any]]:
        namespace = ("project", project_id, "memory")
        found = self.store.search(namespace, limit=100)
        rows = []
        for item in found:
            value = dict(item.value)
            value.setdefault("created_at", item.created_at.strftime("%Y-%m-%dT%H:%M:%SZ"))
            rows.append(value)
        rows.sort(key=lambda row: row.get("created_at") or "")
        return rows[-limit:]

    def close(self) -> None:
        closer = getattr(self.store, "close", None)
        if closer:
            closer()


def open_memory(database_url: str | None):
    if not database_url:
        raise RuntimeError("CAKERDESK_DATABASE_URL 未设置，不使用 SQLite")
    from langgraph.store.postgres import PostgresStore

    context = PostgresStore.from_conn_string(database_url)
    store = context.__enter__()
    store.setup()
    memory = MemoryStore(store)
    memory._context = context
    return memory
