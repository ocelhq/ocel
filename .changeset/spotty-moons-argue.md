---
"ocel": minor
---

`ocel dev` now resolves variables from a `.env` file at the project root, so
getting a project running takes no cloud account and no `ocel env set`. The file
is `KEY=VALUE`, `#` comments, optional quotes — single, double or backtick — and
nothing else: no layering, no `$VAR` interpolation.

`.env` is the file your framework already reads, so Ocel adopted it rather than
claimed it: a line whose key Ocel could never be asked for — anything under
`OCEL_`, `AWS_`, `LAMBDA_` or `NEXT_PUBLIC_`, or a name outside
`^[A-Z_][A-Z0-9_]*$` — is left to whatever else reads the file, in silence. A
key set twice takes its last value, the same answer the parser your framework
uses gives. The only thing the run says anything about is a line that assigns
nothing at all, and it says so by line number and never by content, because that
is precisely the shape a pasted token has.

Precedence for a dev run is shell < project env vars < live values < `.env` <
resource connection details. The file outranks everything a cloud account could
supply, because it is the file you edit and a feature whose premise is "no cloud
account required" collapses the moment editing it stops deciding the value. It
loses only to a resource, whose names Ocel owns and which the parser will not
let a line claim. The shell is deliberately last and does not satisfy the gate
at all: a verdict that read your shell would differ from your teammate's, which
is the failure variables exist to prevent — so a value found only there is
refused, and the refusal says it saw it.

The same gate a deploy runs now runs in dev, before the app is spawned, so a
missing or schema-failing value is a named refusal at startup rather than a
crash at the first read. Its remedy diverges from the deploy's on purpose: dev
says `add KEY=<VALUE> to .env`, because `ocel env set` needs a cloud provider
and a bootstrapped store this path exists to do without.

A scoped variable is readable in dev. `.env` is flat and has no folders in it,
so one root line stands as the value at every folder the declaration names.
That is the cost: in dev a scoped value cannot differ per app the way a deployed
one does.

`ocel run` resolves and gates the file exactly as `ocel dev` does. The two
answering differently would mean a project set up to run under one is refused
under the other, with the `ocel env set` remedy this whole path exists to do
without.

`ocel dev` and `ocel run` now state `OCEL_APP_FOLDER` on the child, always —
empty for an unbound project, so a binding left over in your shell can never
answer for the run. Without it every scoped read threw, including ones whose
values were sitting in that very environment.

Three limits are worth knowing rather than discovering:

- Dev spawns one child for the whole project and nothing tells it which of your
  apps that child is, so it states the folder every app agrees on. Where two
  apps bind different folders it can only state the project root, under which
  every scoped read refuses — so rather than start and throw at the first read,
  `ocel dev` and `ocel run` refuse up front and name the keys. Bind every app to
  the same folder, or keep those variables unscoped. Running a second `ocel dev`
  will not help: the leader election keys on the project root, so the second run
  joins the first as a follower and is handed the leader's binding.
- `.env` is not in the watch set. Editing it does not re-resolve a running dev
  server; restart `ocel dev`, the same as for a rotated live value.
- Dev's only delivery channel is the child's environment, and a framework dev
  server hands its own environment to an edge sandbox. So a bare
  `process.env.KEY` read from an edge route resolves under `ocel dev` and is
  `undefined` once deployed, and the dotfile widens the set of names that
  behave that way. Reading through `ocel/env` is unaffected: it refuses an edge
  read on both sides.

Dev's divergence notice now says all of this at the moment it happens: which
keys came from the file, that the file is yours alone and a deploy resolves none
of it, that dev hands every class to the app in plaintext under its own name
where a deploy keeps a sensitive value out of the function environment and a
live one out of the artifact, that the file is read once at startup so an edit
takes effect on the next `ocel dev`, and — when the project's `.gitignore` does
not match it — that the file is not ignored yet. Key names and line numbers
only; never a value.

Nothing about the file reaches a preview or a production deploy: a deploy
resolves its values through the provider it already has a session with, and the
dev store the parser feeds is unexported and reachable from nothing on that
path. The import graph is pinned where it can be — `envgate`, the deploy
collector and the variables UI cannot reach the parser, and a test asserts that
over `go list -deps` rather than by convention. It is not pinned for the deploy
command itself, which lives in the same package as `ocel dev` and therefore
necessarily imports it.
