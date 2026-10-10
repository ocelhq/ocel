# Environment variables and secrets

App code declares the variables it reads, and a deploy refuses to proceed while a required one has no value. There are three classes:
- `plain`
- `sensitive` (redacted in output)
- `secret` (read live on each access, so it can rotate; printed redacted)

Ocel delivers the values. Code reads them through the declaration, never through raw `process.env` or `os.Getenv`. A `plain` or `sensitive` value is also in the process environment under its own name, so a library that reads it there itself (an ORM reading `DATABASE_URL`) finds it. The `OCEL_` prefix is reserved for every class.

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

## Values a browser reads (TypeScript)

Import from the framework's own entry instead of `ocel/env`; ocel writes nothing into `tsconfig.json`.

- **Next:** `export const env = defineEnv({ … })` from `ocel/env/next` in a file in the discovery folder, which app code imports `env` from. A `NEXT_PUBLIC_` key is public and must be class `plain`; a confidential one is a type error. Every other key is server-only and throws when read in the browser. Groups work. A value is inlined when Next builds, so `ocel env set` needs a rebuild; under `ocel dev` it restarts the dev server.
- **SvelteKit 3:** `export const variables = defineEnvVars({ … })` from `ocel/env/sveltekit` in a file in the discovery folder, re-exported from `src/env.ts`. SvelteKit owns `public`, `static` and `schema`; class `secret`, `public` on a confidential class and `static` on `sensitive` are refused. `PUBLIC_OCEL_URL` is the deployment URL.

## Setting values

- **Local dev:** values come from `.env` and `.env.local` (`.env.local` wins). The `envSource` setting in `ocel.json` can point elsewhere.
- **Deployed:** `ocel env set KEY=value` sets production; add `--preview` for previews. Related commands:
  - `ocel env ls`: what is set, masked
  - `ocel env rm`
  - `ocel env history`
- **Missing values:** a deploy missing a value stops with `variables.missing`. The error names the keys; set them, or ask the human for values that are theirs to supply.
- **Reading a value:** `ocel env get KEY` shows it masked. `--reveal` prints it; use that only when the human asks to see it.
