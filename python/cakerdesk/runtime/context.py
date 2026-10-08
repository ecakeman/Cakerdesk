from __future__ import annotations

import json
from typing import Any

from langchain_core.messages import BaseMessage, HumanMessage, SystemMessage

from cakerdesk.runtime.state import estimate_tokens

RULES = """你是 Cakerdesk 的 Lead Agent。
未调用 submit_for_verification 不算完成。普通总结文字之后你仍要继续使用工具或提交验证。
set_step_status 只能把步骤标成 in_progress 或 completed，不能把步骤标成 blocked，也不能改已经 completed 的步骤。
completed 只表示你认为该动作做完了，不表示契约已经满足。
不要把上下文块写回对话。"""

CONTEXT_MARKER = "## Cakerdesk Context"


def _clip(text: str, token_budget: int) -> str:
    limit = token_budget * 4
    if len(text) <= limit:
        return text
    return text[:limit] + "…"


def _message_text(messages: list[BaseMessage]) -> str:
    parts = []
    for message in messages:
        content = message.content
        parts.append(content if isinstance(content, str) else str(content))
    return "\n".join(parts)


def _dump(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2)


class ContextManager:
    def build(
        self,
        state: dict,
        memory_rows: list[dict],
        *,
        skill_names: list[str] | None = None,
        rules: str | None = None,
    ) -> list[BaseMessage]:
        rules = rules or RULES
        sections = self.apply_budget(self._sections(state, memory_rows, skill_names or []), rules=rules)
        context = "\n\n".join(part for part in sections if part)
        recent = self._fit_messages(list(state.get("messages") or []), token_room=4000)
        return [
            SystemMessage(content=rules),
            SystemMessage(content=f"{CONTEXT_MARKER}\n{context}"),
            *recent,
        ]

    def _fit_messages(self, messages: list[BaseMessage], token_room: int) -> list[BaseMessage]:
        kept = list(messages)
        while len(kept) > 4 and estimate_tokens(_message_text(kept)) > token_room:
            kept.pop(0)
        return kept

    def _sections(self, state: dict, memory_rows: list[dict], skill_names: list[str]) -> list[tuple[str, str, int, bool]]:
        """(name, text, token_cap, droppable_from_front)。"""
        contract = state.get("contract") or {}
        plan = state.get("plan") or {}
        findings = state.get("findings")
        artifacts = state.get("artifacts") or []
        summary = state.get("summary") or ""
        memory_text = "\n".join(
            f"- {row['kind']}: {row['content']}" for row in memory_rows
        )
        skills = "、".join(skill_names) if skill_names else ""
        return [
            ("contract", "Contract\n" + (_dump(contract) if contract else "（无）"), 800, False),
            ("plan", "Plan\n" + (_dump(plan) if plan else "（无）"), 800, False),
            ("findings", "Findings\n" + (_dump(findings) if findings else "（无）"), 600, False),
            ("memory", "Memory\n" + (memory_text or "（无）"), 800, True),
            ("summary", "Summary\n" + (summary or "（无）"), 1500, True),
            ("artifacts", "Artifacts\n" + ("\n".join(artifacts) if artifacts else "（无）"), 300, False),
            ("skills", "Skills\n" + (skills or "（无）"), 200, False),
        ]

    def apply_budget(self, sections: list[tuple[str, str, int, bool]], total: int = 12000, rules: str = RULES) -> list[str]:
        rendered: list[str] = []
        used = estimate_tokens(rules)
        for name, text, cap, drop_front in sections:
            body = text
            if name == "memory" and drop_front:
                lines = [line for line in text.splitlines() if line.startswith("- ")]
                header = "Memory"
                while lines and estimate_tokens(header + "\n" + "\n".join(lines)) > cap:
                    lines.pop(0)
                body = header + "\n" + ("\n".join(lines) if lines else "（无）")
            if name == "summary":
                body = "Summary\n" + _clip(text.removeprefix("Summary\n"), cap)
            if name == "plan" and estimate_tokens(text) > cap and "diff" in text:
                body = _clip(text, cap)
            else:
                body = _clip(body, cap)
            cost = estimate_tokens(body)
            if used + cost > total and name in {"memory", "summary", "skills"}:
                continue
            used += cost
            rendered.append(body)
        return rendered
