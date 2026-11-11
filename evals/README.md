# Open Code Review evals

Multi-step [Harbor](https://github.com/harbor-framework/harbor) tasks that score Open Code Review
on real pull requests. Each task is one change of a real project with a known set of issues, taken
from reviewed pull requests. A judge checks how many of those issues the review states.

## What a trial runs

OCR reviews a change in three stages: it groups the changed files, a sub-agent reviews each
group, and the comments are filtered and reported. A trial runs those stages as Harbor steps, each
a fresh agent session, so every stage leaves its own trajectory and can be read on its own:

| Step | Who | What it does |
|---|---|---|
| `grouping` | the grouping agent | Answers the grouping request `ocr review` sends its model |
| `group-01` … `group-NN` | one reviewer per group | Reviews its group's files with the `open-code-review-delegate` skill |
| `report` | the report step | Checks the merged comments and writes the report that is graded |

The step instructions are the messages the
[`open-code-review-orchestrator`](../skills/open-code-review-orchestrator/SKILL.md) skill sends its
sub-agents, and the setup scripts are the `ocr delegate` commands it runs between them. The
generator lifts both from the skill, so the eval and the skill cannot say different things.

The grouping agent decides how many groups there are, during the trial. Harbor fixes a task's
steps before the trial starts, so a task declares one reviewer step per changed file, which is the
most groups any grouping can produce, and a reviewer step past the last group starts no agent.
When OCR's own thresholds say a change needs no grouping call (under 4 files), the grouping step
starts no agent either. A grouping answer that is missing or does not parse gives every file its
own group, as a failed grouping call does in `ocr review`.

Only the report step is scored. Its grader is separate from the agent's container, so the known
issues are never where an agent can read them.

## Cases

[`cases.toml`](cases.toml) has one row per case. A case names a published
[code-review team task](https://github.com/davidbzyk/code_review/tree/main/evals/team-tasks): its
repository snapshot, its known issues and its grader are reused as they are. OCR is therefore
scored by the same judge on the same criteria as that review team, and adding a case is adding a
row. A case whose team task has an `-llm-judge` twin gets one too: the same steps, with a judge
that rates the whole review on four scales instead of counting known issues.

OCR reviews every changed file here, tests and documentation included, through its `include`
rule ([`ocr-rule.json`](ocr-rule.json)); its default filter leaves those out, and some known
issues sit in them.

## Regenerating

The tasks under [`tasks/`](tasks) are generated and never edited by hand.

```bash
python3 evals/generator/generate.py           # rewrite evals/tasks
python3 evals/generator/generate.py --check   # fail when evals/tasks differs from a fresh run
python3 -m unittest discover -s evals/tests   # the generator's tests, standard library only
```

The generator needs `git` and Go on `PATH`. It builds `ocr` from this checkout to count each
case's reviewable files, and each task's image builds `ocr` from the newest commit that touched
`cmd/`, `internal/`, `go.mod` or `go.sum`. Commit and push a Go change before regenerating, since
the image fetches that commit from GitHub.

The tests include a simulation: one task's own setup scripts, checks and oracle run in Harbor's
order on this machine, with a do-nothing agent, the oracle, a three-group grouping and a planted
report.

## Running a task

```bash
harbor run -y -p evals/tasks/vo-t06 -a oracle
harbor run -y -p evals/tasks/vo-t06 -a claude-code -m <model>
```

A reviewer step past the last group tells Harbor it has no agent work by exiting its `setup.sh`
with code 78. The Harbor build Standard Envs runs reads that code and records the step without
starting an agent. Stock Harbor treats it as a failed setup, so there a trial stops at the first
reviewer step past the last group unless the grouping makes one group per file.

The run picks the judge: `REWARDKIT_JUDGE` names a Reward Kit backend (`agy`, `claude-code`,
`codex`) or a LiteLLM model id, and `REWARDKIT_MODEL` its model. Pick one from another model
family than the agent under test. On a recall task the oracle must score 1.0 and an agent that does
nothing 0.0. An `-llm-judge` twin gives the oracle a rating, not a count, so it lands below 1.0.

[`se.toml`](se.toml) is the manifest Standard Envs reads when this
repository is connected there.
