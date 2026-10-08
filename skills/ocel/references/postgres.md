# Postgres

One managed Postgres database per declaration. In `ocel dev` it runs in Docker and keeps
its data until `ocel dev --reset`.

## Declare and query

```ts
// infra/index.ts
import { postgres } from "ocel/postgres";
export const db = postgres("main"); // a `pg` pool, connected on first use

// app code
const { rows } = await db.query("SELECT * FROM orders WHERE id = $1", [id]);
```

```go
// infra/infra.go
var DB = ocel.Postgres("main", ocel.PostgresVersion("17"))

// app code: hand the string to pgx, database/sql or an ORM
dsn, err := infra.DB.ConnectionString()
```

```python
# infra/__init__.py
db = ocel.postgres("main")

# app code (asyncpg)
rows = await db.fetch("SELECT 1")  # also db.connection_string, await db.pool()
```

```rust
#[derive(ocel::Resources)]
pub struct Infra {
    #[ocel(name = "main", version = "17")]
    pub db: ocel::Postgres,
}

let infra = Infra::load()?;
```

The version defaults to `17`. In TypeScript, `pg` is a peer dependency the app installs
itself (`npm install pg`).

Discovery runs the declaring file before the database exists, so create tables in a
migration or on first use, never at the declaring file's top level.

## Migrations

Run them as a one-off against the database they target:

- **local:** `ocel run -- <migrate command>`
- **deployed:** `ocel run --env production -- <migrate command>` (or `--env preview`). This
  reaches the deployed database over a port forward and writes to its data, so running it
  against production is the human's call.

## An existing database

`bindings.postgres.<name>` in `ocel.json` binds the declaration to a database Ocel does not
provision: a URL from an env var, or host and credentials, optionally per tier. See
`config.md`.

The SDK's own types and docstrings are the full API.
