---
"ocel": patch
---

Editing `.env` now re-resolves a running `ocel dev`. The file was read once and
held for the process, so an edit changed neither the gate's verdict nor the
app's environment until the next restart — which contradicted the premise that
the file you edit is the file that decides, and said so nowhere.

The project root joins the watch set for that one path: a write to
`package.json`, a lockfile or an editor's scratch file is no reason to
re-discover. A save re-reads the file, rebuilds the store the gate rules from,
re-runs discovery and pushes the result to every follower.

A run can therefore start refusing mid-session — deleting a required value is a
named refusal on the spot rather than a surprise on the next start — and the
refusal is not terminal: putting the value back clears it without a restart.
Dev's divergence notice says this instead of telling you to restart. `ocel run`
has no watcher and keeps the old wording, which is true of it.
