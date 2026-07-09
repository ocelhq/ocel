# TASK

You are running **on the host** at the end of one orchestrator wave. The branches below
were just implemented in parallel sandboxes; their bd issues are already closed and their
commits are on the named branches. Your job is to get each branch onto its Graphite stack
and submitted as a PR, handling whatever rebasing/restacking that takes.

You have `gt` (Graphite), `git`, `gh`, and `bd` available and authenticated. Pass
`--no-interactive` to every `gt` command.

## Branches to submit

Process them **in the order listed** (a later branch may be stacked on an earlier one):

{{BRANCHES}}

# STEPS

For each branch, in order:

1. Track it onto its parent: `gt track <BRANCH> --parent <PARENT_BRANCH> --no-interactive`
   (idempotent — re-tracking just updates the parent).
2. Submit it: `gt submit --branch <BRANCH> --no-edit --publish --no-interactive`. This also
   submits any untracked ancestors and prints one `<branch>: <url>` line per branch touched.

If a submit fails because the branch is out of date / needs restacking first, run
`gt sync --force --no-interactive` and/or `gt restack --no-interactive` (or a plain
`git rebase`) to bring it up to date, then retry the submit. Use your judgement to work
around transient or ordering errors — that is the point of doing this here rather than a
fixed script.

# WHEN A BRANCH CANNOT BE SUBMITTED

Do **not** resolve genuine merge conflicts by guessing. If, after reasonable retries, a
branch still cannot be submitted cleanly, reopen its issue so a human picks it up and this
run's future waves skip it:

```
bd update <ISSUE_ID> --status=open --assignee= --add-label orchestrator-failed
```

Then move on to the remaining branches — one bad branch must not block the others.

# FINAL OUTPUT

End your response with a single line of JSON summarizing what happened, so the host can log
it:

```
{"results":[{"id":"<ISSUE_ID>","status":"submitted","pr":"<url>"},{"id":"<ISSUE_ID>","status":"failed","reason":"<short reason>"}]}
```

Use `"status":"submitted"` with the PR url for each branch that landed a PR, and
`"status":"failed"` with a short reason for each you reopened.
