from __future__ import annotations

import json
import os
import uuid
from pathlib import Path
from typing import Any

from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langchain_core.runnables import RunnableConfig
from langgraph.checkpoint.memory import MemorySaver
from langgraph.graph import END, START, StateGraph
from pydantic import BaseModel, ValidationError

from cakerdesk.context import CONTEXT_MARKER, ContextManager
from cakerdesk.events import EventSink, fetch_run_status, publish
from cakerdesk.lead import decide_after_model, decide_after_tools
from cakerdesk.middleware import maybe_summarize
from cakerdesk.plan import block_failed_steps, normalize_contract, normalize_plan, validate_diff
from cakerdesk.schemas import ContractOut, PlanOut, ReflectionOut
from cakerdesk.state import AgentState, estimate_tokens, fresh_guard
from cakerdesk.tooldefs import LEAD_TOOLS
from cakerdesk.tools import execute_tool_calls, public_plan
from cakerdesk.verify import verify
from cakerdesk.workspace import ensure_layout, scan_artifacts


def load_skill_names(root: Path) -> list[str]:
    if not root.exists():
        return []
    names = []
    for path in sorted(root.glob("*/SKILL.md")):
        line = next((item.strip() for item in path.read_text(encoding="utf-8").splitlines() if item.strip()), "")
        names.append(line.lstrip("#").strip() or path.parent.name)
    return names


def build_graph(checkpointer=None):
    graph = StateGraph(AgentState)
    graph.add_node("bootstrap", bootstrap)
    graph.add_node("ensure_contract", ensure_contract)
    graph.add_node("ensure_plan", ensure_plan)
    graph.add_node("lead_model", lead_model)
    graph.add_node("lead_tools", lead_tools)
    graph.add_node("verify", verify_node)
    graph.add_node("replan", replan_node)
    graph.add_node("reflect", reflect_node)
    graph.add_node("emit_completed", emit_completed)
    graph.add_node("emit_failed", emit_failed)
    graph.add_node("emit_cancelled", emit_cancelled)

    graph.add_edge(START, "bootstrap")
    graph.add_edge("bootstrap", "ensure_contract")
    graph.add_conditional_edges("ensure_contract", _route, {"ensure_plan": "ensure_plan", "emit_failed": "emit_failed"})
    graph.add_conditional_edges("ensure_plan", _route, {"lead_model": "lead_model", "emit_failed": "emit_failed"})
    graph.add_conditional_edges(
        "lead_model",
        _route,
        {
            "lead_model": "lead_model",
            "tools": "lead_tools",
            "runtime_fail": "reflect",
            "cancel": "emit_cancelled",
        },
    )
    graph.add_conditional_edges(
        "lead_tools",
        _route,
        {"lead_model": "lead_model", "verify": "verify", "runtime_fail": "reflect", "cancel": "emit_cancelled"},
    )
    graph.add_conditional_edges(
        "verify",
        _route,
        {"reflect": "reflect", "replan": "replan", "runtime_fail": "reflect", "cancel": "emit_cancelled"},
    )
    graph.add_conditional_edges(
        "replan",
        _route,
        {"lead_model": "lead_model", "runtime_fail": "reflect", "cancel": "emit_cancelled"},
    )
    graph.add_conditional_edges("reflect", _route, {"emit_completed": "emit_completed", "emit_failed": "emit_failed"})
    graph.add_edge("emit_completed", END)
    graph.add_edge("emit_failed", END)
    graph.add_edge("emit_cancelled", END)
    return graph.compile(checkpointer=checkpointer or MemorySaver())


def execute_run(
    *,
    project_id: str,
    thread_id: str,
    run_id: str,
    goal: str,
    workspace_root: str,
    model,
    memory,
    sink: EventSink,
    checkpointer=None,
    cancel_flags: dict | None = None,
    skill_root: Path | None = None,
    summarize_message_count: int = 24,
    graph=None,
    resume: bool = False,
    interrupt_after: list[str] | None = None,
    status_lookup=None,
) -> dict:
    root = Path(workspace_root)
    ensure_layout(root)
    compiled = graph or build_graph(checkpointer)
    config = {
        "configurable": {
            "thread_id": thread_id,
            "project_id": project_id,
            "run_id": run_id,
            "goal": goal,
            "workspace_root": str(root),
            "model": model,
            "memory": memory,
            "sink": sink,
            "context": ContextManager(),
            "cancel_flags": cancel_flags or {},
            "skill_names": load_skill_names(skill_root or Path("skills")),
            "summarize_message_count": summarize_message_count,
            "go_url": sink.post_url,
            "status_lookup": status_lookup,
        },
        "recursion_limit": 80,
    }
    incoming: dict | None = {}
    if resume:
        snapshot = compiled.get_state(config)
        if not snapshot.next:
            raise RuntimeError("没有可恢复的检查点")
        if _cancelled(config):
            sink.emit(run_id, "run.cancelled", {"reason": "user_cancelled"})
            return snapshot.values
        incoming = None
    final = None
    for item in compiled.stream(
        incoming,
        config,
        stream_mode=["values", "custom"],
        durability="sync",
        interrupt_after=interrupt_after,
    ):
        mode, chunk = item
        if mode == "custom":
            sink.emit(chunk["run_id"], chunk["type"], chunk["payload"])
        else:
            final = chunk
    if final is None:
        final = compiled.get_state(config).values or {}
    return final


def open_checkpointer(database_url: str | None):
    if not database_url:
        raise RuntimeError("CAKERDESK_DATABASE_URL 未设置，不使用内存 Checkpoint 作为产品存储")
    os.environ["LANGGRAPH_STRICT_MSGPACK"] = "true"
    from langgraph.checkpoint.postgres import PostgresSaver

    context = PostgresSaver.from_conn_string(database_url)
    saver = context.__enter__()
    saver.setup()
    saver._cakerdesk_context = context
    return saver


def _cfg(config: RunnableConfig) -> dict:
    return config["configurable"]


def _route(state: AgentState) -> str:
    return state.get("route") or "emit_failed"


def _last_ai(state: AgentState) -> AIMessage:
    for message in reversed(state.get("messages") or []):
        if isinstance(message, AIMessage):
            return message
    raise RuntimeError("没有模型消息")


def _cancelled(config: RunnableConfig) -> bool:
    cfg = _cfg(config)
    if cfg["cancel_flags"].get(cfg["run_id"]):
        return True
    lookup = cfg.get("status_lookup")
    if lookup is not None:
        return lookup(cfg["run_id"]) == "cancel_requested"
    return fetch_run_status(cfg.get("go_url"), cfg["run_id"]) == "cancel_requested"


def _prompt(purpose: str) -> str:
    path = Path(__file__).resolve().parent / "prompts" / f"{purpose}.md"
    if path.exists():
        return path.read_text(encoding="utf-8").strip()
    from cakerdesk.context import RULES

    return RULES


def _messages_for(state: AgentState, config: RunnableConfig, purpose: str) -> list:
    cfg = _cfg(config)
    memory_rows = cfg["memory"].read_for_context(cfg["project_id"])
    return cfg["context"].build(state, memory_rows, skill_names=cfg["skill_names"], rules=_prompt(purpose))


def _as_ai(response, purpose: str) -> AIMessage:
    if not isinstance(response, AIMessage):
        response = AIMessage(content=str(response))
    response.id = response.id or f"{purpose}-{uuid.uuid4().hex[:8]}"
    return response


def _call_model(state: AgentState, config: RunnableConfig, purpose: str, tools: list | None):
    response = _as_ai(_cfg(config)["model"].invoke(_messages_for(state, config, purpose), tools=tools, purpose=purpose), purpose)
    _charge(state, response)
    return response


def _structured(state: AgentState, config: RunnableConfig, purpose: str, schema, extra: list | None = None):
    messages = _messages_for(state, config, purpose)
    if extra:
        messages = [*messages, *extra]
    try:
        parsed = _cfg(config)["model"].structured(messages, schema, purpose=purpose)
        data = parsed if isinstance(parsed, BaseModel) else schema.model_validate(parsed)
    except (AttributeError, ValidationError, ValueError, TypeError) as exc:
        raise ValueError(str(exc)) from exc
    response = AIMessage(content=data.model_dump_json(), id=f"{purpose}-{uuid.uuid4().hex[:8]}")
    _charge(state, response)
    return response, data.model_dump()


def _charge(state: AgentState, response: AIMessage) -> None:
    usage = int((getattr(response, "usage_metadata", None) or {}).get("total_tokens") or 1)
    guard = state["guard"]
    guard["total_tokens"] = int(guard.get("total_tokens") or 0) + usage
    if guard["total_tokens"] >= 200000:
        guard["fail_reason"] = "token_budget_exceeded"
        guard["fail_message"] = "token 超过预算"


def bootstrap(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    root = Path(cfg["workspace_root"])
    ensure_layout(root)
    guard = fresh_guard(cfg["run_id"])
    publish(config, "run.started", {"goal": cfg["goal"]})
    return {
        "messages": [HumanMessage(content=cfg["goal"], id=f"goal-{cfg['run_id']}")],
        "findings": None,
        "attempts": [],
        "guard": guard,
        "artifacts": scan_artifacts(root),
        "summary": state.get("summary") or "",
        "delegations": state.get("delegations") or [],
        "contract": None,
        "plan": None,
        "route": "ensure_plan",
    }


def ensure_contract(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    guard = dict(state["guard"])
    state = {**state, "guard": guard}
    response = None
    try:
        response, raw = _structured(state, config, "contract", ContractOut)
        contract = normalize_contract(raw, cfg["goal"])
    except ValueError as exc:
        guard["fail_reason"] = "invalid_contract"
        guard["fail_message"] = str(exc)
        return {"messages": [response] if response else [], "guard": guard, "route": "emit_failed"}
    return {"messages": [response], "contract": contract, "guard": guard, "route": "ensure_plan"}


def ensure_plan(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    guard = dict(state["guard"])
    state = {**state, "guard": guard}
    response = None
    try:
        response, raw = _structured(state, config, "plan", PlanOut)
        plan = normalize_plan(raw, version=1)
        plan["diff"] = None
    except ValueError as exc:
        guard["fail_reason"] = "invalid_plan"
        guard["fail_message"] = str(exc)
        return {"messages": [response] if response else [], "guard": guard, "route": "emit_failed"}
    publish(config, "plan.updated", {"plan": public_plan(plan)})
    return {"messages": [response], "plan": plan, "guard": guard, "route": "lead_model"}


def lead_model(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    if _cancelled(config):
        return {"route": "cancel", "guard": state["guard"]}
    guard = dict(state["guard"])
    working = {**state, "guard": guard}
    summary_update = maybe_summarize(
        working,
        cfg["model"],
        message_threshold=int(cfg["summarize_message_count"]),
        keep_recent=8,
    )
    extra_messages: list = []
    if summary_update:
        working["summary"] = summary_update["summary"]
        working["guard"] = summary_update["guard"]
        guard = working["guard"]
        extra_messages.extend(summary_update["messages"])
        removed = {item.id for item in summary_update["messages"]}
        working["messages"] = [item for item in working["messages"] if getattr(item, "id", None) not in removed]
    response = _call_model(working, config, "lead", LEAD_TOOLS)
    content = _text(response)
    if content and not response.tool_calls:
        publish(config, "model.message", {"role": "assistant", "content": content})
        guard["plain_text_streak"] = int(guard.get("plain_text_streak") or 0) + 1
    elif response.tool_calls:
        guard["plain_text_streak"] = 0
    route = decide_after_model(response, guard)
    if route == "runtime_fail":
        guard["reflect_mode"] = "lesson"
    update: dict[str, Any] = {"messages": [*extra_messages, response], "guard": guard, "route": route}
    if summary_update:
        update["summary"] = summary_update["summary"]
    return update


def lead_tools(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    if _cancelled(config):
        return {"route": "cancel", "guard": state["guard"]}
    ai = _last_ai(state)
    update = execute_tool_calls(
        ai=ai,
        state=state,
        workspace_root=Path(cfg["workspace_root"]),
        model=cfg["model"],
        emit=lambda event_type, payload: publish(config, event_type, payload),
    )
    guard = update["guard"]
    route = decide_after_tools(ai, guard)
    if route == "runtime_fail":
        guard["reflect_mode"] = "lesson"
    update["route"] = route
    return update


def verify_node(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    if _cancelled(config):
        return {"route": "cancel", "guard": state["guard"]}
    findings = verify(state.get("contract") or {}, Path(cfg["workspace_root"]), state.get("messages"))
    attempts = list(state.get("attempts") or [])
    attempt_no = 1 + sum(1 for item in attempts if item.get("kind") == "verification")
    reason = "；".join(item["evidence"] for item in findings["items"] if item["status"] == "failed") or "通过"
    attempts.append(
        {
            "kind": "verification",
            "attempt": attempt_no,
            "passed": bool(findings["passed"]),
            "reason": reason,
            "plan_version": (state.get("plan") or {}).get("version"),
        }
    )
    plan = state.get("plan")
    if not findings["passed"] and plan:
        blocked = block_failed_steps(plan, findings)
        if blocked != plan:
            publish(config, "plan.updated", {"plan": public_plan(blocked)})
        plan = blocked
    guard = dict(state["guard"])
    guard["executed"] = True
    publish(
        config,
        "verification.completed",
        {"passed": findings["passed"], "findings": findings["items"]},
    )
    if findings["passed"]:
        route = "reflect"
        guard["reflect_mode"] = "full"
    else:
        guard["replan_count"] = int(guard.get("replan_count") or 0) + 1
        if guard["replan_count"] >= 3:
            guard["fail_reason"] = "max_replans_exceeded"
            guard["fail_message"] = "任务在多次重新规划后仍未完成"
            guard["reflect_mode"] = "lesson"
            route = "runtime_fail"
        else:
            route = "replan"
    return {"findings": findings, "attempts": attempts, "plan": plan, "guard": guard, "route": route}


def replan_node(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    if _cancelled(config):
        return {"route": "cancel", "guard": state["guard"]}
    publish(config, "replan.started", {"reason": "verification_failed"})
    guard = dict(state["guard"])
    working = {**state, "guard": guard}
    response = None
    plan = None
    error = ""
    for _ in range(2):
        response = None
        try:
            response, raw = _structured(working, config, "replan", PlanOut)
            plan = normalize_plan(raw, version=int(state["plan"]["version"]) + 1)
            validate_diff(state["plan"], plan)
            error = ""
            break
        except ValueError as exc:
            error = str(exc)
            if response is not None:
                working = {**working, "messages": [*(working.get("messages") or []), response]}
    if error or plan is None:
        guard["fail_reason"] = "invalid_replan"
        guard["fail_message"] = error or "重规划失败"
        guard["reflect_mode"] = "lesson"
        return {"messages": [response] if response else [], "guard": guard, "route": "runtime_fail"}
    attempts = list(state.get("attempts") or [])
    attempts.append(
        {
            "kind": "replan",
            "attempt": guard["replan_count"],
            "passed": None,
            "reason": "补写未通过验证的交付",
            "plan_version": plan["version"],
        }
    )
    publish(config, "plan.updated", {"plan": public_plan(plan)})
    return {"messages": [response], "plan": plan, "attempts": attempts, "guard": guard, "route": "lead_model"}


def reflect_node(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    guard = dict(state["guard"])
    if guard.get("fail_reason") in {"invalid_contract", "invalid_plan"} or not guard.get("executed"):
        if guard.get("fail_reason"):
            return {"guard": guard, "route": "emit_failed"}
    mode = guard.get("reflect_mode") or "full"
    extra = [SystemMessage(content="Attempts\n" + json.dumps(state.get("attempts") or [], ensure_ascii=False))]
    try:
        response, raw = _structured({**state, "guard": guard}, config, "reflect", ReflectionOut, extra=extra)
        raw_items = raw.get("items") or []
    except ValueError:
        response = AIMessage(content="", id=f"reflect-{uuid.uuid4().hex[:8]}")
        raw_items = []
    items = []
    for item in raw_items:
        kind = item.get("kind")
        if mode == "lesson" and kind != "lesson":
            continue
        if kind in {"fact", "lesson"} and str(item.get("content") or "").strip():
            items.append({"kind": kind, "content": str(item["content"]).strip()})
    written = cfg["memory"].write(cfg["project_id"], cfg["run_id"], items[:5])
    for row in written:
        publish(
            config,
            "memory.written",
            {"kind": row["kind"], "summary": row["content"][:120]},
        )
    route = "emit_failed" if guard.get("fail_reason") else "emit_completed"
    return {"messages": [response], "guard": guard, "route": route}


def emit_completed(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    publish(
        config,
        "run.completed",
        {"summary": "任务完成", "artifacts": list(state.get("artifacts") or [])},
    )
    return {"route": "done"}


def emit_failed(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    guard = state.get("guard") or {}
    publish(
        config,
        "run.failed",
        {
            "reason": guard.get("fail_reason") or "failed",
            "message": guard.get("fail_message") or "运行失败",
        },
    )
    return {"route": "done"}


def emit_cancelled(state: AgentState, config: RunnableConfig) -> dict:
    cfg = _cfg(config)
    publish(config, "run.cancelled", {"reason": "user_cancelled"})
    return {"route": "done"}


def _text(message) -> str:
    content = getattr(message, "content", message)
    if not isinstance(content, str):
        return str(content)
    return content


def checkpoint_contains_context(messages: list) -> bool:
    for message in messages:
        if CONTEXT_MARKER in _text(message):
            return True
    return False


def message_tokens(messages: list) -> int:
    return estimate_tokens("\n".join(_text(message) for message in messages))
