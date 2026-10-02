"""checkpoint 表建在 lg。cd_migrate 的默认 search_path 不是 lg，所以这里要自己设。"""

from __future__ import annotations

import os

from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
from psycopg import AsyncConnection
from psycopg.rows import dict_row


async def setup_tables() -> None:
    url = os.environ.get("CD_KERNEL_SETUP_DATABASE_URL")
    if not url:
        raise SystemExit("缺环境变量 CD_KERNEL_SETUP_DATABASE_URL")
    async with await AsyncConnection.connect(
        url, autocommit=True, prepare_threshold=0, row_factory=dict_row
    ) as conn:
        await conn.execute("SET search_path TO lg")
        saver = AsyncPostgresSaver(conn)
        await saver.setup()
