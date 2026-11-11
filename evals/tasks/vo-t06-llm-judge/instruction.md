# Open Code Review reviews a change before it is merged

This is a multi-step task. Each agent reads only its own step's `instruction.md`.

The git repository at `/workspace/repo` holds the change: `d47ca8270bc57385662f40f0c12f83f92b572c96` (base) and
`d9677189b6880ce609fd32f9132b8fc965fac999` (head). What the change is meant to do:

The change adds region capture to screen recording. After the user picks a screen, a selection overlay shows a captured frame and lets them draw a rectangle, and the recorder crops the stream to that rectangle. It also maps the capture mode to the picker's surface hint, minimizes and restores the app window around the picker, waits for the first video frame before compositing, and sets a dark background color on the window and page.

Step 1 is the grouping agent: it answers the grouping request Open Code Review sends its model.
Steps 2 to 11 are the reviewers, one per group. The change has 10 reviewable files, so it can
have at most 10 groups; a reviewer step past the last group starts no agent. Step 12 writes the
report. Each agent is started with the message the `open-code-review-orchestrator` skill sends
it. The report is graded against the change's known issues.
