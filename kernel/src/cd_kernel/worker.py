import argparse
import asyncio
import os
import socket
import sys
from importlib.metadata import version

import httpx
from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver

from cd_kernel.checkpoint import setup_tables
from cd_kernel.graph import build_graph, first_input


def main() -> None:
    parser = argparse.ArgumentParser(prog="cd-kernel")
    parser.add_argument("--version", action="store_true")
    parser.add_argument("command", nargs="?", choices=["setup"])
    args = parser.parse_args()
    if args.version:
        print(version("cd-kernel"))
        return
    if args.command == "setup":
        asyncio.run(setup_tables())
        return
    asyncio.run(run_forever())


async def run_forever() -> None:
    api = os.environ.get("CD_KERNEL_API_URL", "").rstrip("/")
    token = os.environ.get("CD_INTERNAL_TOKEN", "")
    db = os.environ.get("CD_KERNEL_DATABASE_URL", "")
    if not api or not token or not db:
        print("缺 CD_KERNEL_API_URL、CD_INTERNAL_TOKEN 或 CD_KERNEL_DATABASE_URL", file=sys.stderr)
        raise SystemExit(1)
    worker_id = f"{socket.gethostname()}-{os.getpid()}-{os.urandom(4).hex()}"
    timeout = httpx.Timeout(90)
    async with AsyncPostgresSaver.from_conn_string(db) as saver:
        graph = build_graph(saver)
        async with httpx.AsyncClient(timeout=timeout) as client:
            # 一个进程一次只跑一个 Run。跑完再领，不并行。
            while True:
                claim = await take(client, api, token, worker_id)
                if claim is None:
                    continue
                await execute(graph, client, api, token, worker_id, claim)


async def take(client: httpx.AsyncClient, api: str, token: str, worker_id: str) -> dict | None:
    try:
        res = await client.post(
            f"{api}/internal/runs/claim",
            headers={"Authorization": f"Bearer {token}"},
            json={"worker_id": worker_id, "wait_ms": 20000},
        )
    except httpx.HTTPError:
        await asyncio.sleep(0.5)
        return None
    if res.status_code == 204:
        return None
    res.raise_for_status()
    return res.json()


async def execute(graph, client, api: str, token: str, worker_id: str, claim: dict) -> None:
    run_id = claim["run_id"]
    attempt = claim["attempt"]
    cfg = {
        "configurable": {
            "thread_id": run_id,
            "worker_id": worker_id,
            "attempt": attempt,
            "api_url": api,
            "token": token,
            "model": claim["config"]["model"],
            "tools": claim["tools"],
        },
        "recursion_limit": 400,
    }
    try:
        # 已有 checkpoint 时传 None，从上次写完的节点继续，而不是再塞一条用户消息。
        snap = await graph.aget_state(cfg)
        if snap.values:
            final = await graph.ainvoke(None, cfg)
        else:
            prompt = claim["config"].get("system_prompt") or ""
            final = await graph.ainvoke(first_input(prompt, claim["input"]), cfg)
    # 图抛出来的类型不固定。不接住的话 Run 停在 running，这个进程也不再领下一个。
    except Exception as exc:  # noqa: BLE001
        await finish(client, api, token, worker_id, run_id, attempt, None, "kernel_error", str(exc))
        return
    if final.get("result") is not None:
        await finish(client, api, token, worker_id, run_id, attempt, final["result"], None, None)
        return
    code = final.get("failure") or "no_result"
    await finish(client, api, token, worker_id, run_id, attempt, None, code, "没有调用 submit_result")


async def finish(client, api, token, worker_id, run_id, attempt, result, code, message) -> None:
    if result is not None:
        body = {"worker_id": worker_id, "attempt": attempt, "status": "succeeded", "result": result}
    else:
        body = {
            "worker_id": worker_id,
            "attempt": attempt,
            "status": "failed",
            "error": {"code": code, "message": message},
        }
    res = await client.post(
        f"{api}/internal/runs/{run_id}/complete",
        headers={"Authorization": f"Bearer {token}"},
        json=body,
    )
    res.raise_for_status()
