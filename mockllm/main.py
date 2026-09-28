"""本地 OpenAI 兼容桩。场景由消息里的 [[scenario:NAME]] 选择。"""

import hashlib
import re
from pathlib import Path

import yaml
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI()
ROOT = Path(__file__).parent / "scenarios"
requests_log: list[dict] = []


class ChatIn(BaseModel):
    model: str = "mock"
    messages: list[dict]
    stream: bool = False
    tools: list | None = None


def text_of(message: dict) -> str:
    content = message.get("content", "")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(part.get("text", "") for part in content if isinstance(part, dict))
    return str(content)


def scenario_name(messages: list[dict]) -> str | None:
    for message in messages:
        found = re.search(r"\[\[scenario:([^\]]+)\]\]", text_of(message))
        if found:
            return found.group(1)
    return None


def load_scenario(name: str) -> dict:
    path = ROOT / f"{name}.yaml"
    if not path.exists():
        raise HTTPException(status_code=404, detail=f"unknown scenario {name}")
    return yaml.safe_load(path.read_text())


def step_index(messages: list[dict]) -> int:
    last_human = -1
    for i, message in enumerate(messages):
        if message.get("role") == "user":
            last_human = i
    return sum(1 for message in messages[last_human + 1 :] if message.get("role") == "assistant")


def chat_payload(content: str, tool_calls: list | None) -> dict:
    message = {"role": "assistant", "content": content}
    if tool_calls:
        message["tool_calls"] = [
            {
                "id": f"call_{i}",
                "type": "function",
                "function": {"name": call["name"], "arguments": str(call.get("args", {}))},
            }
            for i, call in enumerate(tool_calls)
        ]
    return {
        "id": "chatcmpl-mock",
        "object": "chat.completion",
        "choices": [{"index": 0, "message": message, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
    }


@app.post("/v1/chat/completions")
def chat(body: ChatIn):
    record = body.model_dump()
    requests_log.append(record)
    name = scenario_name(body.messages)
    if name is None:
        return chat_payload("ok", None)
    scenario = load_scenario(name)
    step = step_index(body.messages)
    errors = scenario.get("errors") or []
    for item in errors:
        if item.get("at_step") == step:
            raise HTTPException(status_code=item.get("status", 500), detail="scenario error")
    steps = scenario.get("steps") or []
    if step >= len(steps):
        return chat_payload("done", None)
    chosen = steps[step]
    return chat_payload(chosen.get("content") or "", chosen.get("tool_calls"))


@app.post("/v1/embeddings")
def embeddings(body: dict):
    raw = body.get("input", "")
    if isinstance(raw, list):
        raw = " ".join(map(str, raw))
    digest = hashlib.sha256(str(raw).encode()).digest()
    vector = [((digest[i % len(digest)] / 255) * 2 - 1) for i in range(1536)]
    return {"object": "list", "data": [{"object": "embedding", "index": 0, "embedding": vector}], "model": body.get("model", "mock")}


@app.get("/mock/requests")
def list_requests(run: str | None = None):
    if run is None:
        return {"requests": requests_log}
    matched = [item for item in requests_log if run in str(item)]
    return {"requests": matched}


@app.post("/mock/reset")
def reset():
    requests_log.clear()
    return {"ok": True}
