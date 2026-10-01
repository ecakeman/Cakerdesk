import argparse
import sys
from importlib.metadata import version


def main() -> None:
    """A1 只接受 --version。其它参数退出码 2，避免空进程看起来像在跑。"""
    parser = argparse.ArgumentParser(prog="cd-kernel")
    parser.add_argument("--version", action="store_true")
    args = parser.parse_args()
    if not args.version:
        print("unknown command", file=sys.stderr)
        raise SystemExit(2)
    print(version("cd-kernel"))
