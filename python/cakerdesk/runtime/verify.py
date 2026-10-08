from __future__ import annotations

from pathlib import Path

from cakerdesk.infra.workspace import WorkspaceError, resolve


def verify(contract: dict, workspace_root: Path, messages: list | None = None) -> dict:
    """按文件、工具结果、消息的顺序检查。文件已经失败时，不用模型翻案。"""
    del messages
    items = []
    for deliverable in contract.get("deliverables") or []:
        path = deliverable["path"]
        try:
            disk = resolve(workspace_root, path, write=False)
        except WorkspaceError as exc:
            items.append(_failed(path, "路径不合法", str(exc)))
            continue
        if not disk.is_file():
            items.append(_failed(f"{path} 存在且非空", f"{path} 不存在", "契约要求的交付物没有落到磁盘"))
            continue
        if deliverable.get("must_be_nonempty", True) and disk.stat().st_size == 0:
            items.append(_failed(f"{path} 存在且非空", f"{path} 文件为空", "交付物是空文件"))
            continue
        text = disk.read_text(encoding="utf-8")
        missing = [piece for piece in deliverable.get("must_contain") or [] if piece not in text]
        if missing:
            shown = "、".join(missing)
            items.append(
                _failed(
                    f"{path} 包含「{shown}」",
                    f"{path} 存在且非空，正文没有「{shown}」",
                    "契约要求的内容不在文件里",
                )
            )
            continue
        items.append(
            {
                "criterion": f"{path} 满足契约",
                "status": "passed",
                "evidence": f"已在 {path} 中找到要求的内容" if deliverable.get("must_contain") else f"{path} 存在且非空",
                "reason": "",
            }
        )
    passed = bool(items) and all(item["status"] == "passed" for item in items)
    return {"passed": passed, "items": items}


def _failed(criterion: str, evidence: str, reason: str) -> dict:
    return {
        "criterion": criterion,
        "status": "failed",
        "evidence": evidence,
        "reason": reason,
    }
