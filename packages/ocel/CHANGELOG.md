# ocel

## 0.1.0

### Minor Changes

- fe28c9c: Split the runtime SDK out of `ocel` into a new publishable package `@ocel/sdk`. Runtime imports move from `ocel/*` to `@ocel/sdk/*` (e.g. `@ocel/sdk/postgres`, `@ocel/sdk/blob`, `@ocel/sdk/config`). `ocel` is now the CLI package only; it also ships the node builder, which the Go CLI resolves at runtime via `OCEL_HOME` (no more `go:embed`).
