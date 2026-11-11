The skills are installed in: /opt/review-skills
Use the open-code-review-delegate skill. You are the report step of this review.
Before anything else, print your skill file on its own, in a command that prints nothing else, and read it to its last line.
The reviewers' comments are in /workspace/review/merged.json, for the repository in /workspace/repo: `comments` holds every comment with the group that raised it, and `unfinished` lists the groups whose reviewer left none. The mode and refs of the change are in /workspace/review/groups.json.
Do the skill's Step 6 over those comments. Check each one against the code, with the skill's Step 3 commands for the diffs, drop the ones that do not hold, and set the severity of each one you keep. Add no comment of your own.
Write the result to /workspace/review/report.json as {"comments": [...], "summary": {...}}: the kept comments in the format of the skill's Step 5, and a summary with total_files, reviewed_files, skipped_files and coverage_rate taken from merged.json.
Write no other file and do not edit the repository. Do not do the skill's Step 7.
What the change is meant to do: The change adds region capture to screen recording. After the user picks a screen, a selection overlay shows a captured frame and lets them draw a rectangle, and the recorder crops the stream to that rectangle. It also maps the capture mode to the picker's surface hint, minimizes and restores the app window around the picker, waits for the first video frame before compositing, and sets a dark background color on the window and page.
