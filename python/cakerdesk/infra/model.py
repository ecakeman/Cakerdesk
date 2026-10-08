from __future__ import annotations

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
        return _once(lambda: self.llm.with_structured_output(schema).invoke(messages))


def env_model() -> ChatModel:
    if not (os.environ.get("OPENAI_API_KEY") or os.environ.get("OPENAI_BASE_URL")):
        raise SystemExit("需要 OPENAI_API_KEY 或 OPENAI_BASE_URL 才能启动真实模型")
    llm = ChatOpenAI(
        model=os.environ.get("CAKERDESK_MODEL", "gpt-4o-mini"),
        api_key=os.environ.get("OPENAI_API_KEY"),
        base_url=os.environ.get("OPENAI_BASE_URL"),
    )
    return ChatModel(llm)
