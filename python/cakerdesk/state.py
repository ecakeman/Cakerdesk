from __future__ import annotations

from typing import Annotated, Any

from langgraph.graph.message import add_messages
from typing_extensions import TypedDict


class AgentState(TypedDict, total=False):
    messages: Annotated[list, add_messages]
    contract: dict | None
    plan: dict | None
    summary: str
    findings: dict | None
    artifacts: list[str]
    delegations: list[dict]
    guard: dict
    attempts: list[dict]
    route: str


def fresh_guard(run_id: str) -> dict[str, Any]:
    return {
        "run_id": run_id,
        "replan_count": 0,
        "plain_text_streak": 0,
        "last_tool_sig": "",
        "tool_repeat": 0,
        "total_tokens": 0,
        "executed": False,
        "delegate_count": 0,
        "cancel": False,
        "fail_reason": "",
        "fail_message": "",
        "reflect_mode": "",
    }


def estimate_tokens(text: str) -> int:
    return max(1, len(text) // 4) if text else 0
