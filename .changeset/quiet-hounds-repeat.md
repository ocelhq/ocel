---
"ocel": minor
---

Declare a project's variables in code with `defineEnv` from `ocel/env` and read
them as plain synchronous properties, for every class. A deploy now stops before
anything is built when a required value is missing or fails its schema, naming
the cell and the command that fills it.
