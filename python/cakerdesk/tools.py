from __future__ import annotations

import json
from pathlib import Path

from langchain_core.messages import AIMessage, ToolMessage

from cakerdesk.events import EventSink
from cakerdesk.plan import set_step_status
from cakerdesk.subagent import MAX_DELEGATES, run_subagent
from cakerdesk.workspace import WorkspaceError, list_dir, read_text, scan_artifacts, write_text

LEAD_TOOLS = [
    "list_dir",
    "read_file",
    "write_file",
    "set_step_status",
    "delegate_task",
    "submit_for_verification",
]


def tool_sig(name: str, args: dict) -> str:
    return name + ":" + json.dumps(args, ensure_ascii=False, sort_keys=True)


def execute_tool_calls(
    *,
    ai: AIMessage,
    state: dict,
    workspace_root: Path,
    model,
    sink: EventSink,
) -> dict:
    """执行除 submit 以外的工具。submit 只回一条 submitted，不产生业务副作用。"""
    messages = []
    plan = state.get("plan")
    artifacts = list(state.get("artifacts") or [])
    delegations = list(state.get("delegations") or [])
    guard = dict(state.get("guard") or {})
    run_id = guard.get("run_id") or ""
    contract = state.get("contract") or {}
    plan_changed = False

    for call in ai.tool_calls or []:
        name = call["name"]
        args = call.get("args") or {}
        call_id = call["id"]
        if name == "submit_for_verification":
            messages.append(ToolMessage(content="submitted", tool_call_id=call_id, id=f"submit-{call_id}"))
            continue
        sink.emit(run_id, "tool.started", {"tool": name})
        status = "completed"
        try:
            if name == "read_file":
                summary = read_text(workspace_root, args["path"])
                guard["executed"] = True
            elif name == "write_file":
                rel = write_text(workspace_root, args["path"], args.get("content") or "")
                artifacts = scan_artifacts(workspace_root)
                if rel not in artifacts:
                    artifacts.append(rel)
                summary = f"已写入 {rel}"
                guard["executed"] = True
            elif name == "list_dir":
                summary = list_dir(workspace_root, args["path"])
                guard["executed"] = True
            elif name == "set_step_status":
                plan = set_step_status(plan or {"steps": []}, args["step_id"], args["status"], args.get("note") or "")
                plan_changed = True
                summary = f"{args['step_id']} -> {args['status']}"
                guard["executed"] = True
            elif name == "delegate_task":
                guard["delegate_count"] = int(guard.get("delegate_count") or 0) + 1
                if guard["delegate_count"] > MAX_DELEGATES:
                    raise WorkspaceError("本 Run 的 Subagent 数量已达上限")
                sub_id = f"sub_{guard['delegate_count']}"
                brief = contract.get("goal") or ""
                result = run_subagent(
                    task=args.get("task") or "",
                    workspace_root=workspace_root,
                    contract_brief=brief,
                    model=model,
                    subagent_id=sub_id,
                    sink=sink,
                    run_id=run_id,
                )
                delegations.append(
                    {
                        "id": sub_id,
                        "task": args.get("task") or "",
                        "status": result["status"],
                        "summary": result["summary"][:200],
                    }
                )
                delegations = delegations[-20:]
                summary = result["summary"]
                status = result["status"]
                guard["executed"] = True
                artifacts = scan_artifacts(workspace_root)
            else:
                raise WorkspaceError(f"未知工具 {name}")
        except (WorkspaceError, ValueError, KeyError) as exc:
            status = "failed"
            summary = str(exc)
        sink.emit(run_id, "tool.completed", {"tool": name, "status": status, "summary": _short(summary)})
        messages.append(ToolMessage(content=summary[:8000], tool_call_id=call_id, id=f"tool-{call_id}", status="error" if status == "failed" else "success"))
        sig = tool_sig(name, args if isinstance(args, dict) else {})
        if sig == guard.get("last_tool_sig"):
            guard["tool_repeat"] = int(guard.get("tool_repeat") or 0) + 1
        else:
            guard["last_tool_sig"] = sig
            guard["tool_repeat"] = 1
        if guard["tool_repeat"] >= 3:
            guard["fail_reason"] = "repeated_tool_call"
            guard["fail_message"] = "相同工具被连续重复调用"

    update = {
        "messages": messages,
        "artifacts": artifacts,
        "delegations": delegations,
        "guard": guard,
    }
    if plan is not None:
        update["plan"] = plan
    if plan_changed and plan is not None:
        sink.emit(run_id, "plan.updated", {"plan": _public_plan(plan)})
    return update


def _short(text: str) -> str:
    line = " ".join(text.split())
    return line[:180]


def _public_plan(plan: dict) -> dict:
    return {
        "goal": plan.get("goal") or "",
        "steps": [{"id": step["id"], "title": step["title"], "status": step["status"]} for step in plan.get("steps") or []],
    }


def public_plan(plan: dict) -> dict:
    return _public_plan(plan)
