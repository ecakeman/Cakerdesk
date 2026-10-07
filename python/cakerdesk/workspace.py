from __future__ import annotations

from pathlib import Path


class WorkspaceError(Exception):
    pass


_READ_ROOTS = {"uploads", "work", "artifacts"}
_WRITE_ROOTS = {"work", "artifacts"}


def ensure_layout(root: Path) -> None:
    for name in ("uploads", "work", "artifacts"):
        (root / name).mkdir(parents=True, exist_ok=True)


def resolve(root: Path, rel: str, *, write: bool) -> Path:
    if not rel or rel.startswith("/") or "\\" in rel:
        raise WorkspaceError("路径必须是工作区内的相对路径")
    parts = Path(rel).parts
    if ".." in parts or parts[0] not in (_WRITE_ROOTS if write else _READ_ROOTS):
        raise WorkspaceError("路径越出工作区或目录不允许")
    root_resolved = root.resolve()
    path = (root_resolved / rel).resolve()
    if not str(path).startswith(str(root_resolved)):
        raise WorkspaceError("路径越出工作区")
    return path


def scan_artifacts(root: Path) -> list[str]:
    base = root / "artifacts"
    if not base.exists():
        return []
    found = []
    for path in base.rglob("*"):
        if path.is_file():
            found.append(str(path.relative_to(root.resolve())))
    return sorted(found)


def read_text(root: Path, rel: str, limit: int = 8000) -> str:
    path = resolve(root, rel, write=False)
    if not path.is_file():
        raise WorkspaceError(f"{rel} 不存在")
    data = path.read_text(encoding="utf-8")
    if len(data) > limit:
        return data[:limit] + "\n…(已截断，全文仍在磁盘)"
    return data


def write_text(root: Path, rel: str, content: str) -> str:
    path = resolve(root, rel, write=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    if not path.is_file() or path.stat().st_size == 0 and content:
        raise WorkspaceError(f"写入 {rel} 后未能在磁盘确认")
    return rel


def list_dir(root: Path, rel: str) -> str:
    path = resolve(root, rel, write=False)
    if not path.is_dir():
        raise WorkspaceError(f"{rel} 不是目录")
    names = sorted(child.name for child in path.iterdir())
    return "\n".join(names) if names else "(空目录)"
