# Environment variables and secrets

App code declares the variables it reads, and a deploy refuses to proceed while a required one has no value. There are three classes:
- `plain`
- `sensitive` (redacted in output)
- `secret` (read live on each access, so it can rotate; printed redacted)

Ocel delivers the values. Code reads them through the declaration, never through raw `process.env` or `os.Getenv`. The `OCEL_` prefix is reserved.

## Declare and read

```ts
import { defineEnv } from "ocel/env";

export const env = defineEnv({
  LOG_LEVEL: { class: "plain", schema: z.string().default("info") },
  STRIPE_KEY: { class: "secret" },
});
```

```go
var Env = ocel.Env[struct {
    Port   int         `ocel:"PORT,default=3000"`
    APIKey string      `ocel:"API_KEY,sensitive"`
    Stripe ocel.Secret `ocel:"STRIPE_KEY"`
}]()

key := Env.Stripe.Value()
```

```python
class Env(ocel.Env):
    port: int = 3000
    api_key: str = ocel.var(sensitive=True)
    stripe_key: ocel.Secret

Env().stripe_key.value
```

```rust
#[derive(ocel::Env)]
struct Env {
    #[ocel(default = 3000)] port: u16,
    #[ocel(sensitive)] api_key: String,
    stripe_key: ocel::Secret,
}

let env = Env::load()?;
```

An optional variable is a pointer (Go), an `Option` (Rust), or `optional` on a group (TS). A variable can be scoped to app folders with `folders`.

## Setting values

- **Local dev:** values come from `.env` and `.env.local` (`.env.local` wins). The `envSource` setting in `ocel.json` can point elsewhere.
- **Deployed:** `ocel env set KEY=value` sets production; add `--preview` for previews. Related commands:
  - `ocel env ls`: what is set, masked
  - `ocel env rm`
  - `ocel env history`
- **Missing values:** a deploy missing a value stops with `variables.missing`. The error names the keys; set them, or ask the human for values that are theirs to supply.
- **Reading a value:** `ocel env get KEY` shows it masked. `--reveal` prints it; use that only when the human asks to see it.
