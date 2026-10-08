from __future__ import annotations

from langchain_core.messages import HumanMessage, RemoveMessage, SystemMessage

from cakerdesk.runtime.state import estimate_tokens

SUMMARY_INSTRUCTION = "把下面的旧对话压成一段事实摘要。不要添加新的任务指令。"


def maybe_summarize(state: dict, model, *, message_threshold: int, keep_recent: int) -> dict | None:
    """直接调用模型。不经过 ContextManager，也不再进入本函数。"""
    messages = list(state.get("messages") or [])
    text = "\n".join(_text(message) for message in messages)
    over_count = len(messages) > message_threshold
    over_tokens = estimate_tokens(text) > 6000
    if not over_count and not over_tokens:
        return None
    if len(messages) <= keep_recent:
        return None
    old = messages[:-keep_recent]
    prompt = [
        SystemMessage(content=SUMMARY_INSTRUCTION),
        HumanMessage(
            content="已有摘要：\n"
            + (state.get("summary") or "（无）")
            + "\n\n待压缩消息：\n"
            + "\n".join(_text(message) for message in old)
        ),
    ]
    response = model.invoke(prompt, tools=None, purpose="summary")
    summary = response.content if isinstance(response.content, str) else str(response.content)
    usage = int((getattr(response, "usage_metadata", None) or {}).get("total_tokens") or estimate_tokens(summary))
    guard = dict(state.get("guard") or {})
    guard["total_tokens"] = int(guard.get("total_tokens") or 0) + usage
    removals = []
    for message in old:
        if not getattr(message, "id", None):
            continue
        removals.append(RemoveMessage(id=message.id))
    return {"summary": summary.strip(), "messages": removals, "guard": guard}


def _text(message) -> str:
    content = message.content
    return content if isinstance(content, str) else str(content)
