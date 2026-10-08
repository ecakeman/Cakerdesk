from __future__ import annotations

import json
import os
import time

from langchain_openai import ChatOpenAI

_RETRYABLE_STATUS = {429, 500, 502, 503, 504}
_RETRYABLE_NAMES = {"APIConnectionError", "APITimeoutError", "ConnectError", "ReadTimeout", "ConnectTimeout", "RemoteProtocolError"}


def _retryable(exc: BaseException) -> bool:
    status = getattr(exc, "status_code", None)
    if status in _RETRYABLE_STATUS:
        return True
    return type(exc).__name__ in _RETRYABLE_NAMES


def _once(call):
    try:
        return call()
    except Exception as exc:
        if not _retryable(exc):
            raise
        time.sleep(0.2)
        return call()


class ChatModel:
    """只构造 ChatOpenAI。结构化输出和工具绑定都走这一条，不另做 Provider。"""

    def __init__(self, llm: ChatOpenAI) -> None:
        self.llm = llm

    def invoke(self, messages, tools=None, *, purpose: str = "lead"):
        def call():
            bound = self.llm
            if tools and purpose in {"lead", "subagent"}:
                bound = self.llm.bind_tools(tools)
            return bound.invoke(messages)

        return _once(call)

    def structured(self, messages, schema, *, purpose: str):
        # 当前兼容接口不接受 json_schema response_format，统一走函数调用。
        # 个别响应会在合法 JSON 后再多一个右括号，或直接不带参数。空结果再请求一次。
        def call():
            parsed = _read_structured(self.llm, messages, schema)
            if parsed is None:
                time.sleep(0.2)
                parsed = _read_structured(self.llm, messages, schema)
            if parsed is None:
                raise ValueError("模型没有返回可用的结构化结果")
            return parsed

        return _once(call)


def _read_structured(llm, messages, schema):
    result = llm.with_structured_output(schema, method="function_calling", include_raw=True).invoke(messages)
    if result["parsed"] is not None:
        return result["parsed"]
    for item in getattr(result["raw"], "invalid_tool_calls", None) or []:
        payload = _first_object(item.get("args") or "")
        if payload is not None:
            return schema.model_validate(payload)
    return None


def _first_object(text: str):
    text = text.strip()
    if not text:
        return None
    try:
        value, _ = json.JSONDecoder().raw_decode(text)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def env_model() -> ChatModel:
    if not (os.environ.get("OPENAI_API_KEY") or os.environ.get("OPENAI_BASE_URL")):
        raise SystemExit("需要 OPENAI_API_KEY 或 OPENAI_BASE_URL 才能启动真实模型")
    base_url = os.environ.get("OPENAI_BASE_URL")
    extra = {}
    # DeepSeek 思考模式不接受 function calling 所需的 tool_choice。
    if base_url and "deepseek" in base_url:
        extra["extra_body"] = {"thinking": {"type": "disabled"}}
    llm = ChatOpenAI(
        model=os.environ.get("CAKERDESK_MODEL", "gpt-4o-mini"),
        api_key=os.environ.get("OPENAI_API_KEY"),
        base_url=base_url,
        **extra,
    )
    return ChatModel(llm)
