import argparse
import sys
from importlib.metadata import version


def main() -> None:
    parser = argparse.ArgumentParser(prog="cd-kernel")
    parser.add_argument("--version", action="store_true")
    args = parser.parse_args()
    if not args.version:
        print("unknown command", file=sys.stderr)
        raise SystemExit(2)
    print(version("cd-kernel"))
