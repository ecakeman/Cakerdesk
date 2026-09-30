"""加载并校验场景 YAML。步骤必须能从当次消息推出来，服务端不记游标。"""

from __future__ import annotations

import re
from pathlib import Path
from typing import Any

import yaml

ID_RE = re.compile(r"^call_(\d+)_(\d+)_([a-z0-9]{6})$")
SCENARIO_RE = re.compile(r"\[\[scenario:([^\]]+)\]\]")


class ScenarioError(ValueError):
    """场景文件不合法，启动时直接失败。"""


def load_dir(root: Path) -> dict[str, dict[str, Any]]:
    """读目录下全部 yaml，重名或非法则抛错。"""
    out: dict[str, dict[str, Any]] = {}
    for path in sorted(root.glob("*.yaml")):
        data = yaml.safe_load(path.read_text())
        if not isinstance(data, dict):
            raise ScenarioError(f"{path} 不是对象")
        name = data.get("name")
        if not name or not isinstance(name, str):
            raise ScenarioError(f"{path} 缺 name")
        steps = expand_steps(data.get("steps") or [], path)
        if name in out:
            raise ScenarioError(f"场景重名 {name}")
        out[name] = {
            "name": name,
            "steps": steps,
            "errors": data.get("errors") or [],
            "delays": data.get("delays") or [],
        }
    return out


def expand_steps(raw: list[Any], path: Path) -> list[dict[str, Any]]:
    """把 repeat 展开成逐步列表，并检查 content 无 tool_calls 只能在最后。"""
    if not raw:
        raise ScenarioError(f"{path} 没有 steps")
    expanded: list[dict[str, Any]] = []
    for i, step in enumerate(raw):
        if not isinstance(step, dict):
            raise ScenarioError(f"{path} 步骤 {i} 不是对象")
        n = int(step.get("repeat", 1))
        if n < 1:
            raise ScenarioError(f"{path} repeat 必须 ≥ 1")
        body = {k: v for k, v in step.items() if k != "repeat"}
        calls = body.get("tool_calls") or []
        for c in calls:
            if not isinstance(c, dict) or not c.get("name"):
                raise ScenarioError(f"{path} 工具名不能为空")
        for _ in range(n):
            expanded.append(body)
    for i, step in enumerate(expanded):
        calls = step.get("tool_calls") or []
        if step.get("content") is not None and not calls and i != len(expanded) - 1:
            raise ScenarioError(f"{path} 纯文本步骤只能在最后")
    return expanded


def scenario_from_messages(messages: list[dict[str, Any]]) -> str | None:
    """从第一条 user 消息抽出 [[scenario:NAME]]。"""
    for msg in messages:
        if msg.get("role") != "user":
            continue
        text = _text(msg.get("content"))
        m = SCENARIO_RE.search(text)
        if m:
            return m.group(1)
        return None
    return None


def next_step(messages: list[dict[str, Any]], n_steps: int) -> int:
    """从后往前找最后一条带 tool_calls 的 assistant。id 合法则步号 +1，否则 0。"""
    if n_steps <= 0:
        return 0
    for msg in reversed(messages):
        if msg.get("role") != "assistant":
            continue
        calls = msg.get("tool_calls") or []
        if not calls:
            continue
        step_nos: list[int] = []
        for call in calls:
            cid = call.get("id") or ""
            m = ID_RE.match(cid)
            if not m:
                return 0
            step_nos.append(int(m.group(1)))
        nxt = max(step_nos) + 1
        if nxt >= n_steps:
            return n_steps - 1
        return nxt
    return 0


def _text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for part in content:
            if isinstance(part, dict) and part.get("type") == "text":
                parts.append(str(part.get("text") or ""))
        return "".join(parts)
    return str(content)
