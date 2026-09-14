export interface SqlStore {
  sql: SqlStorage;
}

function ensureSchema(store: SqlStore): void {
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS registry (
       id INTEGER PRIMARY KEY CHECK (id = 1),
       secret_hash TEXT NOT NULL
     )`,
  );
}

function initialized(store: SqlStore): boolean {
  return (
    store.sql
      .exec(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'registry'`)
      .toArray().length > 0
  );
}

export type Initialization = "adopted" | "held";

export function initialize(store: SqlStore, secretHash: string, force: boolean): Initialization {
  ensureSchema(store);
  const standing = secretHash_(store);
  if (standing !== undefined && standing !== secretHash && !force) return "held";
  store.sql.exec(
    `INSERT INTO registry (id, secret_hash) VALUES (1, ?)
       ON CONFLICT (id) DO UPDATE SET secret_hash = excluded.secret_hash`,
    secretHash,
  );
  return "adopted";
}

export function isSecretHash(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

export function secretHash(store: SqlStore): string | undefined {
  if (!initialized(store)) return undefined;
  return secretHash_(store);
}

function secretHash_(store: SqlStore): string | undefined {
  return store.sql
    .exec<{ secret_hash: string }>(`SELECT secret_hash FROM registry WHERE id = 1`)
    .toArray()[0]?.secret_hash;
}
