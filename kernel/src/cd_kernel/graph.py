"""legacy 图：agent 调模型，tools 走网关，两次提醒后仍不交结果就失败。"""

from __future__ import annotations

import os
from typing import Annotated, Any

import httpx
from langchain_core.messages import HumanMessage, SystemMessage, ToolMessage
from langchain_core.runnables import RunnableConfig
from langchain_openai import ChatOpenAI
from langgraph.checkpoint.base import BaseCheckpointSaver
from langgraph.graph import END, START, StateGraph
from langgraph.graph.message import add_messages
from typing_extensions import TypedDict


class State(TypedDict):
    messages: Annotated[list[Any], add_messages]
    result: dict[str, Any] | None
    nudges: int
    failure: str | None


def build_graph(saver: BaseCheckpointSaver):
    graph = StateGraph(State)
    graph.add_node("agent", agent)
    graph.add_node("tools", tools)
    graph.add_node("nudge", nudge)
    graph.add_node("fail", fail)
    graph.add_edge(START, "agent")
    # 失败放进节点。在条件边里抛异常，到 ainvoke 外面时经常被包了一层，调用方对不上类型。
    graph.add_conditional_edges("agent", route_after_agent, ["tools", "nudge", "fail"])
    graph.add_conditional_edges("tools", route_after_tools, ["agent", END])
    graph.add_edge("nudge", "agent")
    graph.add_edge("fail", END)
    return graph.compile(checkpointer=saver)


def route_after_agent(state: State) -> str:
    last = state["messages"][-1]
    if getattr(last, "tool_calls", None):
        return "tools"
    if state["nudges"] < 2:
        return "nudge"
    return "fail"


def route_after_tools(state: State) -> str:
    if state.get("result") is not None:
        return END
    return "agent"


async def agent(state: State, config: RunnableConfig) -> dict:
    conf = config.get("configurable") or {}
    llm = ChatOpenAI(
        model=conf["model"],
        base_url=os.environ["CD_LLM_BASE_URL"],
        api_key=os.environ.get("CD_LLM_API_KEY", "mock"),
        max_retries=0,
        timeout=60,
        model_kwargs={"user": conf["thread_id"]},
    )
    specs = conf.get("tools") or []
    if specs:
        llm = llm.bind(
            tools=[
                {
                    "type": "function",
                    "function": {
                        "name": spec["name"],
                        "description": spec["description"],
                        "parameters": spec["parameters"],
                    },
                }
                for spec in specs
            ]
        )
    message = await llm.ainvoke(state["messages"])
    return {"messages": [message]}


async def tools(state: State, config: RunnableConfig) -> dict:
    conf = config.get("configurable") or {}
    last = state["messages"][-1]
    messages = []
    result = None
    async with httpx.AsyncClient(timeout=60) as client:
        for call in last.tool_calls:
            args = call.get("args") or {}
            res = await client.post(
                f"{conf['api_url']}/internal/runs/{conf['thread_id']}/tool-calls",
                headers={"Authorization": f"Bearer {conf['token']}"},
                json={
                    "worker_id": conf["worker_id"],
                    "attempt": conf["attempt"],
                    "tool_call_id": call["id"],
                    "name": call["name"],
                    "args": args,
                },
            )
            res.raise_for_status()
            body = res.json()
            messages.append(ToolMessage(content=str(body.get("output") or ""), tool_call_id=call["id"]))
            if body.get("status") == "succeeded" and call["name"] == "submit_result":
                result = args
    out: dict[str, Any] = {"messages": messages}
    if result is not None:
        out["result"] = result
    return out


def nudge(state: State) -> dict:
    return {
        "messages": [HumanMessage(content="完成后必须调用 submit_result")],
        "nudges": state["nudges"] + 1,
    }


def fail(_state: State) -> dict:
    return {"failure": "no_result"}


def first_input(system_prompt: str, user_input: str) -> dict:
    return {
        "messages": [SystemMessage(content=system_prompt), HumanMessage(content=user_input)],
        "result": None,
        "nudges": 0,
        "failure": None,
    }
