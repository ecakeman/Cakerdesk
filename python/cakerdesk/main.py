from __future__ import annotations

import asyncio
import os

from fastapi import FastAPI
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


def create_app(model, memory: MemoryStore | None = None, sink: EventSink | None = None, graph=None) -> FastAPI:
    app = FastAPI()
    app.state.model = model
    app.state.memory = memory or MemoryStore(os.environ.get("CAKERDESK_MEMORY_PATH", ".data/project_memory.sqlite"))
    app.state.sink = sink or EventSink(os.environ.get("CAKERDESK_GO_URL"))
    app.state.graph = graph or build_graph()
    app.state.tasks = {}

    @app.post("/internal/runs", status_code=202)
    async def start_run(body: RunIn):
        if not body.run_id or not body.workspace_root or not body.goal:
            from fastapi import HTTPException

            raise HTTPException(status_code=400, detail="missing run fields")
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
            )

        app.state.tasks[body.run_id] = asyncio.create_task(_job())
        return {"run_id": body.run_id, "status": "accepted"}

    @app.post("/internal/runs/{run_id}/cancel", status_code=202)
    async def cancel_run(run_id: str):
        CANCEL_FLAGS[run_id] = True
        return {"run_id": run_id, "status": "cancel_requested"}

    return app


def main() -> None:
    import uvicorn

    app = create_app(_env_model())
    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8090")))


def _env_model():
    base = os.environ.get("OPENAI_BASE_URL") or os.environ.get("OPENAI_API_KEY")
    if not base:
        raise SystemExit("需要 OPENAI_API_KEY 或 OPENAI_BASE_URL 才能启动真实模型")
    from langchain_openai import ChatOpenAI

    llm = ChatOpenAI(
        model=os.environ.get("CAKERDESK_MODEL", "gpt-4o-mini"),
        api_key=os.environ.get("OPENAI_API_KEY"),
        base_url=os.environ.get("OPENAI_BASE_URL"),
    )

    class _Adapter:
        def invoke(self, messages, tools=None, *, purpose: str = "lead"):
            bound = llm
            if tools and purpose == "lead":
                bound = llm.bind_tools(_tool_schemas())
            return bound.invoke(messages)

    return _Adapter()


def _tool_schemas() -> list[dict]:
    return [
        {"type": "function", "function": {"name": name, "parameters": {"type": "object", "properties": {}}}}
        for name in (
            "list_dir",
            "read_file",
            "write_file",
            "set_step_status",
            "delegate_task",
            "submit_for_verification",
        )
    ]


if __name__ == "__main__":
    main()
