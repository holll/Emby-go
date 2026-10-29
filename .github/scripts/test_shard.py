#!/usr/bin/env python3
"""Plan disjoint, exhaustive top-level Go test shards; never execute the tests.

Use --package PATH to discover names with `go test -list`, or --list-file PATH
for an offline inventory. --index is zero-based. Stdout contains only a Go -run
regular expression. A selected parent retains all its subtests/fuzz seeds.
Inventory SHA256 covers sorted UTF-8 names, each terminated by one newline.
"""

import argparse
from collections import Counter
import hashlib
from pathlib import Path
import re
import subprocess
import sys
import unicodedata


class PlanningError(ValueError):
    """Discovery or partitioning could omit tests; refuse to produce a regex."""


SUMMARY = re.compile(
    r"ok\s+\S+\s+(?:[0-9]+(?:\.[0-9]+)?s|\(cached\))"
    r"(?:\s+\[no tests to run\])?"
)
NO_TEST_FILES = re.compile(r"\?\s+\S+\s+\[no test files\]")


def is_test_name(name):
    """Go identifiers only, with an explicitly supported top-level prefix."""
    prefix = next((prefix for prefix in ("Test", "Example", "Fuzz") if name.startswith(prefix)), None)
    if prefix is None:
        return False
    suffix = name[len(prefix):]
    # Match cmd/go's top-level naming rule: the next rune is not lowercase.
    if suffix and unicodedata.category(suffix[0]) == "Ll":
        return False
    return all(
        char == "_" or unicodedata.category(char).startswith("L")
        or (index > 0 and unicodedata.category(char) == "Nd")
        for index, char in enumerate(name)
    )


def parse_inventory(text):
    names = []
    seen = set()
    for line_number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or SUMMARY.fullmatch(line) or NO_TEST_FILES.fullmatch(line):
            continue
        if not is_test_name(line):
            raise PlanningError(f"unknown inventory line {line_number}: {line!r}")
        if line in seen:
            raise PlanningError(f"duplicate test name: {line!r}")
        seen.add(line)
        names.append(line)
    if not names:
        raise PlanningError("empty test inventory")
    return sorted(names)


def plan_shards(names, count):
    if count < 1:
        raise PlanningError("--count must be positive")
    if not names:
        raise PlanningError("empty test inventory")
    if len(names) != len(set(names)):
        raise PlanningError("duplicate test names")
    if any(not is_test_name(name) for name in names):
        raise PlanningError("invalid top-level test name")
    if count > len(names):
        raise PlanningError(f"empty shard: {len(names)} tests cannot fill {count} shards")
    ordered = sorted(names)
    shards = [ordered[index::count] for index in range(count)]
    flattened = [name for shard in shards for name in shard]
    if any(not shard for shard in shards) or Counter(flattened) != Counter(ordered):
        raise PlanningError("shard inventory is not an exact, nonempty partition")
    return shards


def pattern_for_names(names):
    # No slash component: Go -run selects these exact parents and then all
    # descendants. Both anchors are essential to avoid selecting TestFooBar.
    return "^(?:" + "|".join(re.escape(name) for name in names) + ")$"


def discover(package):
    result = subprocess.run(
        ["go", "test", "-list", "^(Test|Example|Fuzz)", package],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, encoding="utf-8",
        errors="strict", check=False,
    )
    if result.stderr:
        sys.stderr.write(result.stderr)
        if not result.stderr.endswith("\n"):
            sys.stderr.write("\n")
    if result.returncode:
        raise PlanningError(f"go test -list failed with exit status {result.returncode}")
    return result.stdout


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--package", help="one Go package to inventory")
    source.add_argument("--list-file", type=Path, help="offline go test -list output")
    parser.add_argument("--count", type=int, required=True, help="number of nonempty shards")
    parser.add_argument("--index", type=int, required=True, help="zero-based shard index")
    args = parser.parse_args(argv)
    try:
        if args.count < 1 or not 0 <= args.index < args.count:
            raise PlanningError("require --count >= 1 and 0 <= --index < --count")
        text = args.list_file.read_text(encoding="utf-8") if args.list_file is not None else discover(args.package)
        names = parse_inventory(text)
        shards = plan_shards(names, args.count)
        digest = hashlib.sha256("".join(name + "\n" for name in names).encode("utf-8")).hexdigest()
        selected = shards[args.index]
        print(f"test-shard total={len(names)} count={args.count} index={args.index} selected={len(selected)} inventory_sha256={digest}", file=sys.stderr)
        print("test-shard shard_sizes=" + ",".join(str(len(shard)) for shard in shards), file=sys.stderr)
        print(pattern_for_names(selected))
    except (PlanningError, OSError, UnicodeError) as error:
        print(f"test-shard: error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
