import { Postgres, type PostgresConfig } from "./pg.js";
import { Pool } from "pg";

type PgReturn = Pool & { connectionString: string };

export function postgres(id: string, config?: PostgresConfig): PgReturn {
  const pg = new Postgres(id, config);

  // During discovery, declaration files are imported only so the Postgres
  // constructor above registers the resource with the dev server. The
  // OCEL_RESOURCE_* env this Pool would be built from doesn't exist yet —
  // provisioning happens after discovery — so hand back a placeholder that
  // fails loudly if anything actually touches it in this phase.
  if (process.env.OCEL_PHASE === "discovery") {
    return new Proxy({} as PgReturn, {
      get(_target, prop) {
        throw new Error(
          `'postgres("${id}")' cannot be used during discovery: tried to access '${String(prop)}' before the resource was provisioned`,
        );
      },
    });
  }

  const { connectionString } = pg.__config();

  const client = new Pool({
    connectionString,
  });

  return Object.assign(client, {
    connectionString,
  });
}
