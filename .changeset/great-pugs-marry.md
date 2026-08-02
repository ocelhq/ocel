---
"ocel": minor
---

New subpath export `ocel/env/client`, the browser half of `ocel/env`. It exports
`clientEnv` and `EnvClientError`; inside an app build the specifier resolves to
the accessor generated at `<app>/.ocel/env-client.ts`, and until one is
generated a read throws `EnvClientError` rather than yielding `undefined`.
`EnvClientError` is also re-exported from `ocel/env` on both the node and edge
builds.

A variable is delivered under the name you declared it with, and the accessor
reads it under that name. Which names reach a browser bundle is your bundler's
rule, not Ocel's, so you satisfy it by naming the variable — declare
`NEXT_PUBLIC_APP_ID` under Next, `VITE_APP_ID` under Vite, and read it as
`clientEnv.NEXT_PUBLIC_APP_ID`. Ocel adds no prefix and strips none, which is
what keeps a second bundler from being a change to Ocel.

The cost of holding no opinion is that Ocel cannot know in advance which names
your bundler will pass over, so the accessor refuses to load when a value it
names never arrived, saying which key and why. The value is exported to your
build under its own name, so a server render still finds it; a name your bundler
does not inline fails in the browser instead, as a loud error naming the key at
module load rather than a value that quietly reads as `undefined` later on.

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
