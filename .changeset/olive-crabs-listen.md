---
"ocel": minor
---

Reading a variable from an edge route or middleware now fails loudly instead of
compiling `@connectrpc/connect-node` into the edge bundle and dying on
`node:http2`. `ocel/env` ships an edge build, selected by the `edge-light`,
`workerd` and `worker` export conditions, that imports no Node builtin: an edge
entry can evaluate a definitions module the way a node entry does, and only the
read is refused — with the key, the entry the shim is running, and the remedy.
This is `ocel/env` alone: `ocel/postgres`, `ocel/blob` and `ocel/blob/next` have
no edge build yet and still fail an edge compile on `node:http2`, so an edge
entry that reaches one of those is no better off than before.

No variable class reaches the edge tier in this iteration, and none of the three
can be made to. A plaintext value is a function's environment entry, an
encrypted-baked value is ciphertext inside the function's package, and a live
value is fetched by the Go membrane over the control socket — all three end at a
Lambda, and an edge worker's only bindings are the edge reader's own
credentials. So the one remedy is to move the entry to the `nodejs` runtime;
reclassifying the variable buys nothing. Encrypting a value into the edge bundle
would buy nothing either: the bundle is a single object in an account-global
store, and the key to open it would have to travel beside it.

`ocel build` warns at build time for every edge entry that imports `ocel/env`,
naming the route. It is a warning rather than a failure because the build can
see the import and not the read: an edge route importing a barrel that
re-exports `env` without ever reading one is legitimate.

Ocel writes no variable value into the edge bundle's environment. The bundle
carries exactly the union of the edge outputs' own `config.env`.
