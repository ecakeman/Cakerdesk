from __future__ import annotations

from pathlib import Path

from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from cakerdesk.events import EventSink
from cakerdesk.workspace import WorkspaceError, list_dir, read_text, write_text

MAX_DELEGATES = 6
SUBAGENT_TURN_LIMIT = 8


def run_subagent(
    *,
    task: str,
    workspace_root: Path,
    contract_brief: str,
    model,
    subagent_id: str,
    sink: EventSink,
    run_id: str,
) -> dict:
    """独立消息列表。返回时这份列表被丢掉，不写入父 checkpoint。"""
    sink.emit(run_id, "subagent.started", {"subagent_id": subagent_id, "task": task})
    messages: list = [
        HumanMessage(
            content=(
                "你是一次任务的子执行者，没有计划，也不能再委派。"
                f"\n契约摘要：{contract_brief}\n任务：{task}"
            ),
            id=f"{subagent_id}-task",
        )
    ]
    summary = ""
    status = "completed"
    for turn in range(SUBAGENT_TURN_LIMIT):
        ai = model.invoke(messages, tools=_FILE_TOOLS, purpose="subagent")
        if not isinstance(ai, AIMessage):
            ai = AIMessage(content=str(ai))
        ai.id = ai.id or f"{subagent_id}-ai-{turn}"
        messages.append(ai)
        calls = ai.tool_calls or []
        if not calls:
            summary = ai.content if isinstance(ai.content, str) else str(ai.content)
            break
        for call in calls:
            name = call["name"]
            if name in {"delegate_task", "submit_for_verification", "set_step_status"}:
                messages.append(ToolMessage(content="子执行者不能使用这个工具", tool_call_id=call["id"], status="error"))
                status = "failed"
                summary = "子执行者试图越权"
                break
            try:
                if name == "read_file":
                    result = read_text(workspace_root, call["args"]["path"])
                elif name == "write_file":
                    write_text(workspace_root, call["args"]["path"], call["args"].get("content") or "")
                    result = f"已写入 {call['args']['path']}"
                elif name == "list_dir":
                    result = list_dir(workspace_root, call["args"]["path"])
                else:
                    result = "未知工具"
                    status = "failed"
            except (WorkspaceError, KeyError) as exc:
                result = str(exc)
                status = "failed"
            messages.append(ToolMessage(content=result[:4000], tool_call_id=call["id"], id=f"{subagent_id}-tool-{turn}-{call['id']}"))
        if status == "failed":
            break
    else:
        status = "failed"
        summary = "子执行者超过轮数上限"
    if not summary:
        summary = "子执行者没有给出结果"
        status = "failed"
    sink.emit(
        run_id,
        "subagent.completed",
        {"subagent_id": subagent_id, "status": status, "summary": summary[:200]},
    )
    return {"status": status, "summary": summary[:4000], "messages": messages}


_FILE_TOOLS = [
    {"name": "read_file", "args": {"path": ""}},
    {"name": "write_file", "args": {"path": "", "content": ""}},
    {"name": "list_dir", "args": {"path": ""}},
]
