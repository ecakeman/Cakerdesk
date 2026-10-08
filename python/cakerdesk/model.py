from __future__ import annotations

import os

from langchain_openai import ChatOpenAI


class ChatModel:
    """只构造 ChatOpenAI。结构化输出和工具绑定都走这一条，不另做 Provider。"""

    def __init__(self, llm: ChatOpenAI) -> None:
        self.llm = llm

    def invoke(self, messages, tools=None, *, purpose: str = "lead"):
        bound = self.llm
        if tools and purpose in {"lead", "subagent"}:
            bound = self.llm.bind_tools(tools)
        return bound.invoke(messages)

    def structured(self, messages, schema, *, purpose: str):
        return self.llm.with_structured_output(schema).invoke(messages)


def env_model() -> ChatModel:
    if not (os.environ.get("OPENAI_API_KEY") or os.environ.get("OPENAI_BASE_URL")):
        raise SystemExit("需要 OPENAI_API_KEY 或 OPENAI_BASE_URL 才能启动真实模型")
    llm = ChatOpenAI(
        model=os.environ.get("CAKERDESK_MODEL", "gpt-4o-mini"),
        api_key=os.environ.get("OPENAI_API_KEY"),
        base_url=os.environ.get("OPENAI_BASE_URL"),
    )
    return ChatModel(llm)
