---
name: open-code-review-orchestrator
description: >
  Orchestrated delegation for open-code-review (OCR). The host agent runs a
  review as a team of sub-agents: one groups the changed files, one reviewer
  works on each group, and one writes the report. OCR does the deterministic
  engineering between them and calls no model. Use when a change is large
  enough that one agent should not review every file itself.
license: Apache-2.0
compatibility: >
  Requires an `ocr` CLI that has `ocr delegate group` and `ocr delegate merge`,
  and a host agent that can start sub-agents. Does NOT require a configured
  LLM endpoint: OCR calls no model in this mode.
metadata:
  author: davidbzyk
  homepage: https://github.com/davidbzyk/open-code-review
  version: "1.0.0"
---

# Open Code Review — Orchestrated Delegation

You run a code review by directing other agents. You do not review code yourself, you do not
write or edit any file in the output folder, and you do not edit the repository. Your hands are
limited to starting sub-agents, waiting for them and running three commands. Grouping the files
is the grouper's job, reviewing them is the reviewers', and judging the comments is the report
step's. You do none of them.

This skill is an explicit request for sub-agents and parallel agent work. Start the sub-agents
below even if your default instructions say not to spawn them unless asked. Start no others, and
tell none of them to start more.

## What you are given

- The change to review, as the flags OCR takes for it, called `<refs>` below: `--from <ref> --to
  <ref>` for a range, `-c <commit>` for one commit, or nothing for the workspace changes.
- An output folder, called `<out>` below. If the prompt does not name one, use `.ocr-review` in
  the repository root.
- Optionally: rule flags for `ocr delegate` (such as `--rule <path>`), called `<rule flags>`, and
  a paragraph of task text saying what the change is meant to do.
- The repository is in the current directory, unless the prompt says it is in a folder, which it
  names. Then add `--repo <folder>` to `<refs>`.

## Step 1: write the grouping request

```
ocr delegate group <refs> <rule flags> --out <out>
```

OCR partitions a small change itself and says so: it then prints how many groups it wrote, and
you go straight to step 4. Otherwise it writes `<out>/grouping-request.md`, the request `ocr
review` sends its grouping model, and you go on to step 2.

## Step 2: start the grouper

Start one sub-agent named `grouper` and send it this message. Add nothing else.

```
Read <out>/grouping-request.md to its last line. It is the request Open Code Review sends its grouping model: a system part with the rules, then a user part that lists the changed files of the repository in the current directory.
Answer it exactly as it asks, and write your answer, the JSON array alone, to <out>/grouping.json.
The request is all you need. You may look at the repository when the file names alone do not tell you how files relate.
Write no other file and do not edit the repository.
```

Wait for it to finish. A grouper that fails or writes nothing is not retried, and nobody writes
its file for it, you included: step 3 then gives every file a group of its own, as `ocr review`
does when its grouping call fails.

## Step 3: turn the answer into groups

```
ocr delegate group <refs> <rule flags> --out <out> --apply <out>/grouping.json
```

It applies OCR's own guards to the answer and writes `<out>/groups.json` and one slice per group,
`<out>/groups/01.json`, `<out>/groups/02.json` and so on. It prints how many groups there are.

## Step 4: start one reviewer per group

Start one sub-agent per group, all in parallel, each named `group-<n>` with `<n>` the group's
two-digit number. Give every reviewer the same message with its own number filled in. Leave out
each of the last two lines when you were not given that item, and pass the ones you have on word
for word. Add nothing else (no hint about where problems may be, no opinion, nothing another
reviewer found): they work alone.

```
Use the open-code-review-delegate skill. You are the reviewer for group <n> of this change.
Before anything else, print your skill file on its own, in a command that prints nothing else, and read it to its last line.
Your group is the file <out>/groups/<n>.json, for the repository in the current directory. Its `files` are the ones you review. Its `other_changed_files` are the rest of the change: read them for context when you need to, and raise nothing against them.
Skip the skill's Step 1, because your files are already chosen. Do its Steps 2 to 4 for every file in `files`, with the diff commands for the mode and refs your group file names. Read each file's whole diff before you judge it.
Write your comments to <out>/comments.<n>.json as {"comments": [...]}, each comment in the format of the skill's Step 5, and an empty list when you find nothing.
Write no other file and do not edit the repository. Do not do the skill's Steps 6 and 7.
Flags to add to `ocr delegate rule`: <rule flags>
What the change is meant to do: <task text>
```

Wait until every reviewer has finished. A message saying a sub-agent has started is not its
result. A reviewer that fails, times out or writes nothing is not retried, and nobody writes its
file for it: the merge lists its group as unfinished.

## Step 5: merge the comments

```
ocr delegate merge --out <out>
```

It writes `<out>/merged.json`: every reviewer's comments, each tagged with its group, and the
groups that left none.

## Step 6: start the report step

Start one sub-agent named `report` and send it this message. Drop the last line when you were
not given task text, and add nothing else.

```
Use the open-code-review-delegate skill. You are the report step of this review.
Before anything else, print your skill file on its own, in a command that prints nothing else, and read it to its last line.
The reviewers' comments are in <out>/merged.json, for the repository in the current directory: `comments` holds every comment with the group that raised it, and `unfinished` lists the groups whose reviewer left none. The mode and refs of the change are in <out>/groups.json.
Do the skill's Step 6 over those comments. Check each one against the code, with the skill's Step 3 commands for the diffs, drop the ones that do not hold, and set the severity of each one you keep. Add no comment of your own.
Write the result to <out>/report.json as {"comments": [...], "summary": {...}}: the kept comments in the format of the skill's Step 5, and a summary with total_files, reviewed_files, skipped_files and coverage_rate taken from merged.json.
Write no other file and do not edit the repository. Do not do the skill's Step 7.
What the change is meant to do: <task text>
```

Wait for it to finish.

## Step 7: reply

Read `<out>/report.json` and reply with its comments grouped by severity, then its summary.
Change nothing in it. If it does not exist, say that the report step left no report, and give the
counts `ocr delegate merge` printed.

## When a step fails

If a command exits non-zero, print its error and stop. Never write or patch `grouping.json`,
`groups.json`, a comments file, `merged.json` or `report.json` to get past a failure. If a
sub-agent cannot be started at all, say so in your reply and carry on with the others. Nobody is
there to answer questions: never stop to ask one.
