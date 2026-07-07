# TASK

Fix issue {{ISSUE_ID}}

## Issue

!`bd show {{ISSUE_ID}}`

## Parent epic/PRD

!`bd show {{PARENT_ID}}`

Only work on the issue specified above.

You are already on branch {{SOURCE_BRANCH}} in a dedicated sandbox. This sandbox's `.beads` directory is bind-mounted from the host, so any `bd` command you run here — labels, comments, closing the issue — is visible everywhere else immediately.

Make commits and run tests. Do **not** push the branch or open a PR — the orchestrator handles that from the host once your sandbox run finishes.

# EXPLORATION

Explore the repo and fill your context window with relevant information that will allow you to complete the task.

Pay extra attention to test files that touch the relevant parts of the code.

# EXECUTION

If applicable, use RGR to complete the task.

1. RED: write one test
2. GREEN: write the implementation to pass that test
3. REPEAT until done
4. REFACTOR the code

# FEEDBACK LOOPS

Before committing, run the relevant typecheck and test commands for the part of the repo you touched, and ensure they pass.

# COMMIT

Make a git commit. The commit message must:

1. Have a concise descriptive title
2. Include task completed + PRD reference
3. Key decisions made
4. Files changed
5. Blockers or notes for next iteration

Keep it concise.

# THE ISSUE

If the task is complete, run `bd close {{ISSUE_ID}} --reason="<one-line summary>"`.

If the task is not complete, run `bd comment {{ISSUE_ID}} "<what was done>"` and leave its status as in_progress.

# FINAL RULES

ONLY WORK ON A SINGLE TASK.
