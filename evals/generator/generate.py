"""Write the Open Code Review eval tasks: one multi-step Harbor task per row of cases.toml.

    python3 evals/generator/generate.py [--cases FILE] [--out DIR] [--ocr BIN] [--check | --digest]

A case names a published code-review team task. Its snapshot, answer key and grader are copied as
they are; its reviewer steps are replaced by the steps the open-code-review-orchestrator skill
directs: a grouping agent, one reviewer per group, then the report step. Each step's instruction
is the message that skill sends, and each setup script is the command it runs. A second run
changes nothing; `--check` fails when the committed tasks differ from a fresh run. Standard
library only, plus git and Go on PATH.
"""

from __future__ import annotations

import argparse
import filecmp
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib
from dataclasses import dataclass
from pathlib import Path

HERE = Path(__file__).resolve().parent
EVALS = HERE.parent
REPO = EVALS.parent
BUNDLE = HERE / "bundle.toml"
CASES = EVALS / "cases.toml"
OUT = EVALS / "tasks"
RULE = EVALS / "ocr-rule.json"
COLLECT = HERE / "collect_ocr.py"
SKILLS = REPO / "skills"
ORCHESTRATOR = SKILLS / "open-code-review-orchestrator" / "SKILL.md"
CACHE = Path(os.environ.get("OCR_EVALS_CACHE", Path.home() / ".cache" / "ocr-evals"))

GROUPING = "grouping"
REPORT = "report"
JUDGE_SUFFIX = "-llm-judge"
SETUP_EXIT_NO_AGENT_WORK = 78  # Harbor: this step has no agent work
INTENT_PREFIX = "What the change is meant to do: "
_BLOCK = re.compile(r"```\n(.*?)\n```", re.S)
_CANARY = re.compile(r"harbor-canary GUID ([0-9a-f-]+)")
_PLACEHOLDER = re.compile(r"<[a-z][a-z ]*>")
_BARE_KEY = re.compile(r"[A-Za-z0-9_-]+")


class GenerationError(Exception):
    """A case, a source or the skill is not in the shape the generator needs."""


def _git(repo: Path, *argv: str) -> str:
    """Run git in repo and return its output, stripped."""
    done = subprocess.run(["git", "-C", str(repo), *argv], check=True, capture_output=True, text=True)
    return done.stdout.strip()


# --- what a case brings -------------------------------------------------------------------------


@dataclass
class TeamTask:
    """A published code-review team task, read as a case's input."""

    name: str
    folder: Path
    config: dict
    canary: str
    validator: str
    intent: str
    base: str
    head: str
    snapshot: Path
    oracle: list[dict]

    @property
    def tests(self) -> Path:
        """The grader folder, copied into the report step byte for byte."""
        return self.folder / "steps" / self.validator / "tests"


def _unpack(archive: Path) -> Path:
    """The snapshot unpacked once in the cache, keyed by the archive's digest."""
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()[:16]
    target = CACHE / "snapshots" / digest
    if not (target / "done").exists():
        shutil.rmtree(target, ignore_errors=True)
        target.mkdir(parents=True)
        with tarfile.open(archive) as tar:
            tar.extractall(target, filter="data")
        (target / "done").touch()
    return target


def _oracle_comment(finding: dict, levels: dict) -> dict:
    """One of the team's oracle findings as an Open Code Review comment."""
    title, body = finding.get("title", ""), finding.get("description", "")
    content = body if body.startswith(title) else f"{title}\n\n{body}".strip()
    level = finding.get("severity")
    if level not in levels:
        raise GenerationError(f"oracle finding {title!r} has severity {level!r}, not in [oracle_severity]")
    return {
        "path": finding["file"],
        "content": content,
        "start_line": finding.get("line_start"),
        "end_line": finding.get("line_end"),
        "category": "bug",
        "severity": levels[level],
    }


def read_team_task(folder: Path, bundle: dict) -> TeamTask:
    """Read everything a case reuses from its team task, refusing a shape that has moved."""
    try:
        config = tomllib.loads((folder / "task.toml").read_text())
        text = (folder / "task.toml").read_text()
    except OSError as exc:
        raise GenerationError(f"{folder} is not a task folder: {exc}") from exc
    canary = _CANARY.search(text)
    steps = [s["name"] for s in config.get("steps", [])]
    separate = [s["name"] for s in config.get("steps", [])
                if s.get("verifier", {}).get("environment_mode") == "separate"]  # fmt: skip
    if not canary or len(separate) != 1 or steps[-1] != separate[0]:
        raise GenerationError(f"{folder}: expected a canary and one separately graded last step")
    validator = separate[0]
    intents = {
        line[len(INTENT_PREFIX):].strip()
        for step in steps[:-1]
        for line in (folder / "steps" / step / "instruction.md").read_text().splitlines()
        if line.startswith(INTENT_PREFIX)
    }  # fmt: skip
    if len(intents) != 1:
        raise GenerationError(f"{folder}: expected one intent line across the reviewers, got {len(intents)}")
    archive = folder / "environment" / (bundle["agent"]["repo_dir"] + ".tar.gz")
    snapshot = _unpack(archive) / bundle["agent"]["repo_dir"]
    commits = _git(snapshot, "rev-list", "HEAD").split()
    if len(commits) != 2:
        raise GenerationError(f"{archive}: expected a base and a head commit, got {len(commits)}")
    oracle = [
        _oracle_comment(finding, bundle["oracle_severity"])
        for step in steps[:-1]
        for path in sorted((folder / "steps" / step / "solution").glob("findings.*.json"))
        for finding in json.loads(path.read_text())["findings"]
    ]
    if not oracle:
        raise GenerationError(f"{folder}: its oracle holds no findings")
    return TeamTask(name=folder.name, folder=folder, config=config, canary=canary.group(1),
                    validator=validator, intent=intents.pop(), base=commits[1], head=commits[0],
                    snapshot=snapshot, oracle=oracle)  # fmt: skip


def source_folder(name: str, source: dict, cases_file: Path) -> Path:
    """The folder of team tasks a source names: beside the cases file, or a repository at a commit."""
    if "path" in source:
        return (cases_file.parent / source["path"]).resolve()
    commit = source["commit"]
    checkout = CACHE / "sources" / f"{name}-{commit[:12]}"
    if not (checkout / ".git").exists():
        checkout.mkdir(parents=True, exist_ok=True)
        _git(checkout, "init", "-q")
        _git(checkout, "fetch", "-q", "--depth", "1", source["repo"], commit)
        _git(checkout, "checkout", "-q", "FETCH_HEAD")
    if _git(checkout, "rev-parse", "HEAD") != commit:
        raise GenerationError(f"{checkout} is not at {commit}; remove it and run again")
    return checkout / source["tasks"]


def load_cases(cases_file: Path, bundle: dict) -> list[tuple[TeamTask, TeamTask | None]]:
    """Each case's team task with its LLM-judge twin when the source publishes one."""
    doc = tomllib.loads(cases_file.read_text())
    folders = {name: source_folder(name, src, cases_file) for name, src in doc.get("sources", {}).items()}
    cases = []
    for row in doc.get("cases", []):
        if row["source"] not in folders:
            raise GenerationError(f"case {row['task']!r} names no source in {cases_file}")
        folder = folders[row["source"]] / row["task"]
        twin = folder.with_name(folder.name + JUDGE_SUFFIX)
        cases.append((read_team_task(folder, bundle),
                      read_team_task(twin, bundle) if twin.is_dir() else None))  # fmt: skip
    names = [team.name for team, _ in cases]
    if len(set(names)) != len(names):
        raise GenerationError(f"{cases_file} names a case twice")
    return cases


# --- Open Code Review itself ---------------------------------------------------------------------


def ocr_commit(bundle: dict) -> str:
    """The commit the agent image builds `ocr` from: the newest one that touched its sources."""
    paths = bundle["ocr"]["paths"]
    if _git(REPO, "status", "--porcelain", "--", *paths):
        raise GenerationError("commit the changes under " + ", ".join(paths) + " first: tasks pin a commit")
    return _git(REPO, "log", "-1", "--format=%H", "--", *paths)


def ocr_binary(given: str | None, commit: str) -> Path:
    """An `ocr` built from this checkout, for counting each case's reviewable files."""
    if given:
        return Path(given)
    binary = CACHE / "bin" / f"ocr-{commit[:12]}"
    if not binary.exists():
        binary.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/opencodereview"], cwd=REPO, check=True,
                       env={**os.environ, "CGO_ENABLED": "0"})  # fmt: skip
    return binary


def reviewable_files(ocr: Path, team: TeamTask) -> list[str]:
    """The files `ocr delegate preview` lists for the case under the eval's rule file."""
    home = CACHE / "empty-home"
    home.mkdir(parents=True, exist_ok=True)
    done = subprocess.run(
        [str(ocr), "delegate", "preview", "--format", "json", "--from", team.base, "--to", team.head,
         "--repo", str(team.snapshot), "--rule", str(RULE)],
        check=True, capture_output=True, text=True,
        env={**os.environ, "HOME": str(home), "USERPROFILE": str(home)},
    )  # fmt: skip
    return [f["path"] for f in json.loads(done.stdout)["reviewable_files"]]


# --- the orchestrator's messages and commands ----------------------------------------------------


def _section_block(skill: str, heading: str) -> str:
    """The first code block under one heading of the orchestrator skill."""
    start = skill.index(heading)
    match = _BLOCK.search(skill, start)
    following = skill.find("\n## ", start + len(heading))
    if not match or (following != -1 and match.start() > following):
        raise GenerationError(f"the orchestrator skill has no code block under {heading!r}")
    return match.group(1)


def orchestrator_blocks(skill: str) -> dict[str, str]:
    """The three messages the orchestrator sends and the three commands it runs, by step."""
    headings = {"request": "## Step 1", "grouper": "## Step 2", "apply": "## Step 3",
                "reviewer": "## Step 4", "merge": "## Step 5", "report": "## Step 6"}  # fmt: skip
    return {key: _section_block(skill, heading) for key, heading in headings.items()}


@dataclass
class Layout:
    """Where things are inside the agent image, from the team task and the bundle."""

    workdir: str
    out: str
    repo: str
    skills_dir: str
    eval_dir: str
    user: str

    @property
    def rule_flags(self) -> str:
        """The flags every `ocr delegate` call in a task carries."""
        return f"--repo {self.repo} --rule {self.eval_dir}/{RULE.name}"


def layout(team: TeamTask, bundle: dict) -> Layout:
    """The image paths a task's files name."""
    env, agent = team.config["environment"], bundle["agent"]
    workdir = env["workdir"]
    return Layout(workdir=workdir, out=f"{workdir}/{agent['review_dir']}", repo=f"{workdir}/{agent['repo_dir']}",
                  skills_dir=env["skills_dir"], eval_dir=agent["eval_dir"], user=team.config["agent"]["user"])  # fmt: skip


def fill(block: str, team: TeamTask, where: Layout, group: str | None = None) -> str:
    """One of the orchestrator's blocks with its placeholders filled in for a task."""
    text = block.replace("in the current directory", f"in {where.repo}")
    values = {"<refs>": f"--from {team.base} --to {team.head}", "<rule flags>": where.rule_flags,
              "<out>": where.out, "<task text>": " ".join(team.intent.split())}  # fmt: skip
    if group is not None:
        values["<n>"] = group
    for token, value in values.items():
        text = text.replace(token, value)
    if left := _PLACEHOLDER.findall(text):
        raise GenerationError(f"a block of the orchestrator skill has unfilled placeholders: {left}")
    return text


def skills_line(where: Layout) -> str:
    """The instruction line naming where the skills are installed."""
    return f"The skills are installed in: {where.skills_dir}"


def group_name(index: int) -> str:
    """A group step's two-digit number, as `ocr delegate group` names its slices."""
    return f"{index:02d}"


# --- the task's files ----------------------------------------------------------------------------


def _script(canary: str, about: str, body: str) -> str:
    """A shell script with the task's canary and what it is for."""
    comment = "\n".join("# " + line for line in about.splitlines())
    return f"#!/bin/bash\n# harbor-canary GUID {canary}\n{comment}\n{body}"


def grouping_setup(team: TeamTask, where: Layout, blocks: dict) -> str:
    """The grouping step's setup.sh: the orchestrator's step 1."""
    return _script(team.canary, f"""The orchestrator's step 1: write Open Code Review's grouping request. It exits {SETUP_EXIT_NO_AGENT_WORK} (no
agent work) only when OCR partitioned the change itself; any failure stops the trial.""", f"""set -euo pipefail
rm -f -- "$0"
cd {where.repo}
rm -f {where.out}/grouping.json
{fill(blocks["request"], team, where)}
if [ -f {where.out}/groups.json ]; then
  exit {SETUP_EXIT_NO_AGENT_WORK}
fi
test -f {where.out}/grouping-request.md
""")  # fmt: skip


def group_setup(team: TeamTask, where: Layout, blocks: dict, index: int) -> str:
    """A group step's setup.sh: the first applies the answer (step 3); each checks its group exists."""
    name = group_name(index)
    apply = f"cd {where.repo}\n{fill(blocks['apply'], team, where)}\n" if index == 1 else ""
    first = "The orchestrator's step 3 (turn the grouping answer into groups), then this step's check.\nIt" if index == 1 else "This step"
    return _script(team.canary, f"""{first} exits {SETUP_EXIT_NO_AGENT_WORK} (no agent work) only when the groups were written and none of them
is group {name}. Any failure stops the trial.""", f"""set -euo pipefail
rm -f -- "$0"
{apply}rm -f {where.out}/comments.{name}.json
python3 -I - <<'PY'
import json, sys
groups = json.load(open("{where.out}/groups.json", encoding="utf-8"))["groups"]
sys.exit(0 if any(g["id"] == {index} for g in groups) else {SETUP_EXIT_NO_AGENT_WORK})
PY
""")  # fmt: skip


def report_setup(team: TeamTask, where: Layout, blocks: dict) -> str:
    """The report step's setup.sh: the orchestrator's step 5, on a folder with no earlier report."""
    return _script(team.canary, """The orchestrator's step 5: merge the reviewers' comments. A report already in the folder was
not written by the report step, so it is removed first. A failure stops the trial.""", f"""set -euo pipefail
rm -f -- "$0"
rm -f {where.out}/report.json
{fill(blocks["merge"], team, where)}
""")  # fmt: skip


def _check(canary: str, about: str, python: str) -> str:
    """A step's shared check: Python run as root in the agent's container, writing the reward."""
    return _script(canary, about + """
It runs as root in the agent's container (shared mode): -I keeps the working directory and
PYTHON* variables out of Python's import path, so a planted module never runs.""", f"""mkdir -p /logs/verifier
cd /
python3 -I - <<'PY'
import json, os
{python}json.dump(reward, open("/logs/verifier/reward.json", "w"))
PY
""")  # fmt: skip


def grouping_check(team: TeamTask, where: Layout) -> str:
    """Did the grouping agent write a JSON array, or did OCR need none."""
    return _check(team.canary, """Reward 1.0 when the grouping agent wrote a JSON array, or when Open Code Review grouped the
change itself and no agent ran; else 0.0. The array's content is judged by `ocr delegate group`.""", f'''out, reward = "{where.out}", {{"reward": 0.0, "skipped": 0.0}}
try:
    if os.path.isfile(out + "/groups.json") and not os.path.exists(out + "/grouping-request.md"):
        reward = {{"reward": 1.0, "skipped": 1.0}}
    else:
        text = open(out + "/grouping.json", encoding="utf-8").read(5_000_000).strip()
        if text.startswith("```"):
            text = "\\n".join(text.split("\\n")[1:]).rsplit("```", 1)[0]
        if isinstance(json.loads(text), list):
            reward["reward"] = 1.0
except (OSError, ValueError):
    pass
''')  # fmt: skip


def group_check(team: TeamTask, where: Layout, index: int) -> str:
    """Did this group's reviewer write a comments document, or is there no such group."""
    return _check(team.canary, f"""Reward 1.0 when group {group_name(index)}'s reviewer wrote a comments document, or when the grouping made
fewer than {index} groups and no agent ran; else 0.0. Never the answer key.""", f'''out, reward = "{where.out}", {{"reward": 0.0, "skipped": 0.0, "comments": 0.0}}
try:
    groups = json.load(open(out + "/groups.json", encoding="utf-8"))["groups"]
    path = out + "/comments.{group_name(index)}.json"
    if not any(g["id"] == {index} for g in groups):
        reward.update(reward=1.0, skipped=1.0)
    elif os.path.isfile(path) and not os.path.islink(path) and os.path.getsize(path) <= 5_000_000:
        doc = json.load(open(path, encoding="utf-8"))
        if isinstance(doc, dict) and isinstance(doc.get("comments"), list):
            reward.update(reward=1.0, comments=float(len(doc["comments"])))
except (OSError, ValueError, KeyError, TypeError):
    pass
''')  # fmt: skip


def _solve(canary: str, about: str, python: str) -> str:
    """An oracle solve.sh: Python that writes the step's file."""
    return _script(canary, about, f"set -euo pipefail\npython3 -I - <<'PY'\n{python}PY\n")


def grouping_solve(team: TeamTask, where: Layout) -> str:
    """The oracle's grouping: every file in one group, which OCR's guards then split."""
    return _solve(team.canary, "Oracle: one group of every file; `ocr delegate group` splits it by its own guards.", f'''import json, re
request = open("{where.out}/grouping-request.md", encoding="utf-8").read()
files = [int(n) for n in re.findall(r"^\\[(\\d+)\\] ", request, re.M)]
json.dump([{{"label": "every changed file", "files": files}}], open("{where.out}/grouping.json", "w"))
''')  # fmt: skip


def group_solve(team: TeamTask, where: Layout, index: int) -> str:
    """The oracle's reviewer: the answer-key issues that sit in this group's files."""
    name = group_name(index)
    return _solve(team.canary, "Oracle: this group's reviewer writes the known issues that sit in its own files.", f'''import json
mine = {{f["path"] for f in json.load(open("{where.out}/groups/{name}.json", encoding="utf-8"))["files"]}}
known = json.load(open("/solution/oracle-comments.json", encoding="utf-8"))
json.dump({{"comments": [c for c in known if c["path"] in mine]}}, open("{where.out}/comments.{name}.json", "w"))
''')  # fmt: skip


def report_solve(team: TeamTask, where: Layout) -> str:
    """The oracle's report: every merged comment kept."""
    return _solve(team.canary, "Oracle: keep every comment the reviewers raised.", f'''import json
merged = json.load(open("{where.out}/merged.json", encoding="utf-8"))
kept = [{{k: v for k, v in c.items() if k != "group"}} for c in merged["comments"]]
summary = {{"total_files": merged["total_files"], "reviewed_files": merged["reviewed_files"],
           "skipped_files": merged["total_files"] - merged["reviewed_files"],
           "coverage_rate": merged["reviewed_files"] / max(merged["total_files"], 1)}}
json.dump({{"comments": kept, "summary": summary}}, open("{where.out}/report.json", "w"))
''')  # fmt: skip


def dockerfile(team: TeamTask, where: Layout, bundle: dict, commit: str) -> str:
    """The agent image: the team task's own, with `ocr` built in and Open Code Review's skills."""
    source = (team.folder / "environment" / "Dockerfile").read_text()
    lines = source.splitlines()
    try:
        first = next(i for i, line in enumerate(lines) if line.startswith("FROM "))
        add = next(i for i, line in enumerate(lines) if line.startswith("ADD "))
    except StopIteration as exc:
        raise GenerationError(f"{team.folder}: its Dockerfile has no FROM or ADD line") from exc
    theirs = "\n".join(lines[first:add])
    return f"""# harbor-canary GUID {team.canary}
# The agent's container: the change as a git repository (base and head commits), Open Code
# Review's `ocr` built from one commit of {bundle["ocr"]["repo"]}, and its
# skills. The stages below the first are the code-review team task's own image.
FROM {bundle["images"]["golang"]} AS ocr
ENV GOTOOLCHAIN=local CGO_ENABLED=0
RUN git init -q /src && cd /src \\
 && git fetch -q --depth 1 {bundle["ocr"]["repo"]} {commit} \\
 && git checkout -q FETCH_HEAD \\
 && go build -trimpath -ldflags "-s -w -X main.Version={commit[:12]}" -o /out/ocr ./cmd/opencodereview

{theirs}
COPY --from=ocr /out/ocr /usr/local/bin/ocr
ADD {bundle["agent"]["repo_dir"]}.tar.gz {where.workdir}/
# The archive carries no git index (it would hold build times); rebuild it from HEAD.
RUN git -C {where.repo} read-tree HEAD \\
 && mkdir -p {where.out} && chown -R {where.user}:{where.user} {where.workdir} \\
 && ocr --version
COPY skills {where.skills_dir}
COPY {COLLECT.name} {RULE.name} {where.eval_dir}/
RUN chmod -R a+rX,go-w {where.skills_dir} {where.eval_dir}
WORKDIR {where.workdir}
"""


def _value(value) -> str:
    """A TOML value: a string, a number, a boolean or a list of those."""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False)
    if isinstance(value, (int, float)):
        return repr(value)
    if isinstance(value, list):
        return "[" + ", ".join(_value(v) for v in value) + "]"
    raise GenerationError(f"cannot write {value!r} to task.toml")


def _key(key: str) -> str:
    """A TOML key, quoted when it is not a bare one."""
    return key if _BARE_KEY.fullmatch(key) else json.dumps(key)


def _table(name: str, table: dict) -> str:
    """A TOML table and the tables inside it, scalars first."""
    scalars = {k: v for k, v in table.items() if not isinstance(v, dict)}
    out = f"[{name}]\n" + "".join(f"{_key(k)} = {_value(v)}\n" for k, v in scalars.items())
    for k, v in table.items():
        if isinstance(v, dict):
            out += "\n" + _table(f"{name}.{_key(k)}", v)
    return out


def _unique(items: list[str]) -> list[str]:
    """The items in order, each once."""
    return list(dict.fromkeys(items))


def task_toml(team: TeamTask, where: Layout, bundle: dict, commit: str, groups: int, name: str) -> str:
    """The task's manifest: the team task's settings with Open Code Review's steps."""
    cfg = team.config
    meta = {k: v for k, v in cfg["metadata"].items() if k != "ge"}
    meta.update(mode="orchestrated-delegate", team_task=cfg["task"]["name"], group_steps=groups, ocr_commit=commit)
    ge = json.loads(json.dumps(cfg["metadata"].get("ge", {})))
    if "build_network" in ge:
        ge["build_network"]["allowed_hosts"] = _unique(ge["build_network"]["allowed_hosts"] + bundle["agent"]["build_hosts"])
    golang = bundle["images"]["golang"]
    if "source_images" in ge:
        image, digest = golang.split("@")
        ge["source_images"][golang] = f"docker.io/library/{image.split(':')[0]}@{digest}"
    grader = next(s for s in cfg["steps"] if s["name"] == team.validator)["verifier"]
    collect = [{**hook, "command": f"python3 {where.eval_dir}/{COLLECT.name} --out {where.out} --artifacts /logs/artifacts "
                                   f"--max-bytes {bundle['agent']['max_report_bytes']}"}
               for hook in grader.get("collect", [])]  # fmt: skip
    if len(collect) != 1:
        raise GenerationError(f"{team.folder}: expected one collect hook on its graded step")
    step_agent = next(s for s in cfg["steps"] if s["name"] == team.validator).get("agent", cfg["agent"])
    step = lambda step_name: f'[[steps]]\nname = "{step_name}"\n{_table("steps.agent", step_agent)}'  # noqa: E731
    keywords = _unique(["code-review", "open-code-review"] + [k for k in cfg["task"].get("keywords", []) if k != "code-review"])
    parts = [
        f"""# harbor-canary GUID {team.canary}
schema_version = {_value(cfg["schema_version"])}
# Steps run in order in one container, each a fresh agent session: the grouping agent, one
# reviewer per group it made, then the report step, whose score is the trial's. A change of
# {groups} files can have at most {groups} groups, so {groups} reviewer steps are declared; the ones past the last
# group start no agent.
multi_step_reward_strategy = "final"
""",
        _table("task", {"name": name, "version": cfg["task"]["version"],
                        "description": "Open Code Review reviews one change of a real project: a grouping agent, "
                                       "one reviewer per file group, then the report step.",
                        "keywords": keywords}),  # fmt: skip
        _table("metadata", {**meta, "ge": ge} if ge else meta),
        _table("agent", cfg["agent"]),
        """# The grouping and reviewer steps check only that their file was written, in the agent's
# container; there is no task-level tests/ folder, so nothing secret is ever uploaded there.
""" + _table("verifier", cfg["verifier"]),
        _table("environment", cfg["environment"]),
        step(GROUPING),
        *[step(f"group-{group_name(i)}") for i in range(1, groups + 1)],
        step(REPORT) + f"""
# Harbor builds this step's grader from steps/{REPORT}/tests/ only, the team task's own grader,
# so the answer key never exists in the agent's container. The run picks the judge.
""" + _table("steps.verifier", {k: v for k, v in grader.items() if k != "collect"}) + f"""
# The report in the grader's format, written to the artifacts folder as the agent user. No
# report leaves no findings file: a 0.0, not an error.
[[steps.verifier.collect]]
""" + "".join(f"{_key(k)} = {_value(v)}\n" for k, v in collect[0].items()),
    ]  # fmt: skip
    return "\n".join(parts)


def overview(team: TeamTask, where: Layout, groups: int) -> str:
    """The task's top-level instruction.md."""
    return f"""# Open Code Review reviews a change before it is merged

This is a multi-step task. Each agent reads only its own step's `instruction.md`.

The git repository at `{where.repo}` holds the change: `{team.base}` (base) and
`{team.head}` (head). What the change is meant to do:

{team.intent}

Step 1 is the grouping agent: it answers the grouping request Open Code Review sends its model.
Steps 2 to {groups + 1} are the reviewers, one per group. The change has {groups} reviewable files, so it can
have at most {groups} groups; a reviewer step past the last group starts no agent. Step {groups + 2} writes the
report. Each agent is started with the message the `open-code-review-orchestrator` skill sends
it. The report is graded against the change's known issues.
"""


def readme(team: TeamTask, bundle: dict, commit: str, groups: int, name: str) -> str:
    """The task's README.md."""
    meta = team.config["metadata"]
    return f"""# {name}

<!-- harbor-canary GUID {team.canary} -->

Open Code Review reviews one change of a real project ({meta.get("source", "?")}) as a multi-step task.
Generated by `evals/generator/generate.py` from the code-review team task
`{team.config["task"]["name"]}`; never edit it by hand.

- **Step 1, `{GROUPING}`**: `setup.sh` runs `ocr delegate group`, which writes the grouping request
  `ocr review` sends its model; the agent answers it. When OCR partitions the change itself
  (under 4 files), the step has no agent work.
- **Steps 2 to {groups + 1}, `group-01` to `group-{group_name(groups)}`**: `group-01`'s `setup.sh` turns the answer into
  groups with OCR's own guards (an unusable answer gives every file its own group). Each group's
  reviewer is a fresh agent session using the `open-code-review-delegate` skill on its files,
  writing `comments.<NN>.json`. A step past the last group starts no agent.
- **Step {groups + 2}, `{REPORT}`**: `setup.sh` runs `ocr delegate merge`, the agent checks the merged
  comments and writes `report.json`, and the collect hook hands it to the grader. The grader
  (`steps/{REPORT}/tests/`) is the team task's, byte for byte: {meta.get("golden_issues", "?")} known issues
  ({meta.get("golden", "?")}); the trial reward is this step's (`final`).
- **OCR**: built in the agent image from {bundle["ocr"]["repo"]}
  at `{commit}`. Its skills are at `{team.config["environment"]["skills_dir"]}`.
- **Traces**: one trajectory per step that ran an agent, under `steps/<name>/agent/` in the trial.

    harbor run -y -p <task> -a oracle
    harbor run -y -p <task> -a claude-code -m <model>

The run picks the judge: `REWARDKIT_JUDGE` names a Reward Kit backend (agy, claude-code, codex) or
a LiteLLM model id, `REWARDKIT_MODEL` its model. Pick one from another family than the reviewers.
"""


def _write(path: Path, text: str, executable: bool = False) -> None:
    """Write a text file, creating its folder."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    path.chmod(0o755 if executable else 0o644)


def _write_step(folder: Path, instruction: str, setup: str, solve: str, check: str | None) -> None:
    """One step's files: its instruction, setup script, oracle and, when shared, its check."""
    _write(folder / "instruction.md", instruction)
    _write(folder / "workdir" / "setup.sh", setup, executable=True)
    _write(folder / "solution" / "solve.sh", solve, executable=True)
    if check is not None:
        _write(folder / "tests" / "test.sh", check, executable=True)


def write_task(team: TeamTask, out: Path, bundle: dict, commit: str, files: list[str], blocks: dict) -> Path:
    """Write one task folder from a team task; returns the folder."""
    where = layout(team, bundle)
    stray = sorted({c["path"] for c in team.oracle} - set(files))
    if stray:
        raise GenerationError(f"{team.name}: known issues sit in files Open Code Review does not review: {stray}")
    groups = len(files)
    name = f"{bundle['dataset']}/{team.name}"
    task = out / team.name
    env = task / "environment"
    env.mkdir(parents=True)
    archive = bundle["agent"]["repo_dir"] + ".tar.gz"
    shutil.copyfile(team.folder / "environment" / archive, env / archive)
    _write(env / "Dockerfile", dockerfile(team, where, bundle, commit))
    shutil.copyfile(COLLECT, env / COLLECT.name)
    shutil.copyfile(RULE, env / RULE.name)
    for skill in bundle["ocr"]["skills"]:
        shutil.copytree(SKILLS / skill, env / "skills" / skill)

    steps = task / "steps"
    _write_step(steps / GROUPING, fill(blocks["grouper"], team, where) + "\n",
                grouping_setup(team, where, blocks), grouping_solve(team, where), grouping_check(team, where))  # fmt: skip
    oracle = json.dumps(team.oracle, indent=2) + "\n"
    for index in range(1, groups + 1):
        folder = steps / f"group-{group_name(index)}"
        message = fill(blocks["reviewer"], team, where, group_name(index))
        _write_step(folder, f"{skills_line(where)}\n{message}\n", group_setup(team, where, blocks, index),
                    group_solve(team, where, index), group_check(team, where, index))  # fmt: skip
        _write(folder / "solution" / "oracle-comments.json", oracle)
    _write_step(steps / REPORT, f"{skills_line(where)}\n{fill(blocks['report'], team, where)}\n",
                report_setup(team, where, blocks), report_solve(team, where), None)  # fmt: skip
    shutil.copytree(team.tests, steps / REPORT / "tests")

    _write(task / "instruction.md", overview(team, where, groups))
    _write(task / "README.md", readme(team, bundle, commit, groups, name))
    _write(task / "task.toml", task_toml(team, where, bundle, commit, groups, name))
    return task


def generate(cases_file: Path, out: Path, ocr: str | None) -> list[Path]:
    """Write every case's task (and its LLM-judge twin) under out, replacing what was there."""
    bundle = tomllib.loads(BUNDLE.read_text())
    blocks = orchestrator_blocks(ORCHESTRATOR.read_text())
    commit = ocr_commit(bundle)
    binary = ocr_binary(ocr, commit)
    cases = load_cases(cases_file, bundle)
    if out.exists():
        for child in out.iterdir():
            shutil.rmtree(child) if child.is_dir() else child.unlink()
    out.mkdir(parents=True, exist_ok=True)
    written = []
    for team, twin in cases:
        files = reviewable_files(binary, team)
        written.append(write_task(team, out, bundle, commit, files, blocks))
        if twin is not None:
            if (twin.base, twin.head, twin.oracle) != (team.base, team.head, team.oracle):
                raise GenerationError(f"{twin.name} is not the same change and answer key as {team.name}")
            written.append(write_task(twin, out, bundle, commit, files, blocks))
    return written


def pipeline_digest() -> str:
    """A digest that moves when anything a task is generated from in this repository moves."""
    bundle = tomllib.loads(BUNDLE.read_text())
    inputs = [Path(__file__), COLLECT, BUNDLE, RULE, CASES]
    for skill in bundle["ocr"]["skills"]:
        inputs += sorted(p for p in (SKILLS / skill).rglob("*") if p.is_file())
    digest = hashlib.sha256(ocr_commit(bundle).encode())
    for path in inputs:
        digest.update(path.relative_to(REPO).as_posix().encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def differences(a: Path, b: Path) -> list[str]:
    """Every path that differs between two folders, by content or by its executable bit."""
    found: list[str] = []

    def walk(cmp: filecmp.dircmp, prefix: str) -> None:
        found.extend(prefix + n for n in cmp.left_only + cmp.right_only + cmp.funny_files)
        _, mismatch, errors = filecmp.cmpfiles(cmp.left, cmp.right, cmp.common_files, shallow=False)
        found.extend(prefix + n for n in mismatch + errors)
        for n in cmp.common_files:
            if os.access(Path(cmp.left, n), os.X_OK) != os.access(Path(cmp.right, n), os.X_OK):
                found.append(prefix + n + " (mode)")
        for n, sub in cmp.subdirs.items():
            walk(sub, f"{prefix}{n}/")

    walk(filecmp.dircmp(a, b), "")
    return sorted(set(found))


def main(argv: list[str] | None = None) -> int:
    """Generate the tasks, or with --check compare the committed ones with a fresh run."""
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--cases", type=Path, default=CASES, help="the cases file (default: evals/cases.toml)")
    p.add_argument("--out", type=Path, default=OUT, help="the tasks folder (default: evals/tasks)")
    p.add_argument("--ocr", default=os.environ.get("OCR_BIN"), help="an ocr binary built from this checkout")
    p.add_argument("--check", action="store_true", help="fail when --out differs from a fresh run")
    p.add_argument("--digest", action="store_true", help="print the pipeline digest and exit")
    args = p.parse_args(argv)
    try:
        if args.digest:
            print(pipeline_digest())
            return 0
        if not args.check:
            for task in generate(args.cases, args.out, args.ocr):
                print(f"wrote {task}")
            return 0
        CACHE.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
            generate(args.cases, Path(tmp) / "tasks", args.ocr)
            diff = differences(args.out, Path(tmp) / "tasks")
    except (GenerationError, subprocess.CalledProcessError, OSError, KeyError) as exc:
        print(f"generate: {exc}", file=sys.stderr)
        return 1
    for path in diff:
        print(f"differs: {path}", file=sys.stderr)
    print(f"{args.out}: {'matches a fresh run' if not diff else f'{len(diff)} path(s) differ from a fresh run'}")
    return 1 if diff else 0


if __name__ == "__main__":
    sys.exit(main())
