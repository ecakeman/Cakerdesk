from __future__ import annotations

import json
from typing import Any


def normalize_contract(raw: dict, goal: str) -> dict:
    deliverables = raw.get("deliverables") or []
    cleaned = []
    for item in deliverables:
        path = str(item.get("path") or "")
        if not path.startswith("artifacts/") or ".." in path:
            raise ValueError(f"交付路径不合法: {path}")
        cleaned.append(
            {
                "path": path,
                "must_exist": True,
                "must_be_nonempty": True,
                "must_contain": [str(part) for part in item.get("must_contain") or []],
            }
        )
    if not cleaned:
        raise ValueError("契约没有交付物")
    return {"goal": raw.get("goal") or goal, "deliverables": cleaned}


def normalize_plan(raw: dict, *, version: int) -> dict:
    steps = []
    for index, step in enumerate(raw.get("steps") or [], start=1):
        step_id = str(step.get("id") or f"s{index}")
        title = str(step.get("title") or "").strip()
        status = step.get("status") or "pending"
        if status not in {"pending", "in_progress", "completed", "blocked"}:
            status = "pending"
        if not title:
            raise ValueError("步骤缺少标题")
        steps.append({"id": step_id, "title": title, "status": status, "note": str(step.get("note") or "")})
    if not 2 <= len(steps) <= 6:
        raise ValueError("步骤数量必须在 2 到 6")
    return {
        "version": version,
        "goal": raw.get("goal") or "",
        "steps": steps,
        "diff": raw.get("diff"),
    }


def validate_diff(old: dict, new: dict) -> None:
    old_steps = {step["id"]: step for step in old["steps"]}
    new_steps = {step["id"]: step for step in new["steps"]}
    preserved, modified, added, removed = [], [], [], []
    for step_id, step in old_steps.items():
        if step["status"] == "completed":
            current = new_steps.get(step_id)
            if current is None or current["title"] != step["title"] or current["status"] != "completed":
                raise ValueError(f"已完成步骤被改掉: {step_id}")
            preserved.append(step_id)
        elif step["status"] == "blocked":
            current = new_steps.get(step_id)
            if current is None or current["status"] != "pending":
                raise ValueError(f"blocked 步骤必须改回 pending: {step_id}")
            if current["status"] == "completed":
                raise ValueError("blocked 不能直接变成 completed")
            modified.append(step_id)
        elif step_id not in new_steps:
            removed.append(step_id)
    for step_id in new_steps:
        if step_id not in old_steps:
            if new_steps[step_id]["status"] != "pending":
                raise ValueError("新步骤必须是 pending")
            added.append(step_id)
    new["diff"] = {
        "preserved": preserved,
        "modified": modified,
        "added": added,
        "removed": removed,
    }


def step_matches_deliverable(step: dict, path: str) -> bool:
    blob = f"{step.get('title', '')} {step.get('note', '')}".lower()
    name = path.rsplit("/", 1)[-1].lower()
    stem = name.rsplit(".", 1)[0]
    return name in blob or stem in blob or "撰写" in step.get("title", "") or "报告" in step.get("title", "") or "report" in blob


def block_failed_steps(plan: dict, findings: dict) -> dict:
    if findings.get("passed"):
        return plan
    failed = [item for item in findings.get("items") or [] if item.get("status") == "failed"]
    updated = json.loads(json.dumps(plan))
    for step in updated["steps"]:
        if step["status"] != "completed":
            continue
        for item in failed:
            evidence = item.get("evidence") or ""
            path = ""
            for deliverable_path in _paths_from_evidence(evidence, plan):
                path = deliverable_path
                break
            if step_matches_deliverable(step, path or evidence):
                step["status"] = "blocked"
                step["note"] = evidence or item.get("reason") or "验证失败"
                break
    return updated


def _paths_from_evidence(evidence: str, plan: dict) -> list[str]:
    found = []
    for token in evidence.replace("，", " ").replace(",", " ").split():
        if token.startswith("artifacts/"):
            found.append(token.strip("。"))
    return found


def set_step_status(plan: dict, step_id: str, status: str, note: str = "") -> dict:
    if status not in {"in_progress", "completed"}:
        raise ValueError("Lead 只能把步骤标成 in_progress 或 completed")
    updated = json.loads(json.dumps(plan))
    for step in updated["steps"]:
        if step["id"] != step_id:
            continue
        if step["status"] == "completed":
            raise ValueError("Lead 不能修改已经 completed 的步骤")
        if step["status"] == "blocked":
            raise ValueError("blocked 只能由 Replan 改回 pending")
        step["status"] = status
        if note:
            step["note"] = note
        return updated
    raise ValueError(f"没有步骤 {step_id}")
