from langchain_core.tools import tool


@tool
def list_dir(path: str) -> str:
    """列出工作区内一个目录的文件名。path 相对 thread 工作区。"""
    return path


@tool
def read_file(path: str) -> str:
    """读取工作区内的文本文件。path 相对 thread 工作区。"""
    return path


@tool
def write_file(path: str, content: str) -> str:
    """把文本写入 work/ 或 artifacts/ 下的相对路径。"""
    return path


@tool
def set_step_status(step_id: str, status: str, note: str = "") -> str:
    """把已有步骤标成 in_progress 或 completed。不能标成 blocked，也不能改已经 completed 的步骤。"""
    return step_id


@tool
def delegate_task(task: str) -> str:
    """把一段局部工作交给子执行者。task 要写清要读的文件和要返回的说明。"""
    return task


@tool
def submit_for_verification(summary: str) -> str:
    """提交当前交付物接受文件检查。summary 只说明这次提交，不是通过依据。"""
    return summary


LEAD_TOOLS = [list_dir, read_file, write_file, set_step_status, delegate_task, submit_for_verification]
FILE_TOOLS = [list_dir, read_file, write_file]
