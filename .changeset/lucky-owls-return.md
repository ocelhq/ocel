---
"ocel": minor
---

A variable gate refusal no longer ends `ocel deploy` or `ocel preview up`. In a
terminal, the command opens the bundled variables UI on the very gate that
stopped it — the matrix shows exactly the cells that are owed — and waits.
Fill them in, mark the matrix done, and the same command carries on into the
build. The preflight and the confirmation you already gave are not repeated,
because the wait happens after both.

Resuming re-runs discovery rather than re-reading the store. Whether a value
satisfies its schema is only knowable inside the declaring process, and writing
a cell through the UI retracts what discovery said about the value it replaced —
so a resume that only re-checked the old verdict would accept a replacement that
is invalid in a new way. The second pass costs one more discovery run and is
what makes "re-validated" true. A matrix you mark done that still does not
satisfy the gate reopens rather than ending the run.

The wait is escapable and never silent. Ctrl-C aborts with a non-zero status,
and nothing has been built or provisioned by that point — the gate stands before
both. Closing the page is not yet a signal the run can see, so press Ctrl-C to
end a wait you have walked away from.

Nothing waits without a terminal on stdin, so a CI deploy keeps the hard
refusal it has always had. `--no-ui`, or `OCEL_NO_BROWSER` in the environment,
opts out for a terminal with no browser to be handed — over SSH, say. There is
no CI sniffing: a terminal is the signal, and the flag is the answer where the
signal is wrong.

As everywhere else on this path, a refusal and the waiting state name keys,
folders and line numbers, and never a value.
