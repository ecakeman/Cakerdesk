import subprocess


def test_version() -> None:
    out = subprocess.check_output(["cd-kernel", "--version"], text=True)
    assert out.strip() == "0.1.0"
