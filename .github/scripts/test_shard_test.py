#!/usr/bin/env python3
"""Offline correctness and CLI contract tests for the Go shard planner."""

from collections import Counter
from contextlib import redirect_stderr, redirect_stdout
import hashlib
import io
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import test_shard as planner


class ShardPlannerTests(unittest.TestCase):
    def invoke(self, text, count=3, index=0):
        with tempfile.TemporaryDirectory(prefix="emby-test-shard-") as root:
            inventory = Path(root) / "inventory.txt"
            inventory.write_text(text, encoding="utf-8")
            stdout, stderr = io.StringIO(), io.StringIO()
            with redirect_stdout(stdout), redirect_stderr(stderr):
                status = planner.main(["--list-file", str(inventory), "--count", str(count), "--index", str(index)])
            return status, stdout.getvalue(), stderr.getvalue()

    def test_every_name_occurs_once_in_exhaustive_disjoint_partition(self):
        names = ["Test" + str(index) for index in range(29)]
        shards = planner.plan_shards(list(reversed(names)), 4)
        self.assertEqual(Counter(name for shard in shards for name in shard), Counter(names))
        for left, shard in enumerate(shards):
            self.assertTrue(shard)
            for other in shards[left + 1:]:
                self.assertTrue(set(shard).isdisjoint(other))
        self.assertEqual(shards, [sorted(names)[index::4] for index in range(4)])

    def test_unsorted_inventory_has_stable_plan_and_hash(self):
        first = self.invoke("TestZ\nExampleB\nFuzzA\nTestD\nok\tmodule/pkg\t0.012s\n", 2, 1)
        second = self.invoke("TestD\nFuzzA\nTestZ\nExampleB\n", 2, 1)
        self.assertEqual(first, second)
        canonical = "ExampleB\nFuzzA\nTestD\nTestZ\n"
        self.assertIn(hashlib.sha256(canonical.encode()).hexdigest(), first[2])

    def test_new_tests_are_automatically_included_without_an_allowlist(self):
        names = ["TestA", "TestB", "ExampleA", "FuzzA"]
        self.assertEqual(sum("TestNew" in shard for shard in planner.plan_shards(names + ["TestNew"], 3)), 1)
        self.assertEqual(set(sum(planner.plan_shards(names + ["TestNew"], 3), [])), set(names + ["TestNew"]))

    def test_test_example_fuzz_and_unicode_identifiers_are_retained(self):
        names = planner.parse_inventory("TestA\nExample\nExampleWidget_output\nFuzzSeeds\nTest中文\n")
        self.assertEqual(len(names), 5)
        shards = planner.plan_shards(names, 2)
        self.assertEqual(set(sum(shards, [])), set(names))

    def test_exact_anchors_do_not_match_another_top_level_parent(self):
        pattern = planner.pattern_for_names(["TestFoo", "FuzzSeeds", "ExampleWidget"])
        self.assertTrue(pattern.startswith("^"))
        self.assertTrue(pattern.endswith("$"))
        self.assertNotIn("/", pattern)  # Go subsequently runs every descendant.
        for name in ["TestFoo", "FuzzSeeds", "ExampleWidget"]:
            self.assertRegex(name, pattern)
        for name in ["TestFooExtra", "PrefixTestFoo", "ExampleWidgetExtra"]:
            self.assertIsNone(re.search(pattern, name))

    def test_regex_builder_escapes_metacharacters_defensively(self):
        names = ["TestA+B", "Example[one]", "Fuzz.dot", "TestA$"]
        pattern = planner.pattern_for_names(names)
        for name in names:
            self.assertIsNotNone(re.fullmatch(pattern, name))
        for name in ["TestAAAB", "Exampleo", "FuzzXdot", "TestA"]:
            self.assertIsNone(re.fullmatch(pattern, name))
        # Such names cannot actually be Go top-level identifiers. The inventory
        # parser rejects them as well as the builder escaping defensively.
        for name in names:
            with self.assertRaises(planner.PlanningError):
                planner.parse_inventory(name)

    def test_cli_all_indices_have_exactly_once_union(self):
        names = ["TestA", "TestB", "TestC", "ExampleA", "FuzzA", "FuzzB", "TestZ"]
        selections = []
        for index in range(4):
            status, output, audit = self.invoke("\n".join(names), 4, index)
            self.assertEqual(status, 0)
            self.assertEqual(len(output.splitlines()), 1)
            self.assertTrue(output.startswith("^"))
            self.assertIn("inventory_sha256=", audit)
            self.assertIn("shard_sizes=2,2,2,1", audit)
            selections.extend(name for name in names if re.fullmatch(output.strip(), name))
        self.assertEqual(Counter(selections), Counter(names))

    def test_invalid_count_and_index_fail_without_stdout(self):
        for count, index in [(0, 0), (-1, 0), (2, -1), (2, 2), (2, 9)]:
            with self.subTest(count=count, index=index):
                status, output, error = self.invoke("TestA\nTestB\n", count, index)
                self.assertNotEqual(status, 0)
                self.assertEqual(output, "")
                self.assertIn("error:", error)

    def test_duplicate_names_fail_even_when_separated_by_summary(self):
        status, output, error = self.invoke("TestA\nok\tmodule/pkg\t0.001s\nTestA\n", 1)
        self.assertNotEqual(status, 0)
        self.assertEqual(output, "")
        self.assertIn("duplicate test name", error)
        with self.assertRaises(planner.PlanningError):
            planner.plan_shards(["TestA", "TestA"], 1)

    def test_empty_inventory_and_empty_shards_fail_closed(self):
        for text, count in [("", 1), ("\n ok module/pkg (cached)\n", 1), ("? module/pkg [no test files]\n", 1), ("TestOnly\n", 2)]:
            status, output, error = self.invoke(text, count)
            self.assertNotEqual(status, 0)
            self.assertEqual(output, "")
            self.assertIn("empty", error)

    def test_unknown_lines_are_never_silently_dropped(self):
        for line in ["BenchmarkThing", "PASS", "warning: lost tests", "TestA/subtest", "TestBad-Name", "Testlowercase", "Examplelowercase", "Fuzzlowercase", "ok nonsense", '{"Action":"output"}']:
            status, output, error = self.invoke("TestValid\n" + line + "\n", 1)
            self.assertNotEqual(status, 0)
            self.assertEqual(output, "")
            self.assertIn("unknown inventory line", error)

    def test_supported_go_summary_variants_are_not_test_names(self):
        names = planner.parse_inventory("TestA\nok  \tmodule/pkg\t0.001s\nok module/pkg (cached)\nok module/pkg 0s [no tests to run]\n")
        self.assertEqual(names, ["TestA"])

    def test_go_discovery_uses_actual_top_level_list_without_running_tests(self):
        completed = subprocess.CompletedProcess([], 0, "TestA\nFuzzA\nExampleA\nok module/pkg 0.01s\n", "diagnostic\n")
        with patch.object(planner.subprocess, "run", return_value=completed) as run:
            audit = io.StringIO()
            with redirect_stderr(audit):
                names = planner.parse_inventory(planner.discover("./internal/server"))
            self.assertEqual(names, ["ExampleA", "FuzzA", "TestA"])
            self.assertEqual(run.call_args.args[0], ["go", "test", "-list", "^(Test|Example|Fuzz)", "./internal/server"])
            self.assertNotIn("-run", run.call_args.args[0])
            self.assertEqual(audit.getvalue(), "diagnostic\n")

    def test_go_failure_and_missing_inventory_never_emit_a_regex(self):
        with patch.object(planner.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "TestPartial\n", "build failed\n")):
            output, error = io.StringIO(), io.StringIO()
            with redirect_stdout(output), redirect_stderr(error):
                status = planner.main(["--package", "./internal/server", "--count", "1", "--index", "0"])
            self.assertNotEqual(status, 0)
            self.assertEqual(output.getvalue(), "")
            self.assertIn("exit status 1", error.getvalue())
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            self.assertEqual(planner.main(["--list-file", "/nonexistent/shard-inventory", "--count", "1", "--index", "0"]), 1)


if __name__ == "__main__":
    unittest.main()
