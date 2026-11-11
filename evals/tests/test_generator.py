"""Tests for the Open Code Review eval generator and the tasks it commits.

    python3 -m unittest discover -s evals/tests -v

Standard library only. The simulation runs a task's own setup scripts, checks and oracle on this
machine with the task's paths moved under a temporary folder, so the whole step chain is
exercised without a container; it needs git and Go on PATH, as the generator does.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib
import unittest
from pathlib import Path

EVALS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(EVALS / "generator"))

import collect_ocr  # noqa: E402
import generate  # noqa: E402

TASKS = EVALS / "tasks"
SKILL = generate.ORCHESTRATOR.read_text()
BLOCKS = generate.orchestrator_blocks(SKILL)
SMALL = "vo-t06"  # the smallest committed case: ten changed files


def task_folders() -> list[Path]:
    """Every committed task folder."""
    return sorted(p.parent for p in TASKS.glob("*/task.toml"))


def block_pattern(block: str) -> re.Pattern:
    """A block of the orchestrator skill as a pattern: its text, with any filling for a placeholder."""
    text = re.escape(block).replace(re.escape("in the current directory"), r"in \S+")
    return re.compile(re.sub(r"<[a-z][a-z\\ ]*>", r"[^<>\\n]+", text))


class CommittedTasks(unittest.TestCase):
    """What the committed tasks must keep saying."""

    def test_the_committed_tasks_match_a_fresh_regeneration(self):
        self.assertEqual(generate.main(["--check"]), 0, "run evals/generator/generate.py and commit the result")

    def test_every_step_instruction_is_the_message_the_skill_sends(self):
        for task in task_folders():
            for step in sorted((task / "steps").iterdir()):
                kind = {"grouping": "grouper", "report": "report"}.get(step.name, "reviewer")
                text = (step / "instruction.md").read_text()
                if kind != "grouper":
                    first, _, text = text.partition("\n")
                    self.assertEqual(first, "The skills are installed in: /opt/review-skills", step)
                with self.subTest(task=task.name, step=step.name):
                    self.assertRegex(text.removesuffix("\n"), block_pattern(BLOCKS[kind]))
                    self.assertIsNone(re.search(r"<[a-z][a-z ]*>", text), "a placeholder was left unfilled")

    def test_every_setup_script_runs_the_command_the_skill_names(self):
        for task in task_folders():
            for step, block in (("grouping", "request"), ("group-01", "apply"), ("report", "merge")):
                script = (task / "steps" / step / "workdir" / "setup.sh").read_text()
                with self.subTest(task=task.name, step=step):
                    self.assertTrue(any(block_pattern(BLOCKS[block]).fullmatch(line) for line in script.splitlines()),
                                    f"{step}/setup.sh does not run the skill's {block} command")  # fmt: skip

    def test_a_task_declares_one_group_step_per_changed_file(self):
        for task in task_folders():
            config = tomllib.loads((task / "task.toml").read_text())
            steps = [s["name"] for s in config["steps"]]
            groups = [s for s in steps if s.startswith("group-")]
            with tempfile.TemporaryDirectory() as tmp:
                with tarfile.open(task / "environment" / "repo.tar.gz") as tar:
                    tar.extractall(tmp, filter="data")
                changed = subprocess.run(
                    ["git", "-C", f"{tmp}/repo", "diff", "--name-only", "--diff-filter=d", "HEAD~1", "HEAD"],
                    check=True, capture_output=True, text=True).stdout.split("\n")  # fmt: skip
            with self.subTest(task=task.name):
                self.assertEqual(steps[0], "grouping")
                self.assertEqual(steps[-1], "report")
                self.assertEqual(groups, [f"group-{i:02d}" for i in range(1, len(groups) + 1)])
                self.assertEqual(len(groups), len([p for p in changed if p]), "one group step per changed file")
                self.assertEqual(config["metadata"]["group_steps"], len(groups))
                self.assertEqual(config["multi_step_reward_strategy"], "final")

    def test_every_known_issue_sits_in_a_file_a_reviewer_can_be_given(self):
        for task in task_folders():
            known = json.loads((task / "steps" / "group-01" / "solution" / "oracle-comments.json").read_text())
            with tempfile.TemporaryDirectory() as tmp:
                with tarfile.open(task / "environment" / "repo.tar.gz") as tar:
                    tar.extractall(tmp, filter="data")
                changed = set(subprocess.run(
                    ["git", "-C", f"{tmp}/repo", "diff", "--name-only", "--diff-filter=d", "HEAD~1", "HEAD"],
                    check=True, capture_output=True, text=True).stdout.split("\n"))  # fmt: skip
            with self.subTest(task=task.name):
                self.assertTrue(known, "the oracle has no known issue")
                self.assertEqual({c["path"] for c in known} - changed, set())

    def test_the_grader_is_the_team_tasks_and_only_the_report_step_has_it(self):
        bundle = tomllib.loads(generate.BUNDLE.read_text())
        for team, twin in generate.load_cases(generate.CASES, bundle):
            for source in filter(None, (team, twin)):
                task = TASKS / source.name
                with self.subTest(task=source.name):
                    self.assertEqual(generate.differences(source.tests, task / "steps" / "report" / "tests"), [])
                    rubrics = [p for p in task.rglob("judge.toml") if "report" not in p.parts]
                    self.assertEqual(rubrics, [], "the answer key is outside the report step's grader")
                    self.assertFalse((task / "tests").exists(), "a task-level tests/ folder would be uploaded")


class Simulation(unittest.TestCase):
    """One task's steps run in Harbor's order on this machine, each agent played by a function."""

    @classmethod
    def setUpClass(cls):
        bundle = tomllib.loads(generate.BUNDLE.read_text())
        cls.ocr = generate.ocr_binary(os.environ.get("OCR_BIN"), generate.ocr_commit(bundle))

    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="ocr-sim-"))
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.task = TASKS / SMALL
        with tarfile.open(self.task / "environment" / "repo.tar.gz") as tar:
            tar.extractall(self.root / "workspace", filter="data")
        subprocess.run(["git", "-C", str(self.root / "workspace/repo"), "read-tree", "HEAD"], check=True)
        (self.root / "workspace/review").mkdir()
        (self.root / "opt/review-eval").mkdir(parents=True)
        for name in ("collect_ocr.py", "ocr-rule.json"):
            shutil.copyfile(self.task / "environment" / name, self.root / "opt/review-eval" / name)
        (self.root / "bin").mkdir()
        (self.root / "bin/ocr").symlink_to(self.ocr)
        self.review = self.root / "workspace/review"
        self.rewards: dict[str, dict] = {}
        self.ran: list[str] = []

    def run_script(self, script: Path, step: Path) -> int:
        """Run one of the task's scripts with its absolute paths moved under the temporary root."""
        text = script.read_text()
        for path in ("/workspace", "/opt/review-eval", "/logs"):
            text = text.replace(path, f"{self.root}{path}")
        text = text.replace("/solution/", f"{step}/solution/").replace("\ncd /\n", f"\ncd {self.root}\n")
        copy = self.root / "workspace" / script.name
        copy.write_text(text)
        env = {**os.environ, "PATH": f"{self.root}/bin:{os.environ['PATH']}", "HOME": str(self.root / "home")}
        (self.root / "home").mkdir(exist_ok=True)
        done = subprocess.run(["bash", str(copy)], cwd=self.root / "workspace", env=env, capture_output=True, text=True)
        copy.unlink(missing_ok=True)
        self.last_output = done.stdout + done.stderr
        return done.returncode

    def run_trial(self, agent) -> None:
        """Every declared step in order: setup, the agent unless setup said none is needed, the check."""
        config = tomllib.loads((self.task / "task.toml").read_text())
        for name in [s["name"] for s in config["steps"]]:
            step = self.task / "steps" / name
            code = self.run_script(step / "workdir" / "setup.sh", step)
            self.assertIn(code, (0, 78), f"{name}/setup.sh failed: {self.last_output}")
            if code == 0:
                self.ran.append(name)
                agent(name, step)
            if name != "report":
                self.assertEqual(self.run_script(step / "tests" / "test.sh", step), 0, self.last_output)
                self.rewards[name] = json.loads((self.root / "logs/verifier/reward.json").read_text())
        collect_ocr.collect(self.review, self.root / "logs/artifacts")

    def oracle(self, name: str, step: Path) -> None:
        """The oracle agent: the step's solve.sh."""
        self.assertEqual(self.run_script(step / "solution" / "solve.sh", step), 0, self.last_output)

    def findings(self) -> list | None:
        """What the collect hook handed the grader, or None for no findings file."""
        path = self.root / "logs/artifacts/findings.json"
        return json.loads(path.read_text()) if path.exists() else None

    def known(self) -> list[dict]:
        """The case's known issues, as the oracle states them."""
        return json.loads((self.task / "steps/group-01/solution/oracle-comments.json").read_text())

    def test_the_oracle_hands_the_grader_every_known_issue(self):
        self.run_trial(self.oracle)

        # One group of ten files: nine reviewer steps have no agent work.
        self.assertEqual(self.ran, ["grouping", "group-01", "report"])
        self.assertEqual({n: r["skipped"] for n, r in self.rewards.items() if n not in self.ran},
                         {f"group-{i:02d}": 1.0 for i in range(2, 11)})  # fmt: skip
        self.assertTrue(all(r["reward"] == 1.0 for r in self.rewards.values()), self.rewards)
        findings = self.findings()
        self.assertEqual(sorted((f["file"], f["line_start"], f["body"]) for f in findings),
                         sorted((c["path"], c["start_line"], c["content"]) for c in self.known()))  # fmt: skip
        self.assertTrue(all(f["severity"] in ("high", "medium", "low") and f["title"] for f in findings))

    def test_an_agent_that_does_nothing_scores_nothing(self):
        self.run_trial(lambda name, step: None)

        # No grouping answer: OCR's fallback gives each of the ten files its own group.
        self.assertEqual(len(self.ran), 12)
        self.assertEqual(json.loads((self.review / "groups.json").read_text())["fallback"], "the grouping answer is empty")
        self.assertTrue(all(r["reward"] == 0.0 for r in self.rewards.values()), self.rewards)
        merged = json.loads((self.review / "merged.json").read_text())
        self.assertEqual((len(merged["finished"]), len(merged["unfinished"])), (0, 10))
        self.assertIsNone(self.findings(), "a trial with no report must leave no findings file")

    def test_a_three_group_answer_starts_three_reviewers(self):
        def agent(name: str, step: Path) -> None:
            if name == "grouping":
                answer = [{"label": "a", "files": [0, 1, 2, 3]}, {"label": "b", "files": [4, 5, 6]}, {"label": "c", "files": [7, 8, 9]}]
                (self.review / "grouping.json").write_text("```json\n" + json.dumps(answer) + "\n```\n")
            else:
                self.oracle(name, step)

        self.run_trial(agent)

        self.assertEqual(self.ran, ["grouping", "group-01", "group-02", "group-03", "report"])
        self.assertEqual(self.rewards["grouping"]["reward"], 1.0, "a fenced JSON array is an answer OCR accepts")
        self.assertEqual(len(self.findings()), len(self.known()))

    def test_a_report_planted_before_the_report_step_is_never_graded(self):
        def agent(name: str, step: Path) -> None:
            if name == "group-01":
                planted = {"comments": [{"path": "x", "content": "planted", "severity": "high"}]}
                (self.review / "report.json").write_text(json.dumps(planted))
            if name == "grouping":
                self.oracle(name, step)

        self.run_trial(agent)

        self.assertIsNone(self.findings(), "the report step wrote nothing, so nothing may be graded")

    def test_a_failed_setup_is_an_error_and_never_a_skip(self):
        step = self.task / "steps" / "group-02"
        # No groups.json yet: the step cannot know whether it has work.
        self.assertNotIn(self.run_script(step / "workdir" / "setup.sh", step), (0, 78), self.last_output)

        shutil.rmtree(self.root / "workspace/repo/.git")
        grouping = self.task / "steps" / "grouping"
        self.assertNotIn(self.run_script(grouping / "workdir" / "setup.sh", grouping), (0, 78), self.last_output)


class Collect(unittest.TestCase):
    """The collect hook's conversion."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="ocr-collect-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.out, self.artifacts = self.tmp / "review", self.tmp / "artifacts"
        self.out.mkdir()

    def test_a_report_becomes_flat_findings(self):
        long = "x" * 200
        report = {"comments": [
            {"path": "a.go", "content": "Nil map write\n\nThe map is never made.", "start_line": 4, "end_line": 6, "severity": "critical"},
            {"path": "b.go", "content": long, "start_line": 9, "severity": "Medium"},
            {"path": "c.go", "content": "no severity given"},
            "not an object", {"path": "d.go"}, {"content": "no path"},
        ], "summary": {"total_files": 3}}  # fmt: skip
        (self.out / "report.json").write_text(json.dumps(report))
        (self.out / "merged.json").write_text("{}")

        self.assertEqual(collect_ocr.collect(self.out, self.artifacts), 3)

        first, second, third = json.loads((self.artifacts / "findings.json").read_text())
        self.assertEqual(first, {"file": "a.go", "line_start": 4, "line_end": 6, "severity": "high",
                                 "title": "Nil map write", "body": "Nil map write\n\nThe map is never made."})  # fmt: skip
        self.assertEqual((second["severity"], second["line_end"], len(second["title"])), ("medium", 9, 120))
        self.assertEqual(third["severity"], "low")
        self.assertTrue((self.artifacts / "merged.json").exists())
        self.assertEqual(json.loads((self.artifacts / "ocr-report.json").read_text()), report)

    def test_nothing_gradable_leaves_no_findings_file(self):
        cases = {"missing": None, "not json": "the review is done", "no list": '{"comments": "none"}',
                 "a list": "[]", "too big": json.dumps({"comments": [], "pad": "x" * 600})}  # fmt: skip
        for name, text in cases.items():
            with self.subTest(name):
                (self.out / "report.json").unlink(missing_ok=True)
                if text is not None:
                    (self.out / "report.json").write_text(text)
                self.assertIsNone(collect_ocr.collect(self.out, self.artifacts, max_bytes=500))
                self.assertFalse((self.artifacts / "findings.json").exists())

    def test_a_linked_report_is_not_read(self):
        (self.tmp / "elsewhere.json").write_text('{"comments": []}')
        (self.out / "report.json").symlink_to(self.tmp / "elsewhere.json")
        self.assertIsNone(collect_ocr.collect(self.out, self.artifacts))

    def test_an_empty_report_is_an_empty_findings_list(self):
        (self.out / "report.json").write_text('{"comments": []}')
        self.assertEqual(collect_ocr.collect(self.out, self.artifacts), 0)
        self.assertEqual(json.loads((self.artifacts / "findings.json").read_text()), [])


class Manifest(unittest.TestCase):
    """The TOML the generator writes by hand."""

    def test_tables_round_trip(self):
        table = {"name": "a/b", "n": 3, "f": 3600.0, "on": True, "hosts": ["*.x.com", 'q"uote'],
                 "ge": {"mode": "allowlist", "images": {"node:24@sha256:ab": "docker.io/library/node@sha256:ab"}}}  # fmt: skip
        self.assertEqual(tomllib.loads(generate._table("metadata", table)), {"metadata": table})


if __name__ == "__main__":
    unittest.main()
