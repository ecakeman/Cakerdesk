from __future__ import annotations

import sqlite3
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class MemoryStore:
    """project_memory 表。本地用 SQLite，PostgreSQL URL 走同一套 SQL 方言由调用方选择驱动。"""

    def __init__(self, path: str | Path) -> None:
        self.path = str(path)
        Path(self.path).parent.mkdir(parents=True, exist_ok=True)
        self.conn = sqlite3.connect(self.path, check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self.conn.execute(
            """
            CREATE TABLE IF NOT EXISTS project_memory (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                project_id TEXT NOT NULL,
                kind TEXT NOT NULL,
                content TEXT NOT NULL,
                source_run_id TEXT NOT NULL,
                created_at TEXT NOT NULL
            )
            """
        )
        self.conn.commit()

    def write(self, project_id: str, run_id: str, items: list[dict[str, str]]) -> list[dict[str, Any]]:
        written = []
        for item in items:
            kind = item["kind"]
            if kind not in {"fact", "lesson"}:
                continue
            content = item["content"].strip()[:500]
            if not content:
                continue
            created = _now()
            self.conn.execute(
                """
                INSERT INTO project_memory (project_id, kind, content, source_run_id, created_at)
                VALUES (?, ?, ?, ?, ?)
                """,
                (project_id, kind, content, run_id, created),
            )
            written.append({"kind": kind, "content": content, "created_at": created})
        self.conn.commit()
        return written

    def read_for_context(self, project_id: str, limit: int = 20) -> list[dict[str, Any]]:
        rows = self.conn.execute(
            """
            SELECT kind, content, source_run_id, created_at
            FROM project_memory
            WHERE project_id = ?
            ORDER BY id DESC
            LIMIT ?
            """,
            (project_id, limit),
        ).fetchall()
        ordered = [dict(row) for row in reversed(rows)]
        return ordered

    def close(self) -> None:
        self.conn.close()
