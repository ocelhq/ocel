---
"ocel": patch
---

`ocel dev` and `ocel run` now refuse a multi-app project only over a variable one
of its apps would actually have read. The check asked a wider question — any
variable with a `folders:` scope, plus any two apps bound to different folders —
so a variable scoped to a folder no app binds refused the whole run. That
variable is unreadable under every app's own binding too, which is to say a
deploy of the same project resolves nothing for it and says nothing about it, so
dev now starts and is equally silent rather than being stricter than the deploy
it stands in for.

The refusal that remains — a variable scoped to a folder one of your apps binds,
which the run cannot state because your apps do not agree on one — is unchanged
in kind, and now names only the apps bound inside that variable's scope. An app
bound elsewhere was never going to read it, and listing it made the remedy read
as though it were about that app.
