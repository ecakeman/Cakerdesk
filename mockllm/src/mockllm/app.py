"""无状态 OpenAI 兼容假模型。步骤只看这一次请求里的消息。"""

from __future__ import annotations

import json
import random
import string
import threading
import time
from collections import defaultdict
from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException, Query
from fastapi.responses import JSONResponse

from mockllm.scenarios import load_dir, next_step, scenario_from_messages

SCENARIO_DIR = Path(__file__).resolve().parents[2] / "scenarios"
ALPH = string.ascii_lowercase + string.digits

app = FastAPI()
_lock = threading.RLock()
_scenarios: dict[str, dict[str, Any]] = {}
_errors: dict[tuple[str, int], int] = defaultdict(int)
_log: list[dict[str, Any]] = []


def load_scenarios(root: Path | None = None) -> None:
    """启动时加载场景。测试可换目录。"""
    global _scenarios
    _scenarios = load_dir(root or SCENARIO_DIR)


def reset_state() -> None:
    """清空错误计数和请求日志。"""
    with _lock:
        _errors.clear()
        _log.clear()


try:
    load_scenarios()
except FileNotFoundError:
    _scenarios = {}


@app.get("/healthz")
def healthz() -> dict[str, str]:
    """compose 探活。"""
    return {"status": "ok"}


@app.post("/mock/reset")
def mock_reset() -> dict[str, str]:
    """测试夹具：清计数。"""
    reset_state()
    return {"status": "ok"}


@app.get("/mock/requests")
def mock_requests(user: str = Query(default="")) -> dict[str, Any]:
    """最近 500 条摘要，可按请求体 user 过滤。"""
    with _lock:
        items = list(_log[-500:])
    if user != "":
        items = [x for x in items if x.get("user") == user]
    return {"items": items}


@app.post("/v1/chat/completions")
def chat_completions(body: dict[str, Any]) -> JSONResponse:
    """Chat Completions。步骤从 messages 推断，不靠内存游标。"""
    messages = body.get("messages") or []
    if not isinstance(messages, list):
        raise HTTPException(400, "messages")
    # 压缩模式：不推进步骤。
    if _summarize(messages):
        return JSONResponse(_summary_payload(messages))
    name = scenario_from_messages(messages)
    if not name or name not in _scenarios:
        raise HTTPException(400, "unknown scenario")
    scene = _scenarios[name]
    steps = scene["steps"]
    step = next_step(messages, len(steps))
    user = str(body.get("user") or "")
    # 错误注入按 (user, step) 计数。
    err = _match_error(scene["errors"], step)
    if err is not None:
        with _lock:
            key = (user, step)
            used = _errors[key]
            if used < int(err["times"]):
                _errors[key] = used + 1
                _append_log(user, name, step, int(err["status"]))
                headers = {}
                if int(err["status"]) == 429:
                    headers["retry-after"] = "0"
                return JSONResponse({"error": {"message": "injected"}}, status_code=int(err["status"]), headers=headers)
    delay = _match_delay(scene["delays"], step)
    if delay:
        time.sleep(int(delay["ms"]) / 1000)
    payload = _completion(steps[step], step, messages)
    _append_log(user, name, step, 200)
    return JSONResponse(payload)


def _summarize(messages: list[dict[str, Any]]) -> bool:
    for msg in messages:
        if msg.get("role") == "system" and "[[cd:summarize]]" in str(msg.get("content") or ""):
            return True
    return False


def _summary_payload(messages: list[dict[str, Any]]) -> dict[str, Any]:
    n = len(messages)
    content = f"SUMMARY: {n} messages condensed."
    return _wrap_message(content, None, messages)


def _match_error(items: list[Any], step: int) -> dict[str, Any] | None:
    for item in items:
        if int(item.get("step", -1)) == step:
            return item
    return None


def _match_delay(items: list[Any], step: int) -> dict[str, Any] | None:
    for item in items:
        if int(item.get("step", -1)) == step:
            return item
    return None


def _completion(step: dict[str, Any], step_no: int, messages: list[dict[str, Any]]) -> dict[str, Any]:
    """按场景一步生成 assistant。id 含随机后缀，测试不能写死。"""
    calls_out = []
    for i, call in enumerate(step.get("tool_calls") or []):
        suffix = "".join(random.choice(ALPH) for _ in range(6))
        cid = f"call_{step_no}_{i}_{suffix}"
        args = call.get("args") or {}
        calls_out.append({
            "id": cid,
            "type": "function",
            "function": {
                "name": call["name"],
                "arguments": json.dumps(args, ensure_ascii=False),
            },
        })
    content = step.get("content")
    if content is None and not calls_out:
        content = ""
    return _wrap_message(content, calls_out or None, messages)


def _wrap_message(content: Any, tool_calls: list[dict[str, Any]] | None, messages: list[dict[str, Any]]) -> dict[str, Any]:
    msg: dict[str, Any] = {"role": "assistant", "content": content}
    finish = "stop"
    if tool_calls:
        msg["tool_calls"] = tool_calls
        finish = "tool_calls"
        if content is None:
            msg["content"] = None
    prompt_chars = sum(len(json.dumps(m, ensure_ascii=False)) for m in messages)
    completion_chars = len(json.dumps(msg, ensure_ascii=False))
    return {
        "id": "chatcmpl-mock",
        "object": "chat.completion",
        "choices": [{"index": 0, "message": msg, "finish_reason": finish}],
        "usage": {
            "prompt_tokens": prompt_chars // 4,
            "completion_tokens": completion_chars // 4,
            "total_tokens": (prompt_chars + completion_chars) // 4,
        },
    }


def _append_log(user: str, scenario: str, step: int, status: int) -> None:
    with _lock:
        _log.append({"user": user, "scenario": scenario, "step": step, "status": status})
        if len(_log) > 500:
            del _log[:-500]


def serve() -> None:
    """compose / 本机入口。"""
    import uvicorn

    load_scenarios()
    uvicorn.run(app, host="0.0.0.0", port=7330)
