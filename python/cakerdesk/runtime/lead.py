from __future__ import annotations

from langchain_core.messages import AIMessage


def decide_after_model(ai: AIMessage, guard: dict) -> str:
    """Lead 的出口只有工具、继续、提交验证，或护栏失败。没有主动 Replan。"""
    if guard.get("fail_reason"):
        return "runtime_fail"
    names = [call["name"] for call in (ai.tool_calls or [])]
    if "submit_for_verification" in names:
        return "tools"
    if names:
        return "tools"
    if int(guard.get("plain_text_streak") or 0) >= 3:
        content = ai.content if isinstance(ai.content, str) else str(ai.content or "")
        guard["fail_reason"] = "model_no_progress"
        guard["fail_message"] = "模型连续多次没有产生工具调用，任务无法继续推进"
        guard["last_output"] = content[:200]
        return "runtime_fail"
    return "lead_model"


def decide_after_tools(ai: AIMessage, guard: dict) -> str:
    if guard.get("fail_reason"):
        return "runtime_fail"
    names = [call["name"] for call in (ai.tool_calls or [])]
    if "submit_for_verification" in names:
        return "verify"
    return "lead_model"
