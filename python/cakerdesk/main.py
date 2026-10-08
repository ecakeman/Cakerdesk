from __future__ import annotations

import asyncio
import os

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from cakerdesk.events import EventSink
from cakerdesk.graph import build_graph, execute_run
from cakerdesk.memory import MemoryStore

CANCEL_FLAGS: dict[str, bool] = {}


class RunIn(BaseModel):
    project_id: str
    thread_id: str
    run_id: str
    goal: str
    workspace_root: str


def create_app(
    model,
    memory: MemoryStore | None = None,
    sink: EventSink | None = None,
    graph=None,
    checkpointer=None,
) -> FastAPI:
    app = FastAPI()
    app.state.model = model
    app.state.memory = memory or MemoryStore()
    app.state.sink = sink or EventSink(os.environ.get("CAKERDESK_GO_URL"))
    app.state.graph = graph or build_graph(checkpointer)
    app.state.tasks = {}

    def _start(body: RunIn, *, resume: bool) -> dict:
        if not body.run_id or not body.workspace_root or not body.goal:
            raise HTTPException(status_code=400, detail="missing run fields")
        if body.run_id not in CANCEL_FLAGS:
            CANCEL_FLAGS[body.run_id] = False

        async def _job() -> None:
            await asyncio.to_thread(
                execute_run,
                project_id=body.project_id,
                thread_id=body.thread_id,
                run_id=body.run_id,
                goal=body.goal,
                workspace_root=body.workspace_root,
                model=app.state.model,
                memory=app.state.memory,
                sink=app.state.sink,
                graph=app.state.graph,
                cancel_flags=CANCEL_FLAGS,
                resume=resume,
            )

        app.state.tasks[body.run_id] = asyncio.create_task(_job())
        return {"run_id": body.run_id, "status": "accepted"}

    @app.post("/internal/runs", status_code=202)
    async def start_run(body: RunIn):
        return _start(body, resume=False)

    @app.post("/internal/runs/{run_id}/resume", status_code=202)
    async def resume_run(run_id: str, body: RunIn):
        if body.run_id != run_id:
            raise HTTPException(status_code=400, detail="run_id mismatch")
        return _start(body, resume=True)

    @app.post("/internal/runs/{run_id}/cancel", status_code=202)
    async def cancel_run(run_id: str):
        CANCEL_FLAGS[run_id] = True
        return {"run_id": run_id, "status": "cancel_requested"}

    @app.get("/internal/projects/{project_id}/memory")
    async def list_memory(project_id: str):
        return app.state.memory.read_for_context(project_id)

    return app


def main() -> None:
    import uvicorn

    from cakerdesk.graph import open_checkpointer
    from cakerdesk.memory import open_memory
    from cakerdesk.model import env_model

    database_url = os.environ.get("CAKERDESK_DATABASE_URL")
    app = create_app(
        env_model(),
        memory=open_memory(database_url),
        checkpointer=open_checkpointer(database_url),
    )
    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8090")))


if __name__ == "__main__":
    main()
