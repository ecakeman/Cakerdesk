from __future__ import annotations

import json

from langchain_core.messages import AIMessage


def tool(call_id: str, name: str, args: dict) -> dict:
    return {"id": call_id, "name": name, "args": args, "type": "tool_call"}


class DemoModel:
    """按真实 State 里已经发生的事决定下一次工具调用，不把结论文案写死在图里。"""

    def __init__(self) -> None:
        self.calls: list[dict] = []
        self.lead_n = 0
        self.sub_n = 0
        self.run_b = False
        self.b_lead = 0

    def invoke(self, messages, tools=None, *, purpose: str = "lead"):
        self.calls.append({"purpose": purpose, "messages": messages})
        if purpose == "summary":
            return AIMessage(content="已阅读资料并写过一版报告。")
        if purpose == "contract":
            return AIMessage(content=json.dumps({
                "goal": "完成周销售报告",
                "deliverables": [{"path": "artifacts/report.md", "must_contain": ["数据摘要", "异常点", "结论"]}],
            }, ensure_ascii=False))
        if purpose == "plan":
            return AIMessage(content=json.dumps({
                "goal": "完成周销售报告",
                "steps": [
                    {"id": "s1", "title": "阅读 notes.txt", "status": "pending"},
                    {"id": "s2", "title": "阅读 sales.csv", "status": "pending"},
                    {"id": "s3", "title": "委派异常点分析", "status": "pending"},
                    {"id": "s4", "title": "撰写 report.md", "status": "pending"},
                ],
            }, ensure_ascii=False))
        if purpose == "replan":
            return AIMessage(content=json.dumps({
                "goal": "完成周销售报告",
                "steps": [
                    {"id": "s1", "title": "阅读 notes.txt", "status": "completed"},
                    {"id": "s2", "title": "阅读 sales.csv", "status": "completed"},
                    {"id": "s3", "title": "委派异常点分析", "status": "completed"},
                    {"id": "s4", "title": "补写结论，保留已有两节", "status": "pending"},
                    {"id": "s5", "title": "对照三个标题后再次提交", "status": "pending"},
                ],
            }, ensure_ascii=False))
        if purpose == "reflect":
            blob = "\n".join(_text(message) for message in messages)
            if "正文没有" not in blob:
                return AIMessage(content=json.dumps({"items": []}, ensure_ascii=False))
            return AIMessage(content=json.dumps({
                "items": [
                    {"kind": "fact", "content": "周销售报告必须包含数据摘要、异常点、结论三个部分。"},
                    {"kind": "lesson", "content": "首次提交虽然生成了报告文件，但遗漏了结论，因此验证失败；重新规划后补写结论并再次提交，最终通过验证。"},
                ]
            }, ensure_ascii=False))
        if purpose == "subagent":
            self.sub_n += 1
            if self.sub_n == 1:
                return AIMessage(
                    content="内部分析不该进父级",
                    tool_calls=[tool("sub-read", "read_file", {"path": "work/sales.csv"})],
                )
            return AIMessage(content="第 4 周 units=12，相对前后周约 100 属于断点。")
        if self.run_b:
            self.b_lead += 1
            return AIMessage(content="", tool_calls=[tool("b-submit", "submit_for_verification", {"summary": "沿用上次要求提交"})])
        self.lead_n += 1
        n = self.lead_n
        if n == 1:
            return AIMessage(content="", tool_calls=[
                tool("read-notes", "read_file", {"path": "work/notes.txt"}),
                tool("read-csv", "read_file", {"path": "work/sales.csv"}),
            ])
        if n == 2:
            return AIMessage(content="", tool_calls=[
                tool("st-s1", "set_step_status", {"step_id": "s1", "status": "completed"}),
                tool("st-s2", "set_step_status", {"step_id": "s2", "status": "completed"}),
                tool("del-1", "delegate_task", {"task": "只根据 work/sales.csv 解释第 4 周为什么异常"}),
            ])
        if n == 3:
            return AIMessage(content="", tool_calls=[
                tool("write-1", "write_file", {"path": "artifacts/report.md", "content": "## 数据摘要\n四周里第 4 周最低。\n\n## 异常点\n第 4 周 units=12，相对前后周约 100 属于断点。\n"}),
                tool("st-s3", "set_step_status", {"step_id": "s3", "status": "completed"}),
                tool("st-s4", "set_step_status", {"step_id": "s4", "status": "completed"}),
            ])
        if n == 4:
            return AIMessage(content="报告写好了")
        if n == 5:
            return AIMessage(content="", tool_calls=[tool("submit-1", "submit_for_verification", {"summary": "第一次提交"})])
        if n == 6:
            return AIMessage(content="", tool_calls=[
                tool("write-2", "write_file", {"path": "artifacts/report.md", "content": "## 数据摘要\n四周里第 4 周最低。\n\n## 异常点\n第 4 周 units=12，相对前后周约 100 属于断点。\n\n## 结论\n第 4 周需要复核原始记录。\n"}),
                tool("st-s4b", "set_step_status", {"step_id": "s4", "status": "completed"}),
                tool("st-s5", "set_step_status", {"step_id": "s5", "status": "completed"}),
                tool("submit-2", "submit_for_verification", {"summary": "补写后再提交"}),
            ])
        raise AssertionError(f"意外的 Lead 调用 {n}")


def _text(message) -> str:
    content = message.content
    return content if isinstance(content, str) else str(content)
