# Open Code Review reviews a change before it is merged

This is a multi-step task. Each agent reads only its own step's `instruction.md`.

The git repository at `/workspace/repo` holds the change: `f280a86d34b0121cfd84e0815d6fdafa61f10337` (base) and
`c06bf86bd436dda60bee4c2b69f88f21b39f1ba6` (head). What the change is meant to do:

The change adds a Models page for managing local speech models and lets users choose which transcription model the voice pipeline uses. The sidecar gains a registry of named models with categories and recommendations, per-model download status, a delete endpoint, and a transcription model selectable per request. The settings and create-voice screens check only the models needed for the current local mode, and a new config field stores the chosen transcription model.

Step 1 is the grouping agent: it answers the grouping request Open Code Review sends its model.
Steps 2 to 19 are the reviewers, one per group. The change has 18 reviewable files, so it can
have at most 18 groups; a reviewer step past the last group starts no agent. Step 20 writes the
report. Each agent is started with the message the `open-code-review-orchestrator` skill sends
it. The report is graded against the change's known issues.
