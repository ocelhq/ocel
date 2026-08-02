---
"ocel": patch
---

Each app is now built under its own environment. One build process served every
app, so a plaintext key two apps resolved differently could not be expressed
there at all: it was left out of the build entirely and read as unset in
anything the build inlined, with only a warning to say so. That made a
client-accessible value that diverges across apps impossible to inline — which
is the case folders exist for.

The build request now carries each app's own values and the folder that app
binds, and the builder applies them to that app's framework build alone. The
second half matters as much as the first: a build told it was bound to the
project root — the only honest answer a shared process had once two apps bound
different folders — was refused every scoped read at build time, including
reads whose values were sitting in that same environment.

`ocel dev` still spawns one child for the whole project and states one binding
for it; per-app divergence remains inexpressible there.
