from __future__ import annotations

import os
import socket
import subprocess
import sys
import threading
import urllib.request
from pathlib import Path

import pytest
import uvicorn
from langchain_core.messages import AIMessage, HumanMessage

from cakerdesk.runtime.context import CONTEXT_MARKER
from cakerdesk.infra.events import EventSink
from cakerdesk.runtime.graph import build_graph, checkpoint_contains_context, execute_run
from cakerdesk.main import create_app
from cakerdesk.runtime.memory import MemoryStore
from cakerdesk.runtime.middleware import maybe_summarize
from cakerdesk.runtime.plan import block_failed_steps, set_step_status
from cakerdesk.runtime.state import fresh_guard
from tests.demo_model import DemoModel

NOTES = "请根据 sales.csv 写报告。必须包含数据摘要、异常点、结论。异常点不要和全文写作混在同一步。\n"
CSV = "week,units\n1,100\n2,98\n3,110\n4,12\n5,105\n"
GOAL = "根据 work/notes.txt 和 work/sales.csv，完成 artifacts/report.md。"


def _workspace(tmp_path: Path) -> Path:
    root = tmp_path / "threads" / "t1"
    (root / "uploads").mkdir(parents=True)
    (root / "work").mkdir()
    (root / "artifacts").mkdir()
    (root / "work" / "notes.txt").write_text(NOTES, encoding="utf-8")
    (root / "work" / "sales.csv").write_text(CSV, encoding="utf-8")
    return root


def _types(sink: EventSink) -> list[str]:
    return [event["type"] for event in sink.events]


def test_set_step_status_cannot_block_completed():
    plan = {"steps": [{"id": "s4", "title": "撰写 report.md", "status": "completed", "note": ""}]}
    with pytest.raises(ValueError):
        set_step_status(plan, "s4", "blocked")


def test_verify_fail_blocks_completed_step():
    plan = {
        "steps": [
            {"id": "s1", "title": "阅读 notes.txt", "status": "completed", "note": ""},
            {"id": "s4", "title": "撰写 report.md", "status": "completed", "note": ""},
        ]
    }
    findings = {
        "passed": False,
        "items": [{"criterion": "结论", "status": "failed", "evidence": "artifacts/report.md 存在且非空，正文没有「结论」", "reason": ""}],
    }
    updated = block_failed_steps(plan, findings)
    assert updated["steps"][0]["status"] == "completed"
    assert updated["steps"][1]["status"] == "blocked"


def test_demo_run_closes_the_loop(tmp_path: Path):
    root = _workspace(tmp_path)
    model = DemoModel()
    memory = MemoryStore()
    sink = EventSink()
    graph = build_graph()
    result = execute_run(
        project_id="p1",
        thread_id="t1",
        run_id="run-a",
        goal=GOAL,
        workspace_root=str(root),
        model=model,
        memory=memory,
        sink=sink,
        graph=graph,
        skill_root=Path(__file__).resolve().parents[1] / "skills",
    )
    report = (root / "artifacts" / "report.md").read_text(encoding="utf-8")
    assert "结论" in report
    assert "数据摘要" in report
    snapshot = graph.get_state({"configurable": {"thread_id": "t1"}})
    assert not checkpoint_contains_context(snapshot.values["messages"])
    joined = "\n".join(
        message.content if isinstance(message.content, str) else str(message.content)
        for message in snapshot.values["messages"]
    )
    assert "内部分析不该进父级" not in joined
    assert "第 4 周 units=12" in joined
    assert "请根据 sales.csv" in joined

    lead_calls = [call for call in model.calls if call["purpose"] == "lead"]
    first = "\n".join(message.content if isinstance(message.content, str) else "" for message in lead_calls[0]["messages"])
    assert "撰写 report.md" in first
    assert "（无）" in first
    assert CONTEXT_MARKER in first
    later = "\n".join(message.content if isinstance(message.content, str) else "" for message in lead_calls[5]["messages"])
    assert "补写结论" in later
    assert "正文没有" in later
    assert "请根据 sales.csv" in later

    s4_status = []
    for event in sink.events:
        if event["type"] != "plan.updated":
            continue
        for step in event["payload"]["plan"]["steps"]:
            if step["id"] == "s4":
                s4_status.append(step["status"])
    assert "completed" in s4_status
    assert "blocked" in s4_status
    assert s4_status.index("blocked") > s4_status.index("completed")
    assert "pending" in s4_status[s4_status.index("blocked") + 1 :]

    types = _types(sink)
    assert types.index("model.message") < types.index("verification.completed")
    assert types.count("verification.completed") == 2
    assert sink.events[[i for i, event in enumerate(sink.events) if event["type"] == "verification.completed"][0]]["payload"]["passed"] is False
    assert sink.events[[i for i, event in enumerate(sink.events) if event["type"] == "verification.completed"][1]]["payload"]["passed"] is True
    assert "replan.started" in types
    assert result["plan"]["steps"][0]["title"] == "阅读 notes.txt"
    assert any(step["id"] == "s5" for step in result["plan"]["steps"])

    rows = memory.read_for_context("p1")
    assert any(row["kind"] == "fact" and "三个部分" in row["content"] for row in rows)
    assert any(row["kind"] == "lesson" and "遗漏了结论" in row["content"] for row in rows)
    reflect_blob = "\n".join(
        message.content if isinstance(message.content, str) else ""
        for call in model.calls if call["purpose"] == "reflect"
        for message in call["messages"]
    )
    assert "正文没有" in reflect_blob

    model.run_b = True
    root_b = tmp_path / "threads" / "t2"
    (root_b / "work").mkdir(parents=True)
    (root_b / "uploads").mkdir()
    (root_b / "artifacts").mkdir()
    (root_b / "artifacts" / "report.md").write_text("## 数据摘要\n\n## 异常点\n\n## 结论\n沿用。\n", encoding="utf-8")
    sink_b = EventSink()
    execute_run(
        project_id="p1",
        thread_id="t2",
        run_id="run-b",
        goal="再写一份简短周报说明，沿用上次的报告要求。",
        workspace_root=str(root_b),
        model=model,
        memory=memory,
        sink=sink_b,
        graph=build_graph(),
        skill_root=Path(__file__).resolve().parents[1] / "skills",
    )
    b_first = next(call for call in model.calls if call["purpose"] == "lead" and "沿用上次" in _blob(call))
    assert "遗漏了结论" in _blob(b_first)
    memory.close()


def _blob(call: dict) -> str:
    return "\n".join(message.content if isinstance(message.content, str) else "" for message in call["messages"])


def test_summary_call_skips_context(tmp_path: Path):
    model = DemoModel()
    messages = []
    for index in range(10):
        messages.append(HumanMessage(content=f"旧对话 {index} " + ("甲" * 40), id=f"h{index}"))
        messages.append(AIMessage(content=f"旧回答 {index}", id=f"a{index}"))
    state = {"messages": messages, "summary": "", "guard": fresh_guard("r")}
    update = maybe_summarize(state, model, message_threshold=6, keep_recent=8)
    assert update is not None
    assert update["summary"]
    assert update["messages"]
    summary_call = model.calls[-1]
    assert summary_call["purpose"] == "summary"
    assert CONTEXT_MARKER not in _blob(summary_call)
    assert "压缩" in _blob(summary_call)


def test_init_failure_does_not_write_memory(tmp_path: Path):
    class BadContract:
        def invoke(self, messages, tools=None, *, purpose: str = "lead"):
            return AIMessage(content="无法生成契约")

    memory = MemoryStore()
    sink = EventSink()
    root = _workspace(tmp_path)
    execute_run(
        project_id="p1",
        thread_id="t-bad",
        run_id="run-bad",
        goal=GOAL,
        workspace_root=str(root),
        model=BadContract(),
        memory=memory,
        sink=sink,
        graph=build_graph(),
    )
    assert memory.read_for_context("p1") == []
    assert sink.events[-1]["type"] == "run.failed"
    assert sink.events[-1]["payload"]["reason"] == "invalid_contract"
    assert "memory.written" not in _types(sink)
    memory.close()


def test_model_exception_emits_run_failed(tmp_path: Path):
    class Rejecting(DemoModel):
        def structured(self, messages, schema, *, purpose: str):
            raise RuntimeError("provider rejected request")

        def invoke(self, messages, tools=None, *, purpose: str = "lead"):
            raise RuntimeError("provider rejected request")

    sink = EventSink()
    execute_run(
        project_id="p-fail",
        thread_id="t-fail",
        run_id="run-fail",
        goal=GOAL,
        workspace_root=str(_workspace(tmp_path)),
        model=Rejecting(),
        memory=MemoryStore(),
        sink=sink,
        graph=build_graph(),
    )
    failed = [event for event in sink.events if event["type"] == "run.failed"]
    assert failed
    assert failed[-1]["payload"]["reason"] == "model_error"
    assert "verification.completed" not in _types(sink)


def test_plain_text_does_not_verify(tmp_path: Path):
    class TextThenStop(DemoModel):
        def invoke(self, messages, tools=None, *, purpose: str = "lead"):
            if purpose == "lead":
                self.lead_n += 1
                self.calls.append({"purpose": purpose, "messages": messages})
                return AIMessage(content="还在想")
            return super().invoke(messages, tools, purpose=purpose)

    memory = MemoryStore()
    sink = EventSink()
    model = TextThenStop()
    execute_run(
        project_id="p-text",
        thread_id="t-text",
        run_id="run-text",
        goal=GOAL,
        workspace_root=str(_workspace(tmp_path)),
        model=model,
        memory=memory,
        sink=sink,
        graph=build_graph(),
    )
    assert "verification.completed" not in _types(sink)
    assert sink.events[-1]["payload"]["reason"] == "model_no_progress"
    assert sink.events[-1]["payload"]["last_output"] == "还在想"
    assert memory.read_for_context("p-text") == []
    memory.close()


def test_event_seq_restarts_for_each_run():
    sink = EventSink()
    first = sink.emit("run-a", "run.started", {})
    second = sink.emit("run-b", "run.started", {})
    third = sink.emit("run-a", "tool.started", {"tool": "read_file"})
    assert first["seq"] == 1
    assert second["seq"] == 1
    assert third["seq"] == 2


def test_resume_continues_without_resetting_plan(tmp_path: Path):
    root = _workspace(tmp_path)
    model = DemoModel()
    graph = build_graph()
    common = dict(
        project_id="p1",
        thread_id="t-resume",
        run_id="run-resume",
        goal=GOAL,
        workspace_root=str(root),
        model=model,
        memory=MemoryStore(),
        graph=graph,
    )
    partial = execute_run(sink=EventSink(), interrupt_after=["ensure_plan"], **common)
    assert partial["plan"]["steps"][3]["title"] == "撰写 report.md"
    humans = [message for message in partial["messages"] if isinstance(message, HumanMessage)]
    assert len(humans) == 1
    sink = EventSink()
    result = execute_run(sink=sink, resume=True, **common)
    report = (root / "artifacts" / "report.md").read_text(encoding="utf-8")
    assert "结论" in report
    assert result["plan"]["steps"][0]["title"] == "阅读 notes.txt"
    snapshot = graph.get_state({"configurable": {"thread_id": "t-resume"}})
    humans = [message for message in snapshot.values["messages"] if isinstance(message, HumanMessage)]
    assert len(humans) == 1


def test_resume_sees_persisted_cancel(tmp_path: Path):
    root = _workspace(tmp_path)
    model = DemoModel()
    graph = build_graph()
    common = dict(
        project_id="p1",
        thread_id="t-cancel",
        run_id="run-cancel",
        goal=GOAL,
        workspace_root=str(root),
        model=model,
        memory=MemoryStore(),
        graph=graph,
    )
    execute_run(sink=EventSink(), interrupt_after=["ensure_plan"], **common)
    sink = EventSink()
    execute_run(sink=sink, resume=True, status_lookup=lambda _run_id: "cancel_requested", **common)
    assert sink.events[-1]["type"] == "run.cancelled"
    assert model.lead_n == 0
    snapshot = graph.get_state({"configurable": {"thread_id": "t-cancel"}})
    assert snapshot.values["plan"]["steps"][0]["id"] == "s1"


@pytest.mark.skipif(not os.environ.get("CAKERDESK_DATABASE_URL"), reason="需要 CAKERDESK_DATABASE_URL，不退回 SQLite")
def test_postgres_resume_keeps_checkpoint_and_memory(tmp_path: Path):
    from cakerdesk.runtime.graph import open_checkpointer
    from cakerdesk.runtime.memory import open_memory

    url = os.environ["CAKERDESK_DATABASE_URL"]
    checkpointer = open_checkpointer(url)
    memory = open_memory(url)
    root = _workspace(tmp_path)
    model = DemoModel()
    thread_id = "pg-" + next(tempfile_token())
    graph = build_graph(checkpointer)
    common = dict(
        project_id="pg-project",
        thread_id=thread_id,
        run_id="pg-run",
        goal=GOAL,
        workspace_root=str(root),
        model=model,
        memory=memory,
        graph=graph,
    )
    partial = execute_run(sink=EventSink(), interrupt_after=["ensure_plan"], **common)
    assert partial["plan"]["steps"][0]["id"] == "s1"
    execute_run(sink=EventSink(), resume=True, **common)
    assert "结论" in (root / "artifacts" / "report.md").read_text(encoding="utf-8")
    again = build_graph(checkpointer)
    snapshot = again.get_state({"configurable": {"thread_id": thread_id}})
    assert snapshot.values["plan"]["steps"][0]["title"] == "阅读 notes.txt"
    assert any(row["kind"] == "lesson" for row in memory.read_for_context("pg-project"))


def tempfile_token():
    import uuid

    while True:
        yield uuid.uuid4().hex[:8]


@pytest.mark.skipif(not os.environ.get("CAKERDESK_DATABASE_URL"), reason="需要 CAKERDESK_DATABASE_URL，不退回 SQLite")
def test_resume_after_model_failure(tmp_path: Path):
    root = _workspace(tmp_path)
    env = os.environ.copy()
    env.update({
        "WS": str(root),
        "THREAD": "fail-" + next(tempfile_token()),
        "RUN": "run-model-fail",
        "PROJECT": "fail-" + next(tempfile_token()),
    })
    script = r"""
import os
from pathlib import Path
from cakerdesk.infra.events import EventSink
from cakerdesk.runtime.graph import build_graph, execute_run, open_checkpointer
from cakerdesk.runtime.memory import open_memory
from tests.demo_model import DemoModel

url = os.environ["CAKERDESK_DATABASE_URL"]
root = Path(os.environ["WS"])
graph = build_graph(open_checkpointer(url))
common = dict(
    project_id=os.environ["PROJECT"],
    thread_id=os.environ["THREAD"],
    run_id=os.environ["RUN"],
    goal="根据 work/notes.txt 和 work/sales.csv，完成 artifacts/report.md。",
    workspace_root=str(root),
    memory=open_memory(url),
    sink=EventSink(),
    graph=graph,
)
if os.environ["PHASE"] == "start":
    class Rejecting(DemoModel):
        def structured(self, messages, schema, *, purpose: str):
            raise RuntimeError("provider rejected request")
    execute_run(model=Rejecting(), **common)
    failed = [event for event in common["sink"].events if event["type"] == "run.failed"]
    assert failed and failed[-1]["payload"]["reason"] == "model_error"
    snapshot = graph.get_state({"configurable": {"thread_id": os.environ["THREAD"]}})
    assert "ensure_contract" in snapshot.next
else:
    execute_run(model=DemoModel(), resume=True, **common)
    assert "结论" in (root / "artifacts" / "report.md").read_text(encoding="utf-8")
"""
    python_root = Path(__file__).resolve().parents[1]
    env["PHASE"] = "start"
    first = subprocess.run([sys.executable, "-c", script], cwd=python_root, env=env, check=False, capture_output=True, text=True)
    assert first.returncode == 0, first.stderr
    env["PHASE"] = "resume"
    second = subprocess.run([sys.executable, "-c", script], cwd=python_root, env=env, check=False, capture_output=True, text=True)
    assert second.returncode == 0, second.stderr


@pytest.mark.skipif(not os.environ.get("CAKERDESK_DATABASE_URL"), reason="需要 CAKERDESK_DATABASE_URL，不退回 SQLite")
def test_process_exit_then_resume(tmp_path: Path):
    root = _workspace(tmp_path)
    env = os.environ.copy()
    env.update({
        "WS": str(root),
        "THREAD": "exit-" + next(tempfile_token()),
        "RUN": "run-exit",
        "PROJECT": "exit-" + next(tempfile_token()),
    })
    script = r"""
import os
from pathlib import Path
from langchain_core.messages import HumanMessage
from cakerdesk.infra.events import EventSink
from cakerdesk.runtime.graph import build_graph, execute_run, open_checkpointer
from cakerdesk.runtime.memory import open_memory
from tests.demo_model import DemoModel

url = os.environ["CAKERDESK_DATABASE_URL"]
root = Path(os.environ["WS"])
graph = build_graph(open_checkpointer(url))
common = dict(
    project_id=os.environ["PROJECT"],
    thread_id=os.environ["THREAD"],
    run_id=os.environ["RUN"],
    goal="根据 work/notes.txt 和 work/sales.csv，完成 artifacts/report.md。",
    workspace_root=str(root),
    model=DemoModel(),
    memory=open_memory(url),
    sink=EventSink(),
    graph=graph,
)
if os.environ["PHASE"] == "start":
    execute_run(interrupt_after=["ensure_plan"], **common)
else:
    execute_run(resume=True, **common)
    assert "结论" in (root / "artifacts" / "report.md").read_text(encoding="utf-8")
    snapshot = graph.get_state({"configurable": {"thread_id": os.environ["THREAD"]}})
    humans = [message for message in snapshot.values["messages"] if isinstance(message, HumanMessage)]
    assert len(humans) == 1
    assert snapshot.values["plan"]["steps"][0]["title"] == "阅读 notes.txt"
    assert snapshot.values.get("findings")
"""
    python_root = Path(__file__).resolve().parents[1]
    env["PHASE"] = "start"
    first = subprocess.run([sys.executable, "-c", script], cwd=python_root, env=env, check=False, capture_output=True, text=True)
    assert first.returncode == 0, first.stderr
    env["PHASE"] = "resume"
    second = subprocess.run([sys.executable, "-c", script], cwd=python_root, env=env, check=False, capture_output=True, text=True)
    assert second.returncode == 0, second.stderr


def test_start_returns_202_before_run_finishes(tmp_path: Path):
    started = threading.Event()
    release = threading.Event()

    class Blocking:
        def structured(self, messages, schema, *, purpose: str):
            started.set()
            release.wait(5)
            raise ValueError("stop")

        def invoke(self, messages, tools=None, *, purpose: str = "lead"):
            started.set()
            release.wait(5)
            return AIMessage(content="not-json")

    app = create_app(Blocking(), memory=MemoryStore(), sink=EventSink(), graph=build_graph())
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    thread = threading.Thread(
        target=uvicorn.run,
        kwargs={"app": app, "host": "127.0.0.1", "port": port, "log_level": "error"},
        daemon=True,
    )
    thread.start()
    for _ in range(50):
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{port}/openapi.json", timeout=0.2)
            break
        except Exception:
            release.wait(0.05)
    import json

    root = _workspace(tmp_path)
    body = json.dumps({
        "project_id": "p",
        "thread_id": "t",
        "run_id": "run-http",
        "goal": GOAL,
        "workspace_root": str(root),
    }).encode()
    request = urllib.request.Request(
        f"http://127.0.0.1:{port}/internal/runs",
        data=body,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=2) as response:
        payload = json.loads(response.read().decode())
        status = response.status
    assert status == 202
    assert payload == {"run_id": "run-http", "status": "accepted"}
    assert started.wait(2)
    assert not release.is_set()
    release.set()
