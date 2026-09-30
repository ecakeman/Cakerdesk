import json
import threading
import time
from pathlib import Path

import httpx
import pytest
import yaml
from fastapi.testclient import TestClient
from openai import OpenAI

from mockllm.app import app, load_scenarios, reset_state
from mockllm.scenarios import ID_RE, ScenarioError, load_dir

client = TestClient(app)


@pytest.fixture(autouse=True)
def _fresh() -> None:
    """每个测试重新加载场景并清计数。"""
    load_scenarios()
    reset_state()


def complete(messages: list, user: str = "u1") -> httpx.Response:
    """打一次 Chat Completions。"""
    return client.post("/v1/chat/completions", json={"messages": messages, "user": user})


def user_msg(scenario: str) -> dict:
    """第一条 user 消息里带场景名。"""
    return {"role": "user", "content": f"go [[scenario:{scenario}]]"}


def test_ids_unique_and_parseable() -> None:
    res = complete([user_msg("pair")])
    assert res.status_code == 200
    calls = res.json()["choices"][0]["message"]["tool_calls"]
    ids = [c["id"] for c in calls]
    assert len(ids) == 2
    assert len(set(ids)) == 2
    for i in ids:
        assert ID_RE.match(i)


def test_arguments_are_json() -> None:
    res = complete([user_msg("pair")])
    for call in res.json()["choices"][0]["message"]["tool_calls"]:
        args = call["function"]["arguments"]
        assert isinstance(args, str)
        parsed = json_loads(args)
        assert isinstance(parsed, dict)


def json_loads(s: str) -> dict:
    """arguments 必须是 JSON 字符串。"""
    return json.loads(s)


def test_step_from_history() -> None:
    first = complete([user_msg("two-step")])
    assert first.status_code == 200
    assistant = first.json()["choices"][0]["message"]
    tool_id = assistant["tool_calls"][0]["id"]
    pair = [
        user_msg("two-step"),
        assistant,
        {"role": "tool", "tool_call_id": tool_id, "content": "{}"},
    ]
    second = complete(pair)
    assert second.json()["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "submit_result"
    # 丢掉更早的对话，只留最后一对 assistant/tool，步数仍由 id 决定。
    truncated = [user_msg("two-step"), assistant, pair[-1]]
    again = complete(truncated)
    assert again.json()["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "submit_result"


def test_replay_same_request_same_step() -> None:
    messages = [user_msg("simple")]
    a = complete(messages).json()["choices"][0]["message"]["tool_calls"][0]["function"]
    b = complete(messages).json()["choices"][0]["message"]["tool_calls"][0]["function"]
    assert a["name"] == b["name"]
    assert json_loads(a["arguments"]) == json_loads(b["arguments"])


def test_error_times() -> None:
    messages = [user_msg("flaky")]
    assert complete(messages, user="a").status_code == 429
    assert complete(messages, user="a").status_code == 429
    assert complete(messages, user="a").status_code == 200
    assert complete(messages, user="b").status_code == 429


def test_repeat_expansion() -> None:
    names = []
    messages: list = [user_msg("repeater")]
    for _ in range(3):
        res = complete(messages)
        assert res.status_code == 200
        msg = res.json()["choices"][0]["message"]
        names.append(msg["tool_calls"][0]["function"]["name"])
        messages.append(msg)
        messages.append({"role": "tool", "tool_call_id": msg["tool_calls"][0]["id"], "content": "{}"})
    assert names == ["list_files", "list_files", "list_files"]
    last = complete(messages)
    assert last.json()["choices"][0]["message"]["content"] == "Done."


def test_summarize_mode() -> None:
    messages = [
        {"role": "system", "content": "[[cd:summarize]]"},
        user_msg("simple"),
        {"role": "assistant", "content": "x"},
    ]
    res = complete(messages)
    assert res.status_code == 200
    assert res.json()["choices"][0]["message"]["content"] == "SUMMARY: 3 messages condensed."


def test_invalid_scenario_rejected_at_startup(tmp_path: Path) -> None:
    bad = {
        "name": "bad",
        "steps": [
            {"content": "early"},
            {"tool_calls": [{"name": "submit_result", "args": {}}]},
        ],
    }
    (tmp_path / "bad.yaml").write_text(yaml.safe_dump(bad))
    with pytest.raises(ScenarioError):
        load_dir(tmp_path)


def test_openai_sdk_parses() -> None:
    """完成标准：官方 SDK 能解析 mock 的响应。"""
    import uvicorn

    config = uvicorn.Config(app, host="127.0.0.1", port=18733, log_level="error")
    server = uvicorn.Server(config)
    threading.Thread(target=server.run, daemon=True).start()
    for _ in range(50):
        try:
            httpx.get("http://127.0.0.1:18733/healthz", timeout=0.2)
            break
        except httpx.RequestError:
            time.sleep(0.05)
    sdk = OpenAI(api_key="mock", base_url="http://127.0.0.1:18733/v1")
    out = sdk.chat.completions.create(
        model="mock-1",
        messages=[{"role": "user", "content": "[[scenario:simple]] hi"}],
    )
    assert out.choices[0].message.tool_calls[0].function.name == "submit_result"
    json_loads(out.choices[0].message.tool_calls[0].function.arguments)
    server.should_exit = True
