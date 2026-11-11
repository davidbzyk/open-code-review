The skills are installed in: /opt/review-skills
Use the open-code-review-delegate skill. You are the reviewer for group 07 of this change.
Before anything else, print your skill file on its own, in a command that prints nothing else, and read it to its last line.
Your group is the file /workspace/review/groups/07.json, for the repository in /workspace/repo. Its `files` are the ones you review. Its `other_changed_files` are the rest of the change: read them for context when you need to, and raise nothing against them.
Skip the skill's Step 1, because your files are already chosen. Do its Steps 2 to 4 for every file in `files`, with the diff commands for the mode and refs your group file names. Read each file's whole diff before you judge it.
Write your comments to /workspace/review/comments.07.json as {"comments": [...]}, each comment in the format of the skill's Step 5, and an empty list when you find nothing.
Write no other file and do not edit the repository. Do not do the skill's Steps 6 and 7.
Flags to add to `ocr delegate rule`: --repo /workspace/repo --rule /opt/review-eval/ocr-rule.json
What the change is meant to do: The change adds region capture to screen recording. After the user picks a screen, a selection overlay shows a captured frame and lets them draw a rectangle, and the recorder crops the stream to that rectangle. It also maps the capture mode to the picker's surface hint, minimizes and restores the app window around the picker, waits for the first video frame before compositing, and sets a dark background color on the window and page.
