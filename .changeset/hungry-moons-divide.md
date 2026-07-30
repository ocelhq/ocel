---
"ocel": minor
---

Give apps their own variable values with folders. An app binds one folder in
`ocel.config.ts` and reads its values from there, falling back to the project
root; a variable declared with `folders` diverges across exactly the folders it
names, is mandatory in each of them, and cannot be set at the root at all.
Reading a variable an app is not scoped to throws `EnvScopeError` naming the
scope and the binding rather than yielding `undefined`.
