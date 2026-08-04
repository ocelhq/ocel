// The per-deploy Durable Object's storage-class logic: SQL over one deploy's
// SQLite. Kept separate from the DO class (isr-deploy.ts) and the HTTP surface
// (index.ts) so it can be exercised directly against a real DO instance's
// storage, the same way workers/deployments-store's store.ts is.

// The subset of DurableObjectStorage this module calls. A real ctx.storage
// satisfies it structurally.
export interface SqlStore {
  sql: SqlStorage;
}

// One row, holding the SHA-256 hash of this deploy's write secret. The
// plaintext secret is minted by the deploy host, handed to the Lambda as an env
// var and never sent to Cloudflare, so this table is the whole of what a
// compromised writer worker could leak.
export function ensureSchema(store: SqlStore): void {
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS registry (
       id INTEGER PRIMARY KEY CHECK (id = 1),
       secret_hash TEXT NOT NULL
     )`,
  );
}

export function initialize(store: SqlStore, secretHash: string): void {
  store.sql.exec(
    `INSERT INTO registry (id, secret_hash) VALUES (1, ?)
       ON CONFLICT (id) DO UPDATE SET secret_hash = excluded.secret_hash`,
    secretHash,
  );
}

export function secretHash(store: SqlStore): string | undefined {
  return store.sql
    .exec<{ secret_hash: string }>(`SELECT secret_hash FROM registry WHERE id = 1`)
    .toArray()[0]?.secret_hash;
}
