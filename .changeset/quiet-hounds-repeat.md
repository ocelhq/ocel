---
"ocel": minor
---

Declare a project's variables in code with `defineEnv` from `ocel/env` and read
them as plain synchronous properties, for every class. A deploy now stops before
anything is built when a required value is missing or fails its schema, naming
the cell and the command that fills it.

`OCEL_` is the only prefix `defineEnv` reserves — Ocel's own namespace, and a
name Ocel would overwrite. Everything else is yours to declare. A name your
deploy target's runtime injects is refused by that provider at deploy, where the
target is actually known: on AWS, `AWS_` and `LAMBDA_` for a plaintext variable,
which the Lambda runtime would otherwise overwrite before your handler read it.

A client-accessible variable may not carry a schema default. Its value is
inlined into the browser bundle at build time, and a default would be
indistinguishable from a value your bundler never inlined.
