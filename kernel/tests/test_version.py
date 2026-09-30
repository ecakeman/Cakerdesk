import subprocess


def test_version() -> None:
    """cd-kernel --version 必须打印包版本。"""
    out = subprocess.check_output(["cd-kernel", "--version"], text=True)
    assert out.strip() == "0.1.0"
