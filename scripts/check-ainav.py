#!/usr/bin/env python3
"""Check the navigation map's per-file byte limits."""

import argparse
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "directory",
        nargs="?",
        type=Path,
        default=Path(__file__).resolve().parents[1] / ".ainav",
        help="navigation directory (defaults to the repository's .ainav)",
    )
    directory = parser.parse_args().directory
    if not (directory / "index.md").is_file():
        parser.error(f"missing navigation entry point: {directory / 'index.md'}")

    files = sorted(directory.rglob("*.md"))
    failures = []
    for path in files:
        relative = path.relative_to(directory)
        limit = 10000 if relative == Path("index.md") else 2999
        size = path.stat().st_size
        if size > limit:
            failures.append(f"{path}: {size} bytes exceeds maximum {limit}")

    if failures:
        print("\n".join(failures))
        return 1
    print(f"Navigation size check passed ({len(files)} Markdown files).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
